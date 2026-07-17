package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Soyuz-Tec/twintidy/internal/diagnostics"

	"github.com/cespare/xxhash/v2"
)

const (
	boundaryReadSize          = 4 * 1024
	fullHashBufferSize        = 64 * 1024
	directoryReadBatchSize    = 256
	maxScanWorkers            = 64
	defaultMaxScanRoots       = 256
	defaultMaxScanDirectories = 250_000
	defaultMaxScanFiles       = 500_000
)

// ErrScanLimitExceeded identifies an intentional cardinality stop. Its error
// text tells the user which boundary was reached and how to narrow the scan.
var ErrScanLimitExceeded = errors.New("scan cardinality limit exceeded")

type scanLimitError struct {
	kind  string
	limit int64
}

func (e *scanLimitError) Error() string {
	return fmt.Sprintf("%s: reached the configured maximum of %d %s(s); select fewer or smaller folders and scan again", ErrScanLimitExceeded, e.limit, e.kind)
}

func (e *scanLimitError) Unwrap() error { return ErrScanLimitExceeded }

type Engine struct {
	workers int
	limits  ScanLimits
	bufPool sync.Pool
}

func NewEngine(workers int) *Engine {
	return NewEngineWithLimits(workers, DefaultScanLimits())
}

// DefaultScanLimits returns production-safe inventory bounds. The limits are
// intentionally generous enough for whole-profile scans while preventing an
// attacker-controlled directory tree from growing memory without bound.
func DefaultScanLimits() ScanLimits {
	return ScanLimits{
		MaxRoots:       defaultMaxScanRoots,
		MaxDirectories: defaultMaxScanDirectories,
		MaxFiles:       defaultMaxScanFiles,
	}
}

func NewEngineWithLimits(workers int, limits ScanLimits) *Engine {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}
	if workers > maxScanWorkers {
		workers = maxScanWorkers
	}
	limits = normalizeScanLimits(limits)

	return &Engine{
		workers: workers,
		limits:  limits,
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, fullHashBufferSize)
				return &buf
			},
		},
	}
}

func normalizeScanLimits(limits ScanLimits) ScanLimits {
	defaults := DefaultScanLimits()
	if limits.MaxRoots <= 0 {
		limits.MaxRoots = defaults.MaxRoots
	}
	if limits.MaxDirectories <= 0 {
		limits.MaxDirectories = defaults.MaxDirectories
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = defaults.MaxFiles
	}
	return limits
}

func (e *Engine) Workers() int {
	return e.workers
}

func (e *Engine) Scan(ctx context.Context, roots []string, updates chan<- Progress) ([]DuplicateGroup, error) {
	return e.ScanWithOptions(ctx, roots, DefaultScanOptions(), updates)
}

func (e *Engine) ScanWithOptions(ctx context.Context, roots []string, options ScanOptions, updates chan<- Progress) ([]DuplicateGroup, error) {
	report, err := e.SurfaceScan(ctx, roots, updates)
	if err != nil {
		return nil, err
	}
	return e.ScanFiles(ctx, report.Files, options, updates)
}

func (e *Engine) ScanFiles(ctx context.Context, files []FileRecord, options ScanOptions, updates chan<- Progress) ([]DuplicateGroup, error) {
	if int64(len(files)) > e.limits.MaxFiles {
		return nil, &scanLimitError{kind: "file", limit: e.limits.MaxFiles}
	}
	for _, file := range files {
		if !scopeIsValid(file.Scope) {
			return nil, fmt.Errorf("cannot scan unscoped record %q: %w", file.Path, errMissingScope)
		}
	}
	options = NormalizeScanOptions(options)
	filtered := filterRecordsByOptions(files, options)
	startedAt := time.Now()
	refreshed, errorsIgnored, err := e.refreshRecords(ctx, filtered, updates, startedAt)
	if err != nil {
		return nil, err
	}
	if len(refreshed) == 0 {
		sendProgress(updates, Progress{
			Stage:         StageDone,
			ErrorsIgnored: errorsIgnored,
			StartedAt:     startedAt,
			Message:       "No stable, supported files matched the selected user-file categories.",
		})
		return nil, nil
	}

	sizeGroups := sizeGroupsFromRecords(refreshed)
	return e.scanSizeGroups(ctx, sizeGroups, errorsIgnored, updates, startedAt)
}

func (e *Engine) refreshRecords(ctx context.Context, files []FileRecord, updates chan<- Progress, startedAt time.Time) ([]FileRecord, int64, error) {
	refreshed := make([]FileRecord, 0, len(files))
	var errorsIgnored int64
	for index, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, errorsIgnored, err
		}

		current, err := refreshFileRecord(file)
		if err != nil {
			if errors.Is(err, errRootChanged) || errors.Is(err, errMissingScope) {
				return nil, errorsIgnored, err
			}
			errorsIgnored++
			continue
		}
		refreshed = append(refreshed, current)

		if (index+1)%128 == 0 {
			sendProgress(updates, Progress{
				Stage:          StageSizeMapping,
				CurrentPath:    current.Path,
				FilesProcessed: int64(index + 1),
				FilesTotal:     int64(len(files)),
				ErrorsIgnored:  errorsIgnored,
				StartedAt:      startedAt,
				Message:        "Refreshing current file metadata and identity.",
			})
		}
	}
	return refreshed, errorsIgnored, nil
}

func (e *Engine) scanSizeGroups(ctx context.Context, sizeGroups map[int64][]FileRecord, errorsIgnored int64, updates chan<- Progress, startedAt time.Time) ([]DuplicateGroup, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var ignored int64 = errorsIgnored
	stageTwoCandidates := filesFromDuplicateSizeGroups(sizeGroups)
	sendProgress(updates, Progress{
		Stage:          StageSizeMapping,
		FilesProcessed: int64(len(stageTwoCandidates)),
		FilesTotal:     int64(len(stageTwoCandidates)),
		ErrorsIgnored:  atomic.LoadInt64(&ignored),
		StartedAt:      startedAt,
		Message:        "Size mapping complete.",
	})

	if len(stageTwoCandidates) == 0 {
		sendProgress(updates, Progress{
			Stage:         StageDone,
			StartedAt:     startedAt,
			ErrorsIgnored: atomic.LoadInt64(&ignored),
			Message:       "No duplicate candidates found.",
		})
		return nil, nil
	}

	boundaryGroups, err := e.mapByBoundaryHash(ctx, stageTwoCandidates, updates, startedAt, &ignored)
	if err != nil {
		return nil, err
	}
	stageThreeCandidates := filesFromBoundaryGroups(boundaryGroups)
	if len(stageThreeCandidates) == 0 {
		sendProgress(updates, Progress{
			Stage:         StageDone,
			StartedAt:     startedAt,
			ErrorsIgnored: atomic.LoadInt64(&ignored),
			Message:       "No exact-match candidates found after fast boundary hashing.",
		})
		return nil, nil
	}

	duplicates, err := e.mapByFullHash(ctx, stageThreeCandidates, updates, startedAt, &ignored)
	if err != nil {
		return nil, err
	}

	sendProgress(updates, Progress{
		Stage:         StageDone,
		GroupsFound:   len(duplicates),
		ErrorsIgnored: atomic.LoadInt64(&ignored),
		StartedAt:     startedAt,
		Message:       fmt.Sprintf("Scan complete. Found %d duplicate groups.", len(duplicates)),
	})
	return duplicates, nil
}

func (e *Engine) SurfaceScan(ctx context.Context, roots []string, updates chan<- Progress) (SurfaceReport, error) {
	if len(roots) == 0 {
		return SurfaceReport{}, errors.New("no source folders selected")
	}

	startedAt := time.Now()
	report, err := e.collectSurfaceRecords(ctx, roots, updates, startedAt)
	if err != nil {
		return SurfaceReport{}, err
	}

	sendProgress(updates, Progress{
		Stage:              StageSurfaceScan,
		FilesProcessed:     report.TotalFiles,
		DirectoriesScanned: report.DirectoriesScanned,
		ErrorsIgnored:      report.ErrorsIgnored,
		SkippedSystemItems: report.SkippedSystemItems,
		StartedAt:          startedAt,
		Message:            fmt.Sprintf("Surface scan complete. Found %d user-created file(s).", report.TotalFiles),
	})
	return report, nil
}

func (e *Engine) collectSurfaceRecords(ctx context.Context, roots []string, updates chan<- Progress, startedAt time.Time) (SurfaceReport, error) {
	if len(roots) > e.limits.MaxRoots {
		return SurfaceReport{}, &scanLimitError{kind: "root", limit: int64(e.limits.MaxRoots)}
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		mu                 sync.Mutex
		totalBytes         int64
		directoriesScanned int64
		errorsIgnored      int64
		skippedSystemItems int64
	)
	budget := scanBudget{limits: e.limits}

	report := SurfaceReport{
		Files:         make([]FileRecord, 0),
		CategoryStats: make(map[FileCategory]SurfaceCategoryStats, len(categoryDefinitions)),
	}
	for _, definition := range categoryDefinitions {
		report.CategoryStats[definition.Category] = SurfaceCategoryStats{}
	}

	rootDirs := make([]directoryWork, 0, len(roots))
	rootScopes := make([]AuthorizedScope, 0, len(roots))
	seenRoots := make(map[FileIdentity]struct{}, len(roots))

	recordFile := func(path string, scope AuthorizedScope) error {
		if err := scanCtx.Err(); err != nil {
			return err
		}
		if !IsUserCreatedFilePath(path) {
			atomic.AddInt64(&skippedSystemItems, 1)
			return nil
		}

		file, snapshot, finalPath, err := openSurfaceFileSnapshot(path, scope)
		if err != nil {
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}
		processed, err := budget.reserveFile()
		if err != nil {
			return err
		}

		category := CategoryForPath(finalPath)
		record := FileRecord{
			Path:         finalPath,
			Size:         snapshot.size,
			CreatedAt:    bestEffortCreatedAt(finalPath, snapshot.modifiedAt),
			ModifiedAt:   snapshot.modifiedAt,
			Category:     category,
			Identity:     snapshot.identity,
			LinkCount:    snapshot.linkCount,
			NamedStreams: snapshot.namedStreams,
			Scope:        scope,
		}

		mu.Lock()
		report.Files = append(report.Files, record)
		stats := report.CategoryStats[category]
		stats.Files++
		stats.Bytes += record.Size
		report.CategoryStats[category] = stats
		mu.Unlock()

		atomic.AddInt64(&totalBytes, record.Size)
		if processed%128 == 0 {
			sendProgress(updates, Progress{
				Stage:              StageSurfaceScan,
				CurrentPath:        finalPath,
				FilesProcessed:     processed,
				DirectoriesScanned: atomic.LoadInt64(&directoriesScanned),
				ErrorsIgnored:      atomic.LoadInt64(&errorsIgnored),
				SkippedSystemItems: atomic.LoadInt64(&skippedSystemItems),
				StartedAt:          startedAt,
				Message:            "Reading user file metadata.",
			})
		}
		return nil
	}

	for _, root := range roots {
		if err := scanCtx.Err(); err != nil {
			return SurfaceReport{}, err
		}
		absRoot, err := filepath.Abs(root)
		if err != nil {
			atomic.AddInt64(&errorsIgnored, 1)
			continue
		}
		absRoot = filepath.Clean(absRoot)
		if ShouldSkipDirectory(absRoot) {
			atomic.AddInt64(&skippedSystemItems, 1)
			continue
		}

		scope, info, err := authorizeScanRoot(absRoot)
		if err != nil {
			if errors.Is(err, errReparsePoint) {
				return SurfaceReport{}, fmt.Errorf("unsafe scan root %q: %w", root, err)
			}
			atomic.AddInt64(&errorsIgnored, 1)
			continue
		}
		if ShouldSkipDirectory(scope.RootFinalPath) {
			atomic.AddInt64(&skippedSystemItems, 1)
			continue
		}
		if _, seen := seenRoots[scope.RootIdentity]; seen {
			continue
		}
		seenRoots[scope.RootIdentity] = struct{}{}
		rootScopes = append(rootScopes, scope)
		if info.IsDir() {
			if _, err := budget.reserveDirectory(); err != nil {
				return SurfaceReport{}, err
			}
			rootDirs = append(rootDirs, directoryWork{path: scope.RootFinalPath, scope: scope})
			continue
		}
		if err := recordFile(scope.RootFinalPath, scope); err != nil {
			if errors.Is(err, ErrScanLimitExceeded) || errors.Is(err, errRootChanged) || errors.Is(err, errMissingScope) {
				return SurfaceReport{}, err
			}
			if errors.Is(err, errReparsePoint) || errors.Is(err, errScopeEscape) {
				atomic.AddInt64(&skippedSystemItems, 1)
			} else {
				atomic.AddInt64(&errorsIgnored, 1)
			}
		}
	}

	if len(rootDirs) == 0 && atomic.LoadInt64(&budget.files) == 0 {
		return SurfaceReport{}, errors.New("no readable user-created files were found")
	}

	queue := newDirectoryQueue(rootDirs)
	queueWatcherDone := make(chan struct{})
	go func() {
		defer diagnostics.ReportPanicAndRepanic("scanner directory queue watcher", map[string]string{"roots": fmt.Sprint(len(rootDirs))})
		select {
		case <-scanCtx.Done():
			queue.close()
		case <-queueWatcherDone:
		}
	}()

	var wg sync.WaitGroup
	var scanErr error
	var failOnce sync.Once
	fail := func(err error) {
		if err == nil {
			return
		}
		failOnce.Do(func() {
			scanErr = err
			cancel()
			queue.close()
		})
	}
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer diagnostics.ReportPanicAndRepanic("scanner size mapping worker", map[string]string{"worker": fmt.Sprint(workerID)})
			for {
				work, ok := queue.next()
				if !ok {
					return
				}
				err := e.readDirectory(scanCtx, work, queue, &budget, recordFile, &directoriesScanned, &errorsIgnored, &skippedSystemItems, updates, startedAt)
				queue.done()
				if err != nil {
					fail(err)
					return
				}
			}
		}(i)
	}
	wg.Wait()
	close(queueWatcherDone)

	if err := ctx.Err(); err != nil {
		return SurfaceReport{}, err
	}
	if scanErr != nil {
		return SurfaceReport{}, scanErr
	}
	for _, scope := range rootScopes {
		if err := validateAuthorizedScope(scope); err != nil {
			return SurfaceReport{}, err
		}
	}

	sendProgress(updates, Progress{
		Stage:              StageSurfaceScan,
		FilesProcessed:     atomic.LoadInt64(&budget.files),
		DirectoriesScanned: atomic.LoadInt64(&directoriesScanned),
		ErrorsIgnored:      atomic.LoadInt64(&errorsIgnored),
		SkippedSystemItems: atomic.LoadInt64(&skippedSystemItems),
		StartedAt:          startedAt,
		Message:            "Finished reading user-created files.",
	})

	report.TotalFiles = atomic.LoadInt64(&budget.files)
	report.TotalBytes = atomic.LoadInt64(&totalBytes)
	report.DirectoriesScanned = atomic.LoadInt64(&directoriesScanned)
	report.ErrorsIgnored = atomic.LoadInt64(&errorsIgnored)
	report.SkippedSystemItems = atomic.LoadInt64(&skippedSystemItems)
	return report, nil
}

func (e *Engine) readDirectory(
	ctx context.Context,
	work directoryWork,
	queue *directoryQueue,
	budget *scanBudget,
	recordFile func(string, AuthorizedScope) error,
	directoriesScanned *int64,
	errorsIgnored *int64,
	skippedSystemItems *int64,
	updates chan<- Progress,
	startedAt time.Time,
) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	directory, finalDir, err := openDirectoryInScope(work.path, work.scope)
	if err != nil {
		if errors.Is(err, errRootChanged) || errors.Is(err, errMissingScope) {
			return err
		}
		if errors.Is(err, errReparsePoint) || errors.Is(err, errScopeEscape) {
			atomic.AddInt64(skippedSystemItems, 1)
		} else {
			atomic.AddInt64(errorsIgnored, 1)
		}
		return nil
	}
	defer directory.Close()

	sendProgress(updates, Progress{
		Stage:              StageSurfaceScan,
		CurrentPath:        finalDir,
		DirectoriesScanned: atomic.LoadInt64(directoriesScanned),
		ErrorsIgnored:      atomic.LoadInt64(errorsIgnored),
		SkippedSystemItems: atomic.LoadInt64(skippedSystemItems),
		StartedAt:          startedAt,
		Message:            "Scanning directory.",
	})

	atomic.AddInt64(directoriesScanned, 1)

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, readErr := directory.Readdir(directoryReadBatchSize)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			name := entry.Name()
			if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
				atomic.AddInt64(skippedSystemItems, 1)
				continue
			}

			path := filepath.Join(finalDir, name)
			reparse, err := pathIsTraversalReparsePoint(path)
			if err != nil {
				atomic.AddInt64(errorsIgnored, 1)
				continue
			}
			if reparse || entry.Mode()&os.ModeSymlink != 0 {
				atomic.AddInt64(skippedSystemItems, 1)
				continue
			}
			if entry.IsDir() {
				if ShouldSkipDirectory(path) {
					atomic.AddInt64(skippedSystemItems, 1)
					continue
				}
				if _, err := budget.reserveDirectory(); err != nil {
					return err
				}
				if !queue.add(directoryWork{path: path, scope: work.scope}) {
					if err := ctx.Err(); err != nil {
						return err
					}
					return errors.New("directory queue closed before scan completed")
				}
				continue
			}
			if !entry.Mode().IsRegular() {
				continue
			}

			if err := recordFile(path, work.scope); err != nil {
				if errors.Is(err, ErrScanLimitExceeded) || errors.Is(err, errRootChanged) || errors.Is(err, errMissingScope) {
					return err
				}
				if errors.Is(err, errReparsePoint) || errors.Is(err, errScopeEscape) {
					atomic.AddInt64(skippedSystemItems, 1)
				} else {
					atomic.AddInt64(errorsIgnored, 1)
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			atomic.AddInt64(errorsIgnored, 1)
			return nil
		}
		if len(entries) == 0 {
			atomic.AddInt64(errorsIgnored, 1)
			return nil
		}
	}
}

type directoryWork struct {
	path  string
	scope AuthorizedScope
}

type scanBudget struct {
	limits      ScanLimits
	files       int64
	directories int64
}

func (b *scanBudget) reserveFile() (int64, error) {
	return reserveCardinality(&b.files, b.limits.MaxFiles, "file")
}

func (b *scanBudget) reserveDirectory() (int64, error) {
	return reserveCardinality(&b.directories, b.limits.MaxDirectories, "directory")
}

func reserveCardinality(counter *int64, limit int64, kind string) (int64, error) {
	for {
		current := atomic.LoadInt64(counter)
		if current >= limit {
			return current, &scanLimitError{kind: kind, limit: limit}
		}
		if atomic.CompareAndSwapInt64(counter, current, current+1) {
			return current + 1, nil
		}
	}
}

type boundaryKey struct {
	size int64
	hash uint64
}

type boundaryResult struct {
	file FileRecord
	key  boundaryKey
	err  error
}

func (e *Engine) mapByBoundaryHash(ctx context.Context, files []FileRecord, updates chan<- Progress, startedAt time.Time, errorsIgnored *int64) (map[boundaryKey][]FileRecord, error) {
	jobs := make(chan FileRecord)
	results := make(chan boundaryResult)

	var wg sync.WaitGroup
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer diagnostics.ReportPanicAndRepanic("scanner boundary hash worker", map[string]string{"worker": fmt.Sprint(workerID)})
			for file := range jobs {
				key, err := e.boundaryHash(ctx, file)
				select {
				case results <- boundaryResult{file: file, key: key, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}(i)
	}

	go func() {
		defer diagnostics.ReportPanicAndRepanic("scanner boundary job producer", map[string]string{"files": fmt.Sprint(len(files))})
		defer close(jobs)
		for _, file := range files {
			select {
			case jobs <- file:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		defer diagnostics.ReportPanicAndRepanic("scanner boundary result closer", nil)
		wg.Wait()
		close(results)
	}()

	var processed int64
	var fatalError error
	groups := make(map[boundaryKey][]FileRecord)
	for result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		processed++
		if result.err != nil {
			if fatalError == nil && (errors.Is(result.err, errRootChanged) || errors.Is(result.err, errMissingScope)) {
				fatalError = result.err
			} else {
				atomic.AddInt64(errorsIgnored, 1)
			}
		} else {
			groups[result.key] = append(groups[result.key], result.file)
		}

		sendProgress(updates, Progress{
			Stage:          StageBoundaryHashing,
			CurrentPath:    result.file.Path,
			FilesProcessed: processed,
			FilesTotal:     int64(len(files)),
			ErrorsIgnored:  atomic.LoadInt64(errorsIgnored),
			StartedAt:      startedAt,
			Message:        "Comparing file heads and tails.",
		})
	}
	if fatalError != nil {
		return nil, fatalError
	}

	for key, group := range groups {
		if len(group) < 2 {
			delete(groups, key)
		}
	}

	return groups, ctx.Err()
}

func (e *Engine) boundaryHash(ctx context.Context, file FileRecord) (boundaryKey, error) {
	if err := ctx.Err(); err != nil {
		return boundaryKey{}, err
	}

	f, snapshot, _, err := openFileSnapshotInScope(file.Path, file.Scope)
	if err != nil {
		return boundaryKey{}, err
	}
	defer f.Close()
	if !recordMatchesSnapshot(file, snapshot) {
		return boundaryKey{}, errFileChanged
	}
	if snapshot.linkCount > 1 {
		return boundaryKey{}, errHardLinkedFile
	}
	if snapshot.namedStreams > 0 {
		return boundaryKey{}, errNamedStreamFile
	}

	var head [boundaryReadSize]byte
	var tail [boundaryReadSize]byte
	headLen := minInt64(boundaryReadSize, snapshot.size)
	tailLen := minInt64(boundaryReadSize, snapshot.size)

	n, err := f.ReadAt(head[:headLen], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return boundaryKey{}, err
	}
	if n != headLen {
		return boundaryKey{}, io.ErrUnexpectedEOF
	}

	if err := ctx.Err(); err != nil {
		return boundaryKey{}, err
	}
	offset := snapshot.size - int64(tailLen)
	n, err = f.ReadAt(tail[:tailLen], offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return boundaryKey{}, err
	}
	if n != tailLen {
		return boundaryKey{}, io.ErrUnexpectedEOF
	}
	if err := verifyStableSnapshotInScope(f, file.Path, snapshot, file.Scope); err != nil {
		return boundaryKey{}, err
	}

	hasher := xxhash.New()
	var sizeBytes [8]byte
	binary.LittleEndian.PutUint64(sizeBytes[:], uint64(snapshot.size))
	_, _ = hasher.Write(sizeBytes[:])
	_, _ = hasher.Write(head[:headLen])
	_, _ = hasher.Write(tail[:tailLen])

	return boundaryKey{size: snapshot.size, hash: hasher.Sum64()}, nil
}

type fullHashJob struct {
	file FileRecord
}

type fullHashResult struct {
	file        FileRecord
	hash        string
	bytesHashed int64
	err         error
}

func (e *Engine) mapByFullHash(ctx context.Context, files []FileRecord, updates chan<- Progress, startedAt time.Time, errorsIgnored *int64) ([]DuplicateGroup, error) {
	jobs := make(chan fullHashJob)
	results := make(chan fullHashResult)

	var wg sync.WaitGroup
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer diagnostics.ReportPanicAndRepanic("scanner full hash worker", map[string]string{"worker": fmt.Sprint(workerID)})
			for job := range jobs {
				hash, bytesHashed, err := e.fullHash(ctx, job.file)
				select {
				case results <- fullHashResult{file: job.file, hash: hash, bytesHashed: bytesHashed, err: err}:
				case <-ctx.Done():
					return
				}
			}
		}(i)
	}

	go func() {
		defer diagnostics.ReportPanicAndRepanic("scanner full hash job producer", map[string]string{"files": fmt.Sprint(len(files))})
		defer close(jobs)
		for _, file := range files {
			select {
			case jobs <- fullHashJob{file: file}:
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		defer diagnostics.ReportPanicAndRepanic("scanner full hash result closer", nil)
		wg.Wait()
		close(results)
	}()

	type fullKey struct {
		size int64
		hash string
	}

	var (
		processed   int64
		bytesHashed int64
		fatalError  error
	)
	groups := make(map[fullKey][]FileRecord)
	for result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		processed++
		bytesHashed += result.bytesHashed
		if result.err != nil {
			if fatalError == nil && (errors.Is(result.err, errRootChanged) || errors.Is(result.err, errMissingScope)) {
				fatalError = result.err
			} else {
				atomic.AddInt64(errorsIgnored, 1)
			}
		} else {
			key := fullKey{size: result.file.Size, hash: result.hash}
			groups[key] = append(groups[key], result.file)
		}

		sendProgress(updates, Progress{
			Stage:          StageFullHashing,
			CurrentPath:    result.file.Path,
			FilesProcessed: processed,
			FilesTotal:     int64(len(files)),
			BytesHashed:    bytesHashed,
			ErrorsIgnored:  atomic.LoadInt64(errorsIgnored),
			StartedAt:      startedAt,
			Message:        "Streaming full file hashes.",
		})
	}
	if fatalError != nil {
		return nil, fatalError
	}

	duplicates := make([]DuplicateGroup, 0)
	for key, group := range groups {
		if len(group) < 2 {
			continue
		}
		sort.Slice(group, func(i, j int) bool {
			if !group[i].ModifiedAt.Equal(group[j].ModifiedAt) {
				return group[i].ModifiedAt.After(group[j].ModifiedAt)
			}
			return group[i].Path < group[j].Path
		})
		duplicates = append(duplicates, DuplicateGroup{Size: key.size, Hash: key.hash, Files: group})
	}

	sort.Slice(duplicates, func(i, j int) bool {
		if duplicates[i].Size != duplicates[j].Size {
			return duplicates[i].Size > duplicates[j].Size
		}
		return duplicates[i].Hash < duplicates[j].Hash
	})

	return duplicates, ctx.Err()
}

func (e *Engine) fullHash(ctx context.Context, file FileRecord) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}

	f, snapshot, _, err := openFileSnapshotInScope(file.Path, file.Scope)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	if !recordMatchesSnapshot(file, snapshot) {
		return "", 0, errFileChanged
	}
	if snapshot.linkCount > 1 {
		return "", 0, errHardLinkedFile
	}
	if snapshot.namedStreams > 0 {
		return "", 0, errNamedStreamFile
	}

	hasher := sha256.New()
	bufPtr := e.bufPool.Get().(*[]byte)
	buf := *bufPtr
	defer e.bufPool.Put(bufPtr)

	written, err := copyWithContext(ctx, hasher, f, buf)
	if err != nil {
		return "", written, err
	}
	if err := verifyStableSnapshotInScope(f, file.Path, snapshot, file.Scope); err != nil {
		return "", written, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
}

func copyWithContext(ctx context.Context, destination io.Writer, source io.Reader, buffer []byte) (int64, error) {
	var written int64
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}

		read, readErr := source.Read(buffer)
		if read > 0 {
			count, writeErr := destination.Write(buffer[:read])
			written += int64(count)
			if writeErr != nil {
				return written, writeErr
			}
			if count != read {
				return written, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return written, nil
		}
		if readErr != nil {
			return written, readErr
		}
		if read == 0 {
			return written, io.ErrNoProgress
		}
	}
}

func filterRecordsByOptions(files []FileRecord, options ScanOptions) []FileRecord {
	filtered := make([]FileRecord, 0, len(files))
	for _, file := range files {
		if options.UserFilesOnly && !IsUserCreatedFilePath(file.Path) {
			continue
		}
		category := file.Category
		if category == "" {
			category = CategoryForPath(file.Path)
			file.Category = category
		}
		if !options.Categories[category] {
			continue
		}
		filtered = append(filtered, file)
	}
	return filtered
}

func sizeGroupsFromRecords(files []FileRecord) map[int64][]FileRecord {
	groups := make(map[int64][]FileRecord)
	for _, file := range files {
		groups[file.Size] = append(groups[file.Size], file)
	}
	return groups
}

func filesFromDuplicateSizeGroups(groups map[int64][]FileRecord) []FileRecord {
	files := make([]FileRecord, 0)
	sizes := make([]int64, 0, len(groups))
	for size, group := range groups {
		if len(group) > 1 {
			sizes = append(sizes, size)
		}
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i] > sizes[j] })
	for _, size := range sizes {
		group := groups[size]
		sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })
		files = append(files, group...)
	}
	return files
}

func filesFromBoundaryGroups(groups map[boundaryKey][]FileRecord) []FileRecord {
	keys := make([]boundaryKey, 0, len(groups))
	for key, group := range groups {
		if len(group) > 1 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].size != keys[j].size {
			return keys[i].size > keys[j].size
		}
		return keys[i].hash < keys[j].hash
	})

	files := make([]FileRecord, 0)
	for _, key := range keys {
		group := groups[key]
		sort.Slice(group, func(i, j int) bool { return group[i].Path < group[j].Path })
		files = append(files, group...)
	}
	return files
}

func sendProgress(updates chan<- Progress, progress Progress) {
	if updates == nil {
		return
	}
	select {
	case updates <- progress:
	default:
	}
}

func minInt64(limit int, value int64) int {
	if value < int64(limit) {
		return int(value)
	}
	return limit
}

type directoryQueue struct {
	mu      sync.Mutex
	cond    *sync.Cond
	dirs    []directoryWork
	pending int
	closed  bool
}

func newDirectoryQueue(roots []directoryWork) *directoryQueue {
	q := &directoryQueue{
		dirs:    append([]directoryWork(nil), roots...),
		pending: len(roots),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *directoryQueue) add(dir directoryWork) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return false
	}
	q.pending++
	q.dirs = append(q.dirs, dir)
	q.cond.Signal()
	return true
}

func (q *directoryQueue) next() (directoryWork, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.dirs) == 0 && q.pending > 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed || q.pending == 0 {
		return directoryWork{}, false
	}
	dir := q.dirs[len(q.dirs)-1]
	q.dirs = q.dirs[:len(q.dirs)-1]
	return dir, true
}

func (q *directoryQueue) done() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.pending > 0 {
		q.pending--
	}
	if q.pending == 0 {
		q.cond.Broadcast()
	}
}

func (q *directoryQueue) close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.closed = true
	q.cond.Broadcast()
}

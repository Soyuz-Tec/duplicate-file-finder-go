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

	"duplicate-file-finder-go/internal/diagnostics"

	"github.com/cespare/xxhash/v2"
	"github.com/karrick/godirwalk"
)

const (
	boundaryReadSize   = 4 * 1024
	fullHashBufferSize = 64 * 1024
)

type Engine struct {
	workers int
	bufPool sync.Pool
}

func NewEngine(workers int) *Engine {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	if workers < 1 {
		workers = 1
	}

	return &Engine{
		workers: workers,
		bufPool: sync.Pool{
			New: func() any {
				buf := make([]byte, fullHashBufferSize)
				return &buf
			},
		},
	}
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
	options = NormalizeScanOptions(options)
	filtered := filterRecordsByOptions(files, options)
	if len(filtered) == 0 {
		sendProgress(updates, Progress{
			Stage:     StageDone,
			StartedAt: time.Now(),
			Message:   "No files matched the selected user-file categories.",
		})
		return nil, nil
	}

	startedAt := time.Now()
	sizeGroups := sizeGroupsFromRecords(filtered)
	return e.scanSizeGroups(ctx, sizeGroups, 0, updates, startedAt)
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
	var (
		mu                 sync.Mutex
		filesProcessed     int64
		totalBytes         int64
		directoriesScanned int64
		errorsIgnored      int64
		skippedSystemItems int64
	)

	report := SurfaceReport{
		Files:         make([]FileRecord, 0),
		CategoryStats: make(map[FileCategory]SurfaceCategoryStats, len(categoryDefinitions)),
	}
	for _, definition := range categoryDefinitions {
		report.CategoryStats[definition.Category] = SurfaceCategoryStats{}
	}

	rootDirs := make([]string, 0, len(roots))
	seenRoots := make(map[string]struct{}, len(roots))

	recordFile := func(path string, info os.FileInfo) {
		if !info.Mode().IsRegular() {
			return
		}
		if !IsUserCreatedFilePath(path) {
			atomic.AddInt64(&skippedSystemItems, 1)
			return
		}

		category := CategoryForPath(path)
		record := FileRecord{
			Path:       path,
			Size:       info.Size(),
			CreatedAt:  bestEffortCreatedAt(path, info.ModTime()),
			ModifiedAt: info.ModTime(),
			Category:   category,
		}

		mu.Lock()
		report.Files = append(report.Files, record)
		stats := report.CategoryStats[category]
		stats.Files++
		stats.Bytes += record.Size
		report.CategoryStats[category] = stats
		mu.Unlock()

		processed := atomic.AddInt64(&filesProcessed, 1)
		atomic.AddInt64(&totalBytes, record.Size)
		if processed%128 == 0 {
			sendProgress(updates, Progress{
				Stage:              StageSurfaceScan,
				CurrentPath:        path,
				FilesProcessed:     processed,
				DirectoriesScanned: atomic.LoadInt64(&directoriesScanned),
				ErrorsIgnored:      atomic.LoadInt64(&errorsIgnored),
				SkippedSystemItems: atomic.LoadInt64(&skippedSystemItems),
				StartedAt:          startedAt,
				Message:            "Reading user file metadata.",
			})
		}
	}

	for _, root := range roots {
		absRoot, err := filepath.Abs(root)
		if err != nil {
			atomic.AddInt64(&errorsIgnored, 1)
			continue
		}
		absRoot = filepath.Clean(absRoot)
		if _, seen := seenRoots[absRoot]; seen {
			continue
		}
		seenRoots[absRoot] = struct{}{}

		if ShouldSkipDirectory(absRoot) {
			atomic.AddInt64(&skippedSystemItems, 1)
			continue
		}

		info, err := os.Stat(absRoot)
		if err != nil {
			atomic.AddInt64(&errorsIgnored, 1)
			continue
		}
		if info.IsDir() {
			rootDirs = append(rootDirs, absRoot)
			continue
		}
		recordFile(absRoot, info)
	}

	if len(rootDirs) == 0 && atomic.LoadInt64(&filesProcessed) == 0 {
		return SurfaceReport{}, errors.New("no readable user-created files were found")
	}

	queue := newDirectoryQueue(rootDirs)
	queueWatcherDone := make(chan struct{})
	go func() {
		defer diagnostics.ReportPanicAndRepanic("scanner directory queue watcher", map[string]string{"roots": fmt.Sprint(len(rootDirs))})
		select {
		case <-ctx.Done():
			queue.close()
		case <-queueWatcherDone:
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < e.workers; i++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			defer diagnostics.ReportPanicAndRepanic("scanner size mapping worker", map[string]string{"worker": fmt.Sprint(workerID)})
			scratch := make([]byte, godirwalk.MinimumScratchBufferSize)
			for {
				dir, ok := queue.next()
				if !ok {
					return
				}
				e.readDirectory(ctx, dir, scratch, queue, recordFile, &directoriesScanned, &errorsIgnored, &skippedSystemItems, updates, startedAt)
				queue.done()
			}
		}(i)
	}
	wg.Wait()
	close(queueWatcherDone)

	if err := ctx.Err(); err != nil {
		return SurfaceReport{}, err
	}

	sendProgress(updates, Progress{
		Stage:              StageSurfaceScan,
		FilesProcessed:     atomic.LoadInt64(&filesProcessed),
		DirectoriesScanned: atomic.LoadInt64(&directoriesScanned),
		ErrorsIgnored:      atomic.LoadInt64(&errorsIgnored),
		SkippedSystemItems: atomic.LoadInt64(&skippedSystemItems),
		StartedAt:          startedAt,
		Message:            "Finished reading user-created files.",
	})

	report.TotalFiles = atomic.LoadInt64(&filesProcessed)
	report.TotalBytes = atomic.LoadInt64(&totalBytes)
	report.DirectoriesScanned = atomic.LoadInt64(&directoriesScanned)
	report.ErrorsIgnored = atomic.LoadInt64(&errorsIgnored)
	report.SkippedSystemItems = atomic.LoadInt64(&skippedSystemItems)
	return report, nil
}

func (e *Engine) readDirectory(
	ctx context.Context,
	dir string,
	scratch []byte,
	queue *directoryQueue,
	recordFile func(string, os.FileInfo),
	directoriesScanned *int64,
	errorsIgnored *int64,
	skippedSystemItems *int64,
	updates chan<- Progress,
	startedAt time.Time,
) {
	if ctx.Err() != nil {
		return
	}

	sendProgress(updates, Progress{
		Stage:              StageSurfaceScan,
		CurrentPath:        dir,
		DirectoriesScanned: atomic.LoadInt64(directoriesScanned),
		ErrorsIgnored:      atomic.LoadInt64(errorsIgnored),
		SkippedSystemItems: atomic.LoadInt64(skippedSystemItems),
		StartedAt:          startedAt,
		Message:            "Scanning directory.",
	})

	entries, err := godirwalk.ReadDirents(dir, scratch)
	if err != nil {
		atomic.AddInt64(errorsIgnored, 1)
		return
	}
	atomic.AddInt64(directoriesScanned, 1)

	for _, entry := range entries {
		if ctx.Err() != nil {
			return
		}

		path := filepath.Join(dir, entry.Name())
		if entry.IsSymlink() {
			continue
		}
		if entry.IsDir() {
			if ShouldSkipDirectory(path) {
				atomic.AddInt64(skippedSystemItems, 1)
				continue
			}
			queue.add(path)
			continue
		}
		if !entry.IsRegular() {
			continue
		}

		info, err := os.Stat(path)
		if err != nil {
			atomic.AddInt64(errorsIgnored, 1)
			continue
		}
		recordFile(path, info)
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
				key, err := e.boundaryHash(file)
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
	groups := make(map[boundaryKey][]FileRecord)
	for result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		processed++
		if result.err != nil {
			atomic.AddInt64(errorsIgnored, 1)
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

	for key, group := range groups {
		if len(group) < 2 {
			delete(groups, key)
		}
	}

	return groups, ctx.Err()
}

func (e *Engine) boundaryHash(file FileRecord) (boundaryKey, error) {
	f, err := os.Open(file.Path)
	if err != nil {
		return boundaryKey{}, err
	}
	defer f.Close()

	var head [boundaryReadSize]byte
	var tail [boundaryReadSize]byte
	headLen := minInt64(boundaryReadSize, file.Size)
	tailLen := minInt64(boundaryReadSize, file.Size)

	n, err := f.ReadAt(head[:headLen], 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return boundaryKey{}, err
	}
	if n != headLen {
		return boundaryKey{}, io.ErrUnexpectedEOF
	}

	offset := file.Size - int64(tailLen)
	n, err = f.ReadAt(tail[:tailLen], offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return boundaryKey{}, err
	}
	if n != tailLen {
		return boundaryKey{}, io.ErrUnexpectedEOF
	}

	hasher := xxhash.New()
	var sizeBytes [8]byte
	binary.LittleEndian.PutUint64(sizeBytes[:], uint64(file.Size))
	_, _ = hasher.Write(sizeBytes[:])
	_, _ = hasher.Write(head[:headLen])
	_, _ = hasher.Write(tail[:tailLen])

	return boundaryKey{size: file.Size, hash: hasher.Sum64()}, nil
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
	)
	groups := make(map[fullKey][]FileRecord)
	for result := range results {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		processed++
		bytesHashed += result.bytesHashed
		if result.err != nil {
			atomic.AddInt64(errorsIgnored, 1)
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

	f, err := os.Open(file.Path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	hasher := sha256.New()
	bufPtr := e.bufPool.Get().(*[]byte)
	buf := *bufPtr
	defer e.bufPool.Put(bufPtr)

	// io.CopyBuffer streams the file through a pooled 64KB buffer, avoiding
	// whole-file reads and reducing GC pressure on large SSD scans.
	written, err := io.CopyBuffer(hasher, f, buf)
	if err != nil {
		return "", written, err
	}

	if err := ctx.Err(); err != nil {
		return "", written, err
	}
	return hex.EncodeToString(hasher.Sum(nil)), written, nil
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
	dirs    []string
	pending int
	closed  bool
}

func newDirectoryQueue(roots []string) *directoryQueue {
	q := &directoryQueue{
		dirs:    append([]string(nil), roots...),
		pending: len(roots),
	}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *directoryQueue) add(dir string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return
	}
	q.pending++
	q.dirs = append(q.dirs, dir)
	q.cond.Signal()
}

func (q *directoryQueue) next() (string, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.dirs) == 0 && q.pending > 0 && !q.closed {
		q.cond.Wait()
	}
	if q.closed || q.pending == 0 {
		return "", false
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

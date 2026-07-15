package scanner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

var (
	errFileChanged     = errors.New("file changed during scan")
	errReparsePoint    = errors.New("reparse points are not valid scan roots")
	errHardLinkedFile  = errors.New("hard-linked files are protected")
	errNamedStreamFile = errors.New("files with alternate data streams are protected")
	errMissingFileID   = errors.New("stable file identity is unavailable")
	errMissingScope    = errors.New("file is not bound to an authorized scan root")
	errScopeEscape     = errors.New("path resolves outside its authorized scan root")
	errRootChanged     = errors.New("authorized scan root changed or became unavailable")
)

type fileSnapshot struct {
	size         int64
	modifiedAt   time.Time
	identity     FileIdentity
	linkCount    uint32
	namedStreams uint32
}

func openFileSnapshot(path string) (*os.File, fileSnapshot, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fileSnapshot{}, err
	}

	snapshot, err := snapshotOpenFile(file, path)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, err
	}
	return file, snapshot, nil
}

// authorizeScanRoot resolves a selected root through an open handle and binds
// it to a stable filesystem identity. Traversal reparse points are rejected in
// every existing path component, not only at the selected leaf.
func authorizeScanRoot(path string) (AuthorizedScope, os.FileInfo, error) {
	resolved, err := resolveScanRoot(path)
	if err != nil {
		return AuthorizedScope{}, nil, err
	}

	file, err := os.Open(resolved)
	if err != nil {
		return AuthorizedScope{}, nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return AuthorizedScope{}, nil, err
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return AuthorizedScope{}, nil, fmt.Errorf("scan root %q is neither a directory nor a regular file", resolved)
	}
	finalPath, err := finalPathForOpenFile(file)
	if err != nil {
		return AuthorizedScope{}, nil, fmt.Errorf("resolve scan root handle: %w", err)
	}
	if !sameCanonicalPath(resolved, finalPath) {
		return AuthorizedScope{}, nil, fmt.Errorf("%w: selected path %q resolves to %q", errReparsePoint, path, finalPath)
	}
	identity, err := platformPathIdentity(file, finalPath)
	if err != nil {
		return AuthorizedScope{}, nil, fmt.Errorf("read scan root identity: %w", err)
	}
	if identity == (FileIdentity{}) {
		return AuthorizedScope{}, nil, errMissingFileID
	}

	return AuthorizedScope{
		RootFinalPath: finalPath,
		RootIdentity:  identity,
		RootIsFile:    info.Mode().IsRegular(),
	}, info, nil
}

func resolveScanRoot(path string) (string, error) {
	if err := validateNoTraversalReparseComponents(path); err != nil {
		return "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	finalPath, err := finalPathForOpenFile(file)
	if err != nil {
		return "", err
	}
	if !sameCanonicalPath(path, finalPath) {
		return "", fmt.Errorf("%w: selected path %q resolves to %q", errReparsePoint, path, finalPath)
	}
	return filepath.Clean(finalPath), nil
}

func scopeIsValid(scope AuthorizedScope) bool {
	return scope.RootFinalPath != "" && scope.RootIdentity != (FileIdentity{})
}

// validateAuthorizedScope proves that the selected root path still names the
// same root object. It is intentionally reusable by preview and destructive
// workflows before they consume a scan record.
func validateAuthorizedScope(scope AuthorizedScope) error {
	if !scopeIsValid(scope) {
		return errMissingScope
	}
	if err := validateNoTraversalReparseComponents(scope.RootFinalPath); err != nil {
		return fmt.Errorf("%w: %v", errRootChanged, err)
	}

	file, err := os.Open(scope.RootFinalPath)
	if err != nil {
		return fmt.Errorf("%w: %v", errRootChanged, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("%w: %v", errRootChanged, err)
	}
	if info.Mode().IsRegular() != scope.RootIsFile || (!info.IsDir() && !info.Mode().IsRegular()) {
		return fmt.Errorf("%w: root object kind changed", errRootChanged)
	}
	finalPath, err := finalPathForOpenFile(file)
	if err != nil {
		return fmt.Errorf("%w: resolve root handle: %v", errRootChanged, err)
	}
	if !sameCanonicalPath(scope.RootFinalPath, finalPath) {
		return fmt.Errorf("%w: root now resolves to %q", errRootChanged, finalPath)
	}
	identity, err := platformPathIdentity(file, finalPath)
	if err != nil {
		return fmt.Errorf("%w: read root identity: %v", errRootChanged, err)
	}
	if identity != scope.RootIdentity {
		return fmt.Errorf("%w: stable root identity no longer matches", errRootChanged)
	}
	return nil
}

func validateOpenPathScope(file *os.File, requestedPath string, scope AuthorizedScope) (string, error) {
	if !scopeIsValid(scope) {
		return "", errMissingScope
	}
	finalPath, err := finalPathForOpenFile(file)
	if err != nil {
		return "", fmt.Errorf("resolve opened path: %w", err)
	}
	if !pathWithinScope(finalPath, scope) {
		return "", fmt.Errorf("%w: %q resolved to %q", errScopeEscape, requestedPath, finalPath)
	}
	// Surface records contain final paths. A mismatch here proves that a
	// component was redirected or replaced after authorization, even when the
	// redirected object remains under the same selected root.
	if !sameCanonicalPath(requestedPath, finalPath) {
		return "", fmt.Errorf("%w: %q resolved to %q", errReparsePoint, requestedPath, finalPath)
	}
	return filepath.Clean(finalPath), nil
}

func openFileSnapshotInScope(path string, scope AuthorizedScope) (*os.File, fileSnapshot, string, error) {
	if err := validateAuthorizedScope(scope); err != nil {
		return nil, fileSnapshot{}, "", err
	}
	if err := validateNoTraversalReparseComponents(path); err != nil {
		return nil, fileSnapshot{}, "", err
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, fileSnapshot{}, "", err
	}
	finalPath, err := validateOpenPathScope(file, path, scope)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, "", err
	}
	snapshot, err := snapshotOpenFile(file, finalPath)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, "", err
	}
	if err := validateAuthorizedScope(scope); err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, "", err
	}
	return file, snapshot, finalPath, nil
}

// openSurfaceFileSnapshot validates the opened child handle against a scope
// whose root is held stable by directory-level and end-of-scan checks. This
// avoids reopening the root for every inventory row while retaining final-path
// containment and no-alias guarantees on the actual file handle.
func openSurfaceFileSnapshot(path string, scope AuthorizedScope) (*os.File, fileSnapshot, string, error) {
	if !scopeIsValid(scope) {
		return nil, fileSnapshot{}, "", errMissingScope
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fileSnapshot{}, "", err
	}
	finalPath, err := validateOpenPathScope(file, path, scope)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, "", err
	}
	snapshot, err := snapshotOpenFile(file, finalPath)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, "", err
	}
	return file, snapshot, finalPath, nil
}

func openDirectoryInScope(path string, scope AuthorizedScope) (*os.File, string, error) {
	if scope.RootIsFile {
		return nil, "", fmt.Errorf("authorized root %q is a file, not a directory", scope.RootFinalPath)
	}
	if err := validateAuthorizedScope(scope); err != nil {
		return nil, "", err
	}
	if err := validateNoTraversalReparseComponents(path); err != nil {
		return nil, "", err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, "", err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	if !info.IsDir() {
		_ = file.Close()
		return nil, "", fmt.Errorf("%q is no longer a directory", path)
	}
	finalPath, err := validateOpenPathScope(file, path, scope)
	if err != nil {
		_ = file.Close()
		return nil, "", err
	}
	if err := validateAuthorizedScope(scope); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	return file, finalPath, nil
}

// openVerificationSnapshotInScope is the handle-bound entry point used by
// destructive policy. The returned handle already matches the authorized root
// and expected record metadata; callers can hash it without reopening by path.
func openVerificationSnapshotInScope(record FileRecord, shareDelete bool) (*os.File, fileSnapshot, error) {
	file, snapshot, err := openScopedVerificationSnapshot(record, shareDelete)
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	if !recordMatchesSnapshot(record, snapshot) {
		_ = file.Close()
		return nil, fileSnapshot{}, errFileChanged
	}
	return file, snapshot, nil
}

// openScopedVerificationSnapshot binds an open handle to its authorized root
// before policy classifies post-scan metadata changes such as new hard links or
// alternate streams. Callers must compare the returned snapshot with record.
func openScopedVerificationSnapshot(record FileRecord, shareDelete bool) (*os.File, fileSnapshot, error) {
	if err := validateAuthorizedScope(record.Scope); err != nil {
		return nil, fileSnapshot{}, err
	}
	if err := validateNoTraversalReparseComponents(record.Path); err != nil {
		return nil, fileSnapshot{}, err
	}
	file, err := openVerificationFile(record.Path, shareDelete)
	if err != nil {
		return nil, fileSnapshot{}, err
	}
	finalPath, err := validateOpenPathScope(file, record.Path, record.Scope)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, err
	}
	snapshot, err := snapshotOpenFile(file, finalPath)
	if err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, err
	}
	if err := validateAuthorizedScope(record.Scope); err != nil {
		_ = file.Close()
		return nil, fileSnapshot{}, err
	}
	return file, snapshot, nil
}

// ValidateRecordScope reopens a scan record and proves that its path, stable
// identity, and metadata are still bound to the selected root. Callers that
// subsequently act on the file must use openVerificationSnapshotInScope so the
// proof and operation share one handle.
func ValidateRecordScope(record FileRecord) error {
	file, snapshot, _, err := openFileSnapshotInScope(record.Path, record.Scope)
	if err != nil {
		return err
	}
	defer file.Close()
	if !recordMatchesSnapshot(record, snapshot) {
		return errFileChanged
	}
	return verifyStableSnapshotInScope(file, record.Path, snapshot, record.Scope)
}

// OpenVerifiedRecordForRead returns a read handle whose object identity,
// metadata, and final path match the scoped scan record. On Windows the handle
// denies write and delete sharing, so preview parsing can consume stable bytes
// without a validation/reopen race. The caller must close the handle.
func OpenVerifiedRecordForRead(record FileRecord) (*os.File, error) {
	file, snapshot, err := openVerificationSnapshotInScope(record, false)
	if err != nil {
		return nil, err
	}
	if snapshot.linkCount != 1 {
		_ = file.Close()
		return nil, errHardLinkedFile
	}
	if snapshot.namedStreams != 0 {
		_ = file.Close()
		return nil, errNamedStreamFile
	}
	return file, nil
}

func snapshotOpenFile(file *os.File, path string) (fileSnapshot, error) {
	info, err := file.Stat()
	if err != nil {
		return fileSnapshot{}, err
	}
	if !info.Mode().IsRegular() {
		return fileSnapshot{}, fmt.Errorf("%s is not a regular file", path)
	}

	identity, linkCount, namedStreams, err := platformFileSnapshot(file, path)
	if err != nil {
		return fileSnapshot{}, err
	}
	if identity == (FileIdentity{}) {
		return fileSnapshot{}, errMissingFileID
	}

	return fileSnapshot{
		size:         info.Size(),
		modifiedAt:   info.ModTime(),
		identity:     identity,
		linkCount:    linkCount,
		namedStreams: namedStreams,
	}, nil
}

func refreshFileRecord(record FileRecord) (FileRecord, error) {
	var (
		file      *os.File
		snapshot  fileSnapshot
		finalPath = record.Path
		err       error
	)
	if scopeIsValid(record.Scope) {
		file, snapshot, finalPath, err = openFileSnapshotInScope(record.Path, record.Scope)
	} else {
		file, snapshot, err = openFileSnapshot(record.Path)
	}
	if err != nil {
		return FileRecord{}, err
	}
	if err := file.Close(); err != nil {
		return FileRecord{}, err
	}
	if snapshot.linkCount > 1 {
		return FileRecord{}, errHardLinkedFile
	}
	if snapshot.namedStreams > 0 {
		return FileRecord{}, errNamedStreamFile
	}

	record.Path = finalPath
	record.Size = snapshot.size
	record.ModifiedAt = snapshot.modifiedAt
	record.CreatedAt = bestEffortCreatedAt(record.Path, snapshot.modifiedAt)
	record.Identity = snapshot.identity
	record.LinkCount = snapshot.linkCount
	record.NamedStreams = snapshot.namedStreams
	return record, nil
}

func recordMatchesSnapshot(record FileRecord, snapshot fileSnapshot) bool {
	return record.Size == snapshot.size &&
		record.ModifiedAt.Equal(snapshot.modifiedAt) &&
		record.Identity == snapshot.identity &&
		record.LinkCount == snapshot.linkCount &&
		record.NamedStreams == snapshot.namedStreams
}

func snapshotsEqual(left, right fileSnapshot) bool {
	return left.size == right.size &&
		left.modifiedAt.Equal(right.modifiedAt) &&
		left.identity == right.identity &&
		left.linkCount == right.linkCount &&
		left.namedStreams == right.namedStreams
}

func verifyStableSnapshotInScope(file *os.File, path string, expected fileSnapshot, scope AuthorizedScope) error {
	current, err := snapshotOpenFile(file, path)
	if err != nil {
		return err
	}
	if !snapshotsEqual(expected, current) {
		return errFileChanged
	}
	if _, err := validateOpenPathScope(file, path, scope); err != nil {
		return err
	}

	pathFile, pathSnapshot, _, err := openFileSnapshotInScope(path, scope)
	if err != nil {
		if errors.Is(err, errRootChanged) || errors.Is(err, errMissingScope) {
			return err
		}
		return fmt.Errorf("%w: %v", errFileChanged, err)
	}
	if err := pathFile.Close(); err != nil {
		return err
	}
	if !snapshotsEqual(expected, pathSnapshot) {
		return errFileChanged
	}
	return validateAuthorizedScope(scope)
}

func sameCanonicalPath(left, right string) bool {
	left = filepath.Clean(left)
	right = filepath.Clean(right)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func pathWithinScope(path string, scope AuthorizedScope) bool {
	if scope.RootIsFile {
		return sameCanonicalPath(path, scope.RootFinalPath)
	}
	relative, err := filepath.Rel(scope.RootFinalPath, path)
	if err != nil {
		return false
	}
	if relative == "." {
		return true
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

//go:build windows

package scanner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// recycleLocation classifies where a retained, verified file handle resolves
// after a path-based operation ran against its original path. It is the
// read-only building block of the detect-and-undo recycle design proposed in
// ADR 0008. Nothing in this file performs a destructive action; production
// cleanup stays disabled under ADR 0005 until an approved adapter and the
// full evidence matrix exist.
type recycleLocation int

const (
	// recycleLocationUnknown is the zero value; no classification was made.
	recycleLocationUnknown recycleLocation = iota
	// recycleLocationReachedRecycleBin means the verified object now lives
	// under a volume-root $Recycle.Bin, i.e. it was recycled.
	recycleLocationReachedRecycleBin
	// recycleLocationStillAtOrigin means the verified object is still at the
	// path it was verified at, so a path-based operation did not act on it.
	recycleLocationStillAtOrigin
	// recycleLocationElsewhere means the verified object moved somewhere that
	// is neither its origin nor a Recycle Bin, an outcome the caller must
	// treat as unproven rather than success.
	recycleLocationElsewhere
)

// recycleOutcome reports the resolved final path of a retained handle and how
// it relates to the original verified path.
type recycleOutcome struct {
	Location  recycleLocation
	FinalPath string
}

var errRecycleOutcomeUnavailable = errors.New("recycle outcome could not be resolved from the retained handle")

// isRecycleBinComponentPath reports whether any component of the cleaned path
// is a $Recycle.Bin directory. Windows stores recycled files under
// <volume>\$Recycle.Bin\<user SID>\$R..., so a retained handle that resolves
// into that tree proves the exact object reached the bin.
func isRecycleBinComponentPath(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	for _, segment := range strings.Split(clean, string(filepath.Separator)) {
		if strings.EqualFold(segment, "$Recycle.Bin") || strings.EqualFold(segment, "$RECYCLER") {
			return true
		}
	}
	return false
}

// verifyRecycleOutcomeByHandle resolves the current location of the exact file
// object referenced by a retained handle and classifies it relative to the
// path it was verified at. The handle must have been opened before the
// operation with delete sharing so it survives a move to the Recycle Bin.
//
// This function is read-only: it neither recycles, deletes, nor restores. It
// exists so a future, approval-gated adapter can decide between issuing an
// identity-bound success receipt (ReachedRecycleBin) and performing an undo
// (StillAtOrigin or Elsewhere), exactly as ADR 0002 requires.
func verifyRecycleOutcomeByHandle(file *os.File, verifiedPath string) (recycleOutcome, error) {
	if file == nil {
		return recycleOutcome{}, errRecycleOutcomeUnavailable
	}
	finalPath, err := finalPathForOpenFile(file)
	if err != nil {
		return recycleOutcome{}, err
	}
	outcome := recycleOutcome{FinalPath: finalPath}
	switch {
	case isRecycleBinComponentPath(finalPath):
		outcome.Location = recycleLocationReachedRecycleBin
	case verifiedPath != "" && sameCanonicalPath(finalPath, verifiedPath):
		outcome.Location = recycleLocationStillAtOrigin
	default:
		outcome.Location = recycleLocationElsewhere
	}
	return outcome, nil
}

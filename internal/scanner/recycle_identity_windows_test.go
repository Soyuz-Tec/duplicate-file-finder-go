//go:build windows

package scanner

import (
	"os"
	"path/filepath"
	"testing"
)

// The tests below are the ADR 0008 spike. They prove, on disposable fixtures
// using real filesystem moves (recycling is a move within the volume), the two
// operating-system guarantees the detect-and-undo recycle design depends on.
// They perform no recycling and no permanent deletion; production cleanup
// remains disabled under ADR 0005.

// TestRetainedHandleTracksMovedObject proves that a handle opened before a move
// stays valid across it, that GetFinalPathNameByHandle reports the new
// location, and that the identity read from the retained handle equals the
// identity read from a fresh open of the moved object. This is the primitive
// that lets a future adapter find where a verified object went after a
// path-based recycle.
func TestRetainedHandleTracksMovedObject(t *testing.T) {
	root := userFileTestRoot(t)
	origin := filepath.Join(root, "verified.bin")
	writeTestFile(t, origin, []byte("verified duplicate payload"))

	handle, err := openVerificationFile(origin, true)
	if err != nil {
		t.Fatalf("openVerificationFile failed: %v", err)
	}
	defer handle.Close()

	beforeIdentity, err := platformPathIdentity(handle, "")
	if err != nil {
		t.Fatalf("identity before move failed: %v", err)
	}

	// A move within the volume stands in for the Recycle Bin move: same volume,
	// stable FileID, retained handle.
	moved := filepath.Join(root, "moved-away.bin")
	if err := os.Rename(origin, moved); err != nil {
		t.Fatalf("Rename failed: %v", err)
	}

	outcome, err := verifyRecycleOutcomeByHandle(handle, origin)
	if err != nil {
		t.Fatalf("verifyRecycleOutcomeByHandle failed: %v", err)
	}
	if !sameCanonicalPath(outcome.FinalPath, moved) {
		t.Fatalf("retained handle final path = %q, want %q", outcome.FinalPath, moved)
	}
	if outcome.Location != recycleLocationElsewhere {
		t.Fatalf("moved-away object classified as %v, want Elsewhere", outcome.Location)
	}

	afterIdentity, err := platformPathIdentity(handle, "")
	if err != nil {
		t.Fatalf("identity after move failed: %v", err)
	}
	if afterIdentity != beforeIdentity {
		t.Fatalf("retained-handle identity changed across move: before=%+v after=%+v", beforeIdentity, afterIdentity)
	}

	reopened, err := openVerificationFile(moved, false)
	if err != nil {
		t.Fatalf("reopen moved object failed: %v", err)
	}
	defer reopened.Close()
	reopenedIdentity, err := platformPathIdentity(reopened, "")
	if err != nil {
		t.Fatalf("identity of reopened object failed: %v", err)
	}
	if reopenedIdentity != beforeIdentity {
		t.Fatalf("fresh open of moved object has different identity: original=%+v reopened=%+v", beforeIdentity, reopenedIdentity)
	}
}

// TestRetainedHandleDetectsPathSwap reproduces the exact ADR 0005 attack: the
// verified object is renamed away and a replacement is created at the original
// path. It proves the retained handle keeps identifying the original object and
// reports its true location, distinct from the replacement now occupying the
// path. A path-based operation targeting the original path would therefore be
// detectably acting on the replacement, not the verified object.
func TestRetainedHandleDetectsPathSwap(t *testing.T) {
	root := userFileTestRoot(t)
	origin := filepath.Join(root, "target.bin")
	writeTestFile(t, origin, []byte("original verified bytes"))

	handle, err := openVerificationFile(origin, true)
	if err != nil {
		t.Fatalf("openVerificationFile failed: %v", err)
	}
	defer handle.Close()

	originalIdentity, err := platformPathIdentity(handle, "")
	if err != nil {
		t.Fatalf("original identity failed: %v", err)
	}

	// Swap: move the verified object aside, drop an impostor at the path.
	movedAside := filepath.Join(root, "target-moved-aside.bin")
	if err := os.Rename(origin, movedAside); err != nil {
		t.Fatalf("Rename original aside failed: %v", err)
	}
	writeTestFile(t, origin, []byte("impostor replacement bytes"))

	// The retained handle still identifies the original object at its new path.
	outcome, err := verifyRecycleOutcomeByHandle(handle, origin)
	if err != nil {
		t.Fatalf("verifyRecycleOutcomeByHandle failed: %v", err)
	}
	if !sameCanonicalPath(outcome.FinalPath, movedAside) {
		t.Fatalf("retained handle final path = %q, want %q", outcome.FinalPath, movedAside)
	}
	if outcome.Location == recycleLocationStillAtOrigin {
		t.Fatal("retained handle wrongly reported the verified object still at its origin after a swap")
	}

	// The impostor at the path is a distinct object.
	impostor, err := openVerificationFile(origin, false)
	if err != nil {
		t.Fatalf("open impostor failed: %v", err)
	}
	defer impostor.Close()
	impostorIdentity, err := platformPathIdentity(impostor, "")
	if err != nil {
		t.Fatalf("impostor identity failed: %v", err)
	}
	if impostorIdentity == originalIdentity {
		t.Fatal("impostor at the path shares the verified object's identity; swap would be undetectable")
	}
}

// TestVerifyRecycleOutcomeClassifiesOrigin proves that when nothing moves, a
// retained handle resolves to its origin and is classified StillAtOrigin: the
// signal a future adapter reads as "the recycle did not act on this object."
func TestVerifyRecycleOutcomeClassifiesOrigin(t *testing.T) {
	root := userFileTestRoot(t)
	origin := filepath.Join(root, "unmoved.bin")
	writeTestFile(t, origin, []byte("still here"))

	handle, err := openVerificationFile(origin, true)
	if err != nil {
		t.Fatalf("openVerificationFile failed: %v", err)
	}
	defer handle.Close()

	outcome, err := verifyRecycleOutcomeByHandle(handle, origin)
	if err != nil {
		t.Fatalf("verifyRecycleOutcomeByHandle failed: %v", err)
	}
	if outcome.Location != recycleLocationStillAtOrigin {
		t.Fatalf("unmoved object classified as %v, want StillAtOrigin (final path %q)", outcome.Location, outcome.FinalPath)
	}
}

func TestIsRecycleBinComponentPath(t *testing.T) {
	cases := map[string]bool{
		`C:\$Recycle.Bin\S-1-5-21-1\$RA1B2C3.bin`: true,
		`D:\$RECYCLER\stuff`:                       true,
		`C:\Users\person\Documents\report.pdf`:     false,
		`C:\Recycle.Bin\not-a-real-one\file`:       false,
		"":                                         false,
	}
	for path, want := range cases {
		if got := isRecycleBinComponentPath(path); got != want {
			t.Fatalf("isRecycleBinComponentPath(%q) = %v, want %v", path, got, want)
		}
	}
}

func TestVerifyRecycleOutcomeRejectsNilHandle(t *testing.T) {
	outcome, err := verifyRecycleOutcomeByHandle(nil, `C:\x`)
	if err == nil {
		t.Fatal("nil handle was accepted")
	}
	// A failed verification must not imply any placement: the zero outcome
	// classifies as Unknown so a caller cannot misread it as a success signal.
	if outcome.Location != recycleLocationUnknown {
		t.Fatalf("failed verification returned location %v, want Unknown", outcome.Location)
	}
}

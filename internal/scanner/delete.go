package scanner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var (
	errRecycleAborted     = errors.New("the Windows recycle operation was aborted")
	errRecycleUnsupported = errors.New("recycle operation is unsupported on this platform")
	errSourceStillExists  = errors.New("source path still exists after recycle operation")
)

type recycleAdapter interface {
	Recycle(context.Context, string, *os.File, FileIdentity) (recycleReceipt, error)
}

type recycleReceipt struct {
	identity    FileIdentity
	destination string
	confirmed   bool
}

type validatedRecycleRequest struct {
	groupHash string
	selected  []FileRecord
	keepers   []FileRecord
}

// RecycleExactDuplicates applies the production recycle policy to one exact
// duplicate group. Paths alone are never accepted as deletion authority.
func RecycleExactDuplicates(ctx context.Context, request RecycleRequest) RecycleResult {
	return recycleExactDuplicates(ctx, request, newPlatformRecycleAdapter())
}

// RecycleSupported reports whether the production adapter can bind a verified
// file identity through the platform's destructive Recycle Bin operation.
func RecycleSupported() bool {
	return platformRecycleSupported()
}

func recycleExactDuplicates(ctx context.Context, request RecycleRequest, adapter recycleAdapter) RecycleResult {
	validated, err := validateRecycleRequest(request)
	if err != nil {
		return rejectedRecycleResult(request, err)
	}
	if adapter == nil {
		return rejectedRecycleResult(request, errors.New("recycle adapter is unavailable"))
	}

	result := RecycleResult{Items: make([]RecycleItemResult, 0, len(validated.selected))}
	for _, target := range validated.selected {
		if err := ctx.Err(); err != nil {
			result.Items = append(result.Items, recycleItem(target.Path, RecycleStatusCancelled, err))
			continue
		}
		result.Items = append(result.Items, recycleOne(ctx, adapter, validated, target))
	}
	return result
}

func validateRecycleRequest(request RecycleRequest) (validatedRecycleRequest, error) {
	group := request.Group
	if len(group.Files) < 2 {
		return validatedRecycleRequest{}, errors.New("duplicate group must contain at least two files")
	}
	if len(request.Selected) == 0 {
		return validatedRecycleRequest{}, errors.New("no duplicate files were selected")
	}
	hashBytes, err := hex.DecodeString(group.Hash)
	if err != nil || len(hashBytes) != sha256.Size {
		return validatedRecycleRequest{}, errors.New("duplicate group has an invalid SHA-256 hash")
	}

	membersByPath := make(map[string]FileRecord, len(group.Files))
	memberIdentities := make(map[FileIdentity]struct{}, len(group.Files))
	for _, member := range group.Files {
		if member.Path == "" || member.Identity == (FileIdentity{}) {
			return validatedRecycleRequest{}, errors.New("duplicate group contains a file without stable identity")
		}
		if member.Size != group.Size {
			return validatedRecycleRequest{}, errors.New("duplicate group contains inconsistent file size")
		}
		if member.LinkCount != 1 || member.NamedStreams != 0 {
			return validatedRecycleRequest{}, errors.New("duplicate group contains a protected file")
		}

		pathKey := canonicalRecordPath(member.Path)
		if _, exists := membersByPath[pathKey]; exists {
			return validatedRecycleRequest{}, errors.New("duplicate group repeats a source path")
		}
		membersByPath[pathKey] = member
		if _, exists := memberIdentities[member.Identity]; exists {
			return validatedRecycleRequest{}, errors.New("duplicate group repeats a physical file identity")
		}
		memberIdentities[member.Identity] = struct{}{}
	}

	selectedIdentities := make(map[FileIdentity]struct{}, len(request.Selected))
	selectedPaths := make(map[string]struct{}, len(request.Selected))
	selected := make([]FileRecord, 0, len(request.Selected))
	for _, candidate := range request.Selected {
		pathKey := canonicalRecordPath(candidate.Path)
		member, exists := membersByPath[pathKey]
		if !exists || !sameScannedRecord(member, candidate) {
			return validatedRecycleRequest{}, fmt.Errorf("selected file %q is not an exact member of the scanned group", candidate.Path)
		}
		if _, exists := selectedPaths[pathKey]; exists {
			return validatedRecycleRequest{}, fmt.Errorf("selected file %q appears more than once", candidate.Path)
		}
		selectedPaths[pathKey] = struct{}{}
		if _, exists := selectedIdentities[candidate.Identity]; exists {
			return validatedRecycleRequest{}, fmt.Errorf("physical file identity for %q appears more than once", candidate.Path)
		}
		selectedIdentities[candidate.Identity] = struct{}{}
		selected = append(selected, member)
	}
	if len(selectedIdentities) >= len(memberIdentities) {
		return validatedRecycleRequest{}, errors.New("at least one distinct physical duplicate must be retained")
	}

	keepers := make([]FileRecord, 0, len(group.Files)-len(selected))
	for _, member := range group.Files {
		if _, selected := selectedIdentities[member.Identity]; !selected {
			keepers = append(keepers, member)
		}
	}
	if len(keepers) == 0 {
		return validatedRecycleRequest{}, errors.New("no distinct keeper remains")
	}

	return validatedRecycleRequest{
		groupHash: strings.ToLower(group.Hash),
		selected:  selected,
		keepers:   keepers,
	}, nil
}

func recycleOne(ctx context.Context, adapter recycleAdapter, request validatedRecycleRequest, target FileRecord) RecycleItemResult {
	keeper, keeperStatus, err := openVerifiedKeeper(ctx, request.keepers, request.groupHash)
	if err != nil {
		return recycleItem(target.Path, keeperStatus, fmt.Errorf("no verified keeper remains: %w", err))
	}
	defer keeper.Close()

	targetHandle, err := openVerifiedRecord(ctx, target, request.groupHash, true)
	if err != nil {
		return recycleItem(target.Path, verificationStatus(err), err)
	}
	defer targetHandle.Close()

	if err := ctx.Err(); err != nil {
		return recycleItem(target.Path, RecycleStatusCancelled, err)
	}
	receipt, err := adapter.Recycle(ctx, target.Path, targetHandle, target.Identity)
	if err != nil {
		status := RecycleStatusFailed
		if errors.Is(err, context.Canceled) || errors.Is(err, errRecycleAborted) {
			status = RecycleStatusCancelled
		}
		return recycleItem(target.Path, status, err)
	}
	if !receipt.confirmed || receipt.identity != target.Identity || receipt.destination == "" {
		return recycleItem(target.Path, RecycleStatusFailed, errors.New("native recycle receipt did not confirm the expected file identity"))
	}
	if err := ctx.Err(); err != nil {
		return recycleItem(target.Path, RecycleStatusCancelled, err)
	}
	if err := verifySourcePathAbsent(target); err != nil {
		return recycleItem(target.Path, RecycleStatusFailed, err)
	}
	return RecycleItemResult{Path: target.Path, Status: RecycleStatusRecycled}
}

func openVerifiedKeeper(ctx context.Context, candidates []FileRecord, expectedHash string) (*os.File, RecycleStatus, error) {
	var (
		lastError    error
		sawChanged   bool
		sawProtected bool
	)
	for _, candidate := range candidates {
		file, err := openVerifiedRecord(ctx, candidate, expectedHash, false)
		if err == nil {
			return file, "", nil
		}
		lastError = err
		switch verificationStatus(err) {
		case RecycleStatusCancelled:
			return nil, RecycleStatusCancelled, err
		case RecycleStatusSkippedChanged:
			sawChanged = true
		case RecycleStatusSkippedProtected:
			sawProtected = true
		}
	}
	if lastError == nil {
		lastError = errors.New("keeper set is empty")
	}
	if sawChanged {
		return nil, RecycleStatusSkippedChanged, lastError
	}
	if sawProtected {
		return nil, RecycleStatusSkippedProtected, lastError
	}
	return nil, RecycleStatusFailed, lastError
}

func openVerifiedRecord(ctx context.Context, record FileRecord, expectedHash string, shareDelete bool) (*os.File, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, snapshot, err := openScopedVerificationSnapshot(record, shareDelete)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: source path disappeared", errFileChanged)
		}
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
	if !recordMatchesSnapshot(record, snapshot) {
		_ = file.Close()
		return nil, errFileChanged
	}
	hasher := sha256.New()
	buffer := make([]byte, fullHashBufferSize)
	if _, err := copyWithContext(ctx, hasher, file, buffer); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := verifyStableSnapshotInScope(file, record.Path, snapshot, record.Scope); err != nil {
		_ = file.Close()
		return nil, err
	}
	if !strings.EqualFold(hex.EncodeToString(hasher.Sum(nil)), expectedHash) {
		_ = file.Close()
		return nil, fmt.Errorf("%w: SHA-256 no longer matches the duplicate group", errFileChanged)
	}
	return file, nil
}

func verifySourcePathAbsent(expected FileRecord) error {
	file, snapshot, err := openFileSnapshot(expected.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("verify recycle outcome: %w", err)
	}
	defer file.Close()
	if snapshot.identity == expected.Identity {
		return fmt.Errorf("%w: expected object remains", errSourceStillExists)
	}
	return fmt.Errorf("%w: a different object occupies the source path", errSourceStillExists)
}

func verificationStatus(err error) RecycleStatus {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return RecycleStatusCancelled
	case errors.Is(err, errHardLinkedFile), errors.Is(err, errNamedStreamFile), errors.Is(err, errReparsePoint):
		return RecycleStatusSkippedProtected
	case errors.Is(err, errFileChanged), errors.Is(err, os.ErrNotExist):
		return RecycleStatusSkippedChanged
	default:
		return RecycleStatusFailed
	}
}

func rejectedRecycleResult(request RecycleRequest, err error) RecycleResult {
	result := RecycleResult{
		Items:        make([]RecycleItemResult, 0, len(request.Selected)),
		RequestError: err.Error(),
	}
	for _, selected := range request.Selected {
		result.Items = append(result.Items, recycleItem(selected.Path, RecycleStatusFailed, err))
	}
	return result
}

func recycleItem(path string, status RecycleStatus, err error) RecycleItemResult {
	reason := ""
	if err != nil {
		reason = err.Error()
	}
	return RecycleItemResult{Path: path, Status: status, Reason: reason}
}

func sameScannedRecord(left, right FileRecord) bool {
	return strings.EqualFold(filepath.Clean(left.Path), filepath.Clean(right.Path)) &&
		left.Size == right.Size &&
		left.ModifiedAt.Equal(right.ModifiedAt) &&
		left.Identity == right.Identity &&
		left.LinkCount == right.LinkCount &&
		left.NamedStreams == right.NamedStreams &&
		left.Scope == right.Scope
}

func canonicalRecordPath(path string) string {
	return strings.ToLower(filepath.Clean(path))
}

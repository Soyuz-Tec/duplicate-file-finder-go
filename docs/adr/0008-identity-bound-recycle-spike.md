# ADR 0008: Identity-bound detect-and-undo recycle (spike)

- Status: Proposed — requires product-owner approval and an independent security review before any implementation is accepted
- Date: 2026-07-17

## Context

[ADR 0002](0002-verified-recycle-safety.md) requires that a destructive recycle stay bound to the exact `VolumeSerial + FileID` TwinTidy revalidated, and that success be issued only when that exact object reaches the volume-root `$Recycle.Bin`. [ADR 0005](0005-disable-path-based-recycle.md) disabled the production adapter because the documented Shell mechanism (`SHCreateItemFromParsingName` + `IFileOperation::DeleteItem`) consumes a *path-derived* Shell item: a concurrent rename-and-replace can make the Shell recycle a different object at the same path, and inspecting the retained handle afterward detected the mismatch but could not prevent it.

ADR 0005 deliberately left one door open:

> Cleanup may be enabled only when a reversible implementation can prove that the verified `VolumeSerial + FileId` remains authoritative through the destructive operation. […] Any future replacement must add a barrier-controlled native swap test in which a replacement file's identity and bytes always survive, plus native Recycle Bin receipt, cancellation, failure, and rollback evidence.

This ADR proposes a design that fits through that door — **detect-and-undo** — and records a spike that proves the two operating-system guarantees the design depends on. It does **not** enable cleanup and does **not** weaken any invariant in [AGENTS.md](../../AGENTS.md); the production adapter remains disabled and `RecycleSupported()` remains `false` until this ADR is accepted with the required evidence.

## Key insight

ADR 0005 rejected "recycle then verify afterward" because *detection after deletion does not prevent harm*. That reasoning holds only while the operation is irreversible. Recycling is **reversible**: the object moves to `$Recycle.Bin` and can be restored. If the post-operation check is paired with a guaranteed restore, a mis-recycle becomes a detected-and-undone event rather than data loss.

The verification is anchored to a **retained kernel handle**, not to a path:

- Recycling on NTFS is a move within the volume, so the object keeps its `FileID`. A handle opened before the operation, with `FILE_SHARE_DELETE`, stays valid across the move.
- `GetFinalPathNameByHandle` on that retained handle reports *where that exact object now lives*, independent of whatever occupies the original path afterward.

That converts the ADR 0005 failure mode into a decision:

1. Open the verified target with `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE`; record `FileID`/`VolumeSerial` from the handle. Keep it open.
2. Recycle the path via the native Shell operation.
3. Resolve the retained handle's final path.
   - Final path is under `<volume>\$Recycle.Bin\` → the verified object reached the bin. Issue the receipt (**success**).
   - Final path is still the original location, or any non-recycle location → the Shell recycled a *different* object; the verified object was not recycled. Restore the wrongly recycled item from the bin (**undo**) and report **failure**, keeping the row.

## Decision (proposed)

Add an identity-bound recycle adapter whose success path is gated on retained-handle verification and whose failure path is a guaranteed restore, structured exactly as ADR 0002 requires. Concretely:

- A read-only verification primitive, `verifyRecycleOutcomeByHandle`, that classifies a retained handle's post-operation location as `ReachedRecycleBin`, `StillAtOrigin`, or `Elsewhere`, plus the resolved final path. This primitive performs **no destructive action** and is the building block ADR 0002 and 0005 asked for. It ships now (see Spike) because it is pure verification.
- A native adapter (future, approval-gated) that performs the recycle, then calls the primitive, then either issues the identity-bound receipt or performs the undo. Cancellation, ambiguous native results, access failures, and any unverifiable outcome are failures with no permanent-delete fallback, per ADR 0002.

Production cleanup stays disabled until this adapter exists with the full evidence matrix below and passes an independent security review.

## Spike delivered with this ADR

`internal/scanner/recycle_identity_windows.go` adds the read-only primitive and a Recycle Bin path classifier. `internal/scanner/recycle_identity_windows_test.go` proves, on disposable temporary fixtures using real filesystem moves (recycling is a move within the volume), the two load-bearing guarantees:

1. **Retained-handle tracking and FileID stability.** A handle opened before a move stays valid; `GetFinalPathNameByHandle` reports the new location; and the `FileID`/`VolumeSerial` read from the retained handle equals the identity read from a fresh open of the moved object. This is the primitive that lets step 3 find where the verified object went.
2. **Path-swap detection.** When the original object is renamed away and a replacement is created at the original path, the retained handle continues to identify the original object and reports its true current location, distinct from the replacement now at the path. This is the exact ADR 0005 attack, now *detectable* through the retained handle.

The spike deliberately uses filesystem moves rather than a live `IFileOperation` call: it proves the OS guarantees the design rests on without introducing destructive native code before approval. The live Shell integration, and the barrier-controlled test where a concurrently swapped replacement's identity and bytes always survive, are part of the approval-gated implementation, not this spike.

## Consequences

### Positive

- A path swap can no longer cause silent data loss: it is either detected as "verified object reached the bin" or detected-and-undone.
- The success receipt is anchored to the kernel object, matching ADR 0002's contract.
- Duplicate discovery, review, and export remain available unchanged while the boundary is designed.

### Negative

- The undo path depends on locating and restoring the wrongly recycled item; its own failure must itself be reported as a failure (never as success), which the implementation and tests must cover.
- `IFileOperation` COM integration and native fault injection remain to be written and independently reviewed.
- Network, cloud-backed, and non-NTFS targets where `FileID` stability or `$Recycle.Bin` semantics do not hold must be excluded and reported as unsupported rather than guessed.

## Alternatives considered

- **Keep cleanup disabled indefinitely (status quo, ADR 0005):** retained as the fallback if this design fails review; it is safe but leaves the product unable to reclaim space.
- **Path-based recycle with post-hoc detection only:** rejected by ADR 0005 because detection without undo does not prevent harm. This ADR adds the undo.
- **Permanent handle-based delete:** rejected because it violates reversible user intent and has no Recycle Bin path.
- **Copy-keeper-then-delete staging:** rejected as more destructive surface area for no identity-binding benefit.

## Validation required before acceptance

Implementation may be accepted only with all of:

- barrier-controlled native swap test in which a concurrently created replacement's identity and bytes always survive the operation;
- native Recycle Bin receipt proving the verified `VolumeSerial + FileID` reached `$Recycle.Bin`;
- undo test proving a wrongly recycled item is restored, and a failed undo is reported as failure;
- cancellation, ambiguous-result, locked-file, and access-denied fixtures;
- keeper-preservation and operation-generation isolation from ADR 0002;
- confirmation that non-NTFS and cloud-backed targets are refused as unsupported;
- an independent destructive-workflow security review and explicit product-owner approval.

Until then this ADR is **Proposed** and the production adapter stays disabled.

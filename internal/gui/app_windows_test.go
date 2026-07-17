package gui

import (
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/Soyuz-Tec/twintidy/internal/scanner"
)

func TestDuplicateTableModelSetCheckedNotifiesSelectionChanged(t *testing.T) {
	notifications := 0
	model := &duplicateTableModel{
		onSelectionChanged: func() {
			notifications++
		},
		rows: []duplicateRow{
			{Duplicate: true, File: scanner.FileRecord{Path: `C:\Users\vasan\Documents\a.pdf`}},
			{Duplicate: true, File: scanner.FileRecord{Path: `C:\Users\vasan\Documents\b.pdf`}},
		},
	}

	if err := model.SetChecked(1, true); err != nil {
		t.Fatalf("SetChecked returned error: %v", err)
	}
	if !model.Checked(1) {
		t.Fatal("expected duplicate row to be checked")
	}
	if notifications != 1 {
		t.Fatalf("expected 1 selection notification, got %d", notifications)
	}

	if err := model.SetChecked(1, true); err != nil {
		t.Fatalf("SetChecked returned error: %v", err)
	}
	if notifications != 1 {
		t.Fatalf("expected unchanged checkbox state not to notify again, got %d", notifications)
	}

	got := model.selectedPaths()
	want := []string{`C:\Users\vasan\Documents\b.pdf`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selectedPaths() = %v, want %v", got, want)
	}
}

func TestDuplicateTableModelSetCheckedRejectsNonDuplicateRows(t *testing.T) {
	notifications := 0
	model := &duplicateTableModel{
		onSelectionChanged: func() {
			notifications++
		},
		rows: []duplicateRow{
			{
				Duplicate: false,
				Selected:  true,
				Status:    "No duplicate",
				File:      scanner.FileRecord{Path: `C:\Users\vasan\Documents\single.pdf`},
			},
		},
	}

	if err := model.SetChecked(0, true); err != nil {
		t.Fatalf("SetChecked returned error: %v", err)
	}
	if model.Checked(0) {
		t.Fatal("expected non-duplicate row to remain unchecked")
	}
	if model.selectedCount() != 0 {
		t.Fatalf("selectedCount() = %d, want 0", model.selectedCount())
	}
	if notifications != 1 {
		t.Fatalf("expected deselecting invalid row to notify once, got %d", notifications)
	}
}

func TestControlsForOperationPhase(t *testing.T) {
	tests := []struct {
		phase appPhase
		want  phaseControls
	}{
		{
			phase: phaseNoFolder,
			want:  phaseControls{selectFolder: true, reset: true, scanText: "Surface Scan"},
		},
		{
			phase: phaseFolderReady,
			want:  phaseControls{selectFolder: true, scan: true, reset: true, scanText: "Surface Scan"},
		},
		{
			phase: phaseSurfaceScanning,
			want:  phaseControls{cancel: true, scanText: "Surface Scan"},
		},
		{
			phase: phaseSurfaceCancelling,
			want:  phaseControls{scanText: "Surface Scan"},
		},
		{
			phase: phaseSurfaceReady,
			want: phaseControls{
				selectFolder: true,
				scan:         true,
				clear:        true,
				reset:        true,
				fileFocus:    true,
				table:        true,
				scanText:     "Find Duplicates",
			},
		},
		{
			phase: phaseDuplicateScanning,
			want:  phaseControls{cancel: true, scanText: "Find Duplicates"},
		},
		{
			phase: phaseDuplicateCancelling,
			want:  phaseControls{scanText: "Find Duplicates"},
		},
		{
			phase: phaseResultsReady,
			want: phaseControls{
				selectFolder:   true,
				clear:          true,
				reset:          true,
				fileFocus:      true,
				table:          true,
				resultActions:  true,
				deleteSelected: true,
				scanText:       "Find Duplicates",
			},
		},
		{
			phase: phaseExporting,
			want:  phaseControls{cancel: true, scanText: "Find Duplicates"},
		},
		{
			phase: phaseExportCancelling,
			want:  phaseControls{scanText: "Find Duplicates"},
		},
		{
			phase: phaseDeleting,
			want:  phaseControls{scanText: "Find Duplicates"},
		},
		{
			phase: phaseClosingAfterExport,
			want:  phaseControls{scanText: "Find Duplicates"},
		},
		{
			phase: phaseClosingAfterDelete,
			want:  phaseControls{scanText: "Find Duplicates"},
		},
		{
			phase: phaseClosing,
			want:  phaseControls{scanText: "Surface Scan"},
		},
	}

	for _, test := range tests {
		t.Run(test.phase.String(), func(t *testing.T) {
			state := newOperationState()
			state.phase = test.phase
			got := controlsForOperation(&state, true, true, 1)
			if got != test.want {
				t.Fatalf("controlsForOperation() = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestControlsRequireRowsAndCheckedDuplicatesForDestructiveActions(t *testing.T) {
	state := newOperationState()
	state.phase = phaseResultsReady

	withoutRows := controlsForOperation(&state, false, false, 0)
	if withoutRows.clear || withoutRows.resultActions || withoutRows.deleteSelected {
		t.Fatalf("empty results enabled row actions: %+v", withoutRows)
	}

	withoutChecks := controlsForOperation(&state, true, true, 0)
	if !withoutChecks.resultActions || withoutChecks.deleteSelected {
		t.Fatalf("unchecked results have invalid controls: %+v", withoutChecks)
	}
}

func TestCheckedDeletePathsNeverFallsBackToRowHighlight(t *testing.T) {
	app := &windowsApp{
		operation: operationState{phase: phaseResultsReady},
		model: &duplicateTableModel{rows: []duplicateRow{
			{Duplicate: true, File: scanner.FileRecord{Path: `C:\Data\highlighted-only.pdf`}},
			{Duplicate: true, Selected: true, File: scanner.FileRecord{Path: `C:\Data\checked.pdf`}},
		}},
	}

	got := app.checkedDeletePaths()
	want := []string{`C:\Data\checked.pdf`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("checkedDeletePaths() = %v, want %v", got, want)
	}

	app.model.rows[1].Selected = false
	if got := app.checkedDeletePaths(); len(got) != 0 {
		t.Fatalf("unchecked highlighted rows became delete targets: %v", got)
	}
}

func TestSurfaceScanFinishedRejectsOldFolderTokenBeforeTouchingUI(t *testing.T) {
	state := stateWithFolder(t)
	token, err := state.beginSurfaceScan(operationTestStart, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !state.requestScanCancellation() || !state.completeSurfaceScan(token, false) {
		t.Fatal("failed to retire old surface operation")
	}
	if err := state.selectFolder(`D:\NewFolder`); err != nil {
		t.Fatal(err)
	}

	app := &windowsApp{operation: state, model: &duplicateTableModel{}}
	app.surfaceScanFinished(token, scanner.SurfaceReport{TotalFiles: 99}, nil)
	if app.surfaceReport.TotalFiles != 0 {
		t.Fatalf("stale surface callback published %d files", app.surfaceReport.TotalFiles)
	}
	if app.operation.folder != `D:\NewFolder` || app.operation.phase != phaseFolderReady {
		t.Fatalf("stale surface callback mutated current state: %+v", app.operation)
	}
}

func TestDuplicateScanFinishedRejectsOldFolderTokenBeforeTouchingUI(t *testing.T) {
	state := stateWithSurface(t)
	token, err := state.beginDuplicateScan(operationTestStart, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !state.requestScanCancellation() || !state.completeDuplicateScan(token, false) {
		t.Fatal("failed to retire old duplicate operation")
	}
	if err := state.selectFolder(`D:\NewFolder`); err != nil {
		t.Fatal(err)
	}

	app := &windowsApp{operation: state, model: &duplicateTableModel{}}
	groups := []scanner.DuplicateGroup{{Hash: "stale", Files: []scanner.FileRecord{{Path: `C:\Old\a.pdf`}, {Path: `C:\Old\b.pdf`}}}}
	app.scanFinished(token, groups, nil)
	if len(app.model.rows) != 0 {
		t.Fatalf("stale duplicate callback published rows: %+v", app.model.rows)
	}
	if app.operation.folder != `D:\NewFolder` || app.operation.phase != phaseFolderReady {
		t.Fatalf("stale duplicate callback mutated current state: %+v", app.operation)
	}
}

func TestResetAllRejectsBusyScanBeforeTouchingUI(t *testing.T) {
	state := stateWithFolder(t)
	cancelCalls := 0
	token, err := state.beginSurfaceScan(operationTestStart, func() { cancelCalls++ })
	if err != nil {
		t.Fatal(err)
	}
	app := &windowsApp{operation: state, model: &duplicateTableModel{}}

	app.resetAll()
	if app.operation.phase != phaseSurfaceScanning || !app.operation.accepts(token) {
		t.Fatalf("busy reset mutated active scan: %+v", app.operation)
	}
	if cancelCalls != 0 {
		t.Fatalf("busy reset invoked cancellation %d time(s)", cancelCalls)
	}
}

func TestBuildRecyclePlanGroupsCheckedRowsWithExactScanRecords(t *testing.T) {
	rows := []duplicateRow{
		{Group: 1, Key: "group-one", Hash: "hash-one", Duplicate: true, Selected: true, File: recycleTestRecord(`C:\Data\a.txt`, 10, 1)},
		{Group: 1, Key: "group-one", Hash: "hash-one", Duplicate: true, File: recycleTestRecord(`C:\Data\b.txt`, 10, 2)},
		{Group: 1, Key: "group-one", Hash: "hash-one", Duplicate: true, File: recycleTestRecord(`C:\Data\c.txt`, 10, 3)},
		{Group: 2, Key: "group-two", Hash: "hash-two", Duplicate: true, File: recycleTestRecord(`C:\Data\d.txt`, 20, 4)},
		{Group: 2, Key: "group-two", Hash: "hash-two", Duplicate: true, Selected: true, File: recycleTestRecord(`C:\Data\e.txt`, 20, 5)},
	}

	plan, err := buildRecyclePlan(rows)
	if err != nil {
		t.Fatal(err)
	}
	if plan.selectedCount != 2 || len(plan.groups) != 2 {
		t.Fatalf("plan selected=%d groups=%d", plan.selectedCount, len(plan.groups))
	}
	if len(plan.groups[0].request.Group.Files) != 3 || len(plan.groups[0].request.Selected) != 1 {
		t.Fatalf("first request = %+v", plan.groups[0].request)
	}
	if len(plan.groups[1].request.Group.Files) != 2 || len(plan.groups[1].request.Selected) != 1 {
		t.Fatalf("second request = %+v", plan.groups[1].request)
	}

	rows[0].File.Path = `D:\Mutated\later.txt`
	if got := plan.groups[0].request.Selected[0].Path; got != `C:\Data\a.txt` {
		t.Fatalf("immutable request path = %q", got)
	}
}

func TestBuildRecyclePlanRejectsSelectingEveryPhysicalCopy(t *testing.T) {
	rows := []duplicateRow{
		{Group: 1, Key: "group", Hash: "hash", Duplicate: true, Selected: true, File: recycleTestRecord(`C:\Data\a.txt`, 10, 1)},
		{Group: 1, Key: "group", Hash: "hash", Duplicate: true, Selected: true, File: recycleTestRecord(`C:\Data\b.txt`, 10, 2)},
	}

	_, err := buildRecyclePlan(rows)
	if !errors.Is(err, errRecycleWouldRemoveAllCopies) {
		t.Fatalf("error = %v, want all-copies rejection", err)
	}
}

func TestRecycleConfirmationMapsEveryTargetToRetainedKeepers(t *testing.T) {
	rows := []duplicateRow{
		{Group: 1, Key: "group", Hash: "hash", Duplicate: true, Selected: true, File: recycleTestRecord(`C:\Data\recycle-a.txt`, 10, 1)},
		{Group: 1, Key: "group", Hash: "hash", Duplicate: true, File: recycleTestRecord(`C:\Data\keep-b.txt`, 10, 2)},
		{Group: 1, Key: "group", Hash: "hash", Duplicate: true, File: recycleTestRecord(`C:\Data\keep-c.txt`, 10, 3)},
	}
	plan, err := buildRecyclePlan(rows)
	if err != nil {
		t.Fatal(err)
	}
	message, err := recycleConfirmationMessage(plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Group 1",
		"RECYCLE: " + displayFilesystemPath(`C:\Data\recycle-a.txt`),
		"KEEP:    " + displayFilesystemPath(`C:\Data\keep-b.txt`),
		"KEEP:    " + displayFilesystemPath(`C:\Data\keep-c.txt`),
		"Windows Recycle Bin only",
		"never permanently deletes",
	} {
		if !strings.Contains(message, required) {
			t.Fatalf("confirmation omitted %q:\n%s", required, message)
		}
	}
}

func TestRecycleConfirmationMakesBidiTargetExplicit(t *testing.T) {
	target := recycleTestRecord("C:\\Data\\report\u202Efdp.docx", 10, 1)
	keeper := recycleTestRecord(`C:\Data\reportxcod.pdf`, 10, 2)
	plan := recyclePlan{
		selectedCount: 1,
		groups: []plannedRecycleGroup{{
			groupNumber: 1,
			request: scanner.RecycleRequest{
				Group: scanner.DuplicateGroup{
					Size:  10,
					Hash:  "hash",
					Files: []scanner.FileRecord{target, keeper},
				},
				Selected: []scanner.FileRecord{target},
			},
		}},
	}

	message, err := recycleConfirmationMessage(plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(message, "\u202E") || !strings.Contains(message, `\u202e`) {
		t.Fatalf("confirmation retained a bidi override or omitted its escape: %q", message)
	}
	if !strings.Contains(message, "RECYCLE: "+displayFilesystemPath(target.Path)) ||
		!strings.Contains(message, "KEEP:    "+displayFilesystemPath(keeper.Path)) {
		t.Fatalf("confirmation did not identify target and keeper independently: %q", message)
	}
}

func TestRecycleConfirmationRejectsOversizedReviewWithoutTruncation(t *testing.T) {
	files := make([]scanner.FileRecord, maxRecycleConfirmationEntries+1)
	for index := range files {
		files[index] = recycleTestRecord(fmt.Sprintf(`C:\Data\copy-%02d.txt`, index), 10, byte(index+1))
	}
	plan := recyclePlan{
		selectedCount: 1,
		groups: []plannedRecycleGroup{{
			groupNumber: 1,
			request: scanner.RecycleRequest{
				Group:    scanner.DuplicateGroup{Size: 10, Hash: "hash", Files: files},
				Selected: []scanner.FileRecord{files[0]},
			},
		}},
	}

	message, err := recycleConfirmationMessage(plan)
	if !errors.Is(err, errRecycleConfirmationTooLarge) {
		t.Fatalf("error = %v, want oversized-review rejection", err)
	}
	if message != "" {
		t.Fatalf("oversized confirmation returned truncated content: %q", message)
	}
}

func TestApplyRecycleBatchRemovesOnlyRecycledAndRetainsEveryOtherStatus(t *testing.T) {
	model := &duplicateTableModel{rows: []duplicateRow{
		{Group: 1, Key: "group", Duplicate: true, Selected: true, Status: "Duplicate", File: recycleTestRecord(`C:\Data\recycled.txt`, 10, 1)},
		{Group: 1, Key: "group", Duplicate: true, Selected: true, Status: "Duplicate", File: recycleTestRecord(`C:\Data\changed.txt`, 10, 2)},
		{Group: 1, Key: "group", Duplicate: true, Selected: true, Status: "Duplicate", File: recycleTestRecord(`C:\Data\protected.txt`, 10, 3)},
		{Group: 1, Key: "group", Duplicate: true, Selected: true, Status: "Duplicate", File: recycleTestRecord(`C:\Data\cancelled.txt`, 10, 4)},
		{Group: 1, Key: "group", Duplicate: true, Selected: true, Status: "Duplicate", File: recycleTestRecord(`C:\Data\failed.txt`, 10, 5)},
		{Group: 1, Key: "group", Duplicate: true, Status: "Duplicate", File: recycleTestRecord(`C:\Data\keeper.txt`, 10, 6)},
	}}
	batch := recycleBatchResult{groups: []recycleGroupResult{{
		groupNumber: 1,
		result: scanner.RecycleResult{
			RequestError: "group policy warning",
			Items: []scanner.RecycleItemResult{
				{Path: `C:\Data\recycled.txt`, Status: scanner.RecycleStatusRecycled},
				{Path: `C:\Data\changed.txt`, Status: scanner.RecycleStatusSkippedChanged, Reason: "changed"},
				{Path: `C:\Data\protected.txt`, Status: scanner.RecycleStatusSkippedProtected, Reason: "protected"},
				{Path: `C:\Data\cancelled.txt`, Status: scanner.RecycleStatusCancelled, Reason: "cancelled"},
				{Path: `C:\Data\failed.txt`, Status: scanner.RecycleStatusFailed, Reason: "failed"},
			},
		},
	}}}

	outcome := applyRecycleBatchToModel(model, batch)
	if outcome.recycled != 1 || outcome.changed != 1 || outcome.protected != 1 || outcome.cancelled != 1 || outcome.failed != 1 || len(outcome.requestErrors) != 1 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(model.rows) != 5 {
		t.Fatalf("retained rows = %d, want 5", len(model.rows))
	}
	statuses := make(map[string]string, len(model.rows))
	for _, row := range model.rows {
		if row.Selected {
			t.Fatalf("retained row remained checked: %+v", row)
		}
		statuses[filepath.Base(row.File.Path)] = row.Status
	}
	if _, exists := statuses["recycled.txt"]; exists {
		t.Fatal("recycled row remained in the model")
	}
	if statuses["changed.txt"] != "Changed - kept" || statuses["protected.txt"] != "Protected - kept" || statuses["cancelled.txt"] != "Cancelled - kept" || statuses["failed.txt"] != "Recycle failed - kept" {
		t.Fatalf("retained statuses = %v", statuses)
	}
	if !strings.Contains(recycleOutcomeSummary(outcome), "1 recycled") || !recycleOutcomeHasIssues(outcome) {
		t.Fatalf("outcome summary did not expose all statuses: %q", recycleOutcomeSummary(outcome))
	}
}

func TestApplyRecycleBatchRejectsStaleDeleteTokenBeforeWidgetAccess(t *testing.T) {
	state := stateWithResults(t)
	token, err := state.beginDelete(operationTestStart)
	if err != nil {
		t.Fatal(err)
	}
	if accepted, _ := state.completeDelete(token); !accepted {
		t.Fatal("failed to retire delete token")
	}
	model := &duplicateTableModel{rows: []duplicateRow{{
		Group: 1, Key: "group", Duplicate: true, File: recycleTestRecord(`C:\Data\keep.txt`, 10, 1),
	}}}
	app := &windowsApp{operation: state, model: model}

	app.applyRecycleBatch(token, recycleBatchResult{})
	if len(app.model.rows) != 1 {
		t.Fatal("stale recycle result mutated model rows")
	}
}

func TestHandleWindowClosingCancelsAndInvalidatesActiveScan(t *testing.T) {
	state := stateWithFolder(t)
	cancelCalls := 0
	token, err := state.beginSurfaceScan(operationTestStart, func() { cancelCalls++ })
	if err != nil {
		t.Fatal(err)
	}
	app := &windowsApp{operation: state}
	canceled := false

	app.handleWindowClosing(&canceled, 0)
	if canceled {
		t.Fatal("non-destructive scan close was unexpectedly deferred")
	}
	if cancelCalls != 1 || !app.uiClosing.Load() {
		t.Fatalf("close state: cancel calls=%d uiClosing=%v", cancelCalls, app.uiClosing.Load())
	}
	if app.operation.accepts(token) || app.operation.phase != phaseClosing {
		t.Fatalf("scan token survived close: %+v", app.operation)
	}
}

func recycleTestRecord(path string, size int64, identityByte byte) scanner.FileRecord {
	identity := scanner.FileIdentity{VolumeSerial: 1}
	identity.FileID[0] = identityByte
	return scanner.FileRecord{
		Path:      path,
		Size:      size,
		Identity:  identity,
		LinkCount: 1,
	}
}

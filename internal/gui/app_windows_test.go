package gui

import (
	"reflect"
	"testing"

	"duplicate-file-finder-go/internal/scanner"
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

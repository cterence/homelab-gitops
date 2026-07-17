package sync

import "testing"

func TestReconcile(t *testing.T) {
	desired := []DesiredEvent{
		{ShiftID: "a", Summary: "On-call: X", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"}, // new
		{ShiftID: "b", Summary: "On-call: Y", Start: "2026-02-01T00:00:00Z", End: "2026-02-02T00:00:00Z"}, // changed
		{ShiftID: "c", Summary: "On-call: Z", Start: "2026-03-01T00:00:00Z", End: "2026-03-02T00:00:00Z"}, // unchanged
	}
	existing := []ExistingEvent{
		{ID: "e-b", ShiftID: "b", Summary: "On-call: OLD", Start: "2026-02-01T00:00:00Z", End: "2026-02-02T00:00:00Z"},
		{ID: "e-c", ShiftID: "c", Summary: "On-call: Z", Start: "2026-03-01T00:00:00Z", End: "2026-03-02T00:00:00Z"},
		{ID: "e-d", ShiftID: "d", Summary: "On-call: GONE", Start: "2026-04-01T00:00:00Z", End: "2026-04-02T00:00:00Z"}, // orphan
	}

	p := Reconcile(desired, existing)

	if len(p.Create) != 1 || p.Create[0].ShiftID != "a" {
		t.Fatalf("create: %+v", p.Create)
	}
	if len(p.Update) != 1 || p.Update[0].Desired.ShiftID != "b" || p.Update[0].Existing.ID != "e-b" {
		t.Fatalf("update: %+v", p.Update)
	}
	if len(p.Delete) != 1 || p.Delete[0].ShiftID != "d" {
		t.Fatalf("delete: %+v", p.Delete)
	}
}

func TestReconcile_NoUpdateOnEquivalentTimezone(t *testing.T) {
	desired := []DesiredEvent{
		{ShiftID: "a", Summary: "On-call: X", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"},
	}
	existing := []ExistingEvent{
		{ID: "e-a", ShiftID: "a", Summary: "On-call: X", Start: "2026-01-01T00:00:00+00:00", End: "2026-01-02T00:00:00+00:00"},
	}

	p := Reconcile(desired, existing)

	if len(p.Create) != 0 || len(p.Update) != 0 || len(p.Delete) != 0 {
		t.Fatalf("expected empty plan, got %+v", p)
	}
}

func TestReconcile_DuplicateExistingShiftIDDeletesExtra(t *testing.T) {
	desired := []DesiredEvent{
		{ShiftID: "a", Summary: "On-call: X", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"},
	}
	existing := []ExistingEvent{
		{ID: "e1", ShiftID: "a", Summary: "On-call: X", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"},
		{ID: "e2", ShiftID: "a", Summary: "On-call: X", Start: "2026-01-01T00:00:00Z", End: "2026-01-02T00:00:00Z"},
	}

	p := Reconcile(desired, existing)

	if len(p.Create) != 0 || len(p.Update) != 0 {
		t.Fatalf("expected no create/update, got create=%+v update=%+v", p.Create, p.Update)
	}
	if len(p.Delete) != 1 || p.Delete[0].ID != "e2" {
		t.Fatalf("expected extra e2 in delete, got %+v", p.Delete)
	}
}

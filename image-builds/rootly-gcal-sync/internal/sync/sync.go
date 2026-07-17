package sync

import "time"

type DesiredEvent struct {
	ShiftID string
	Summary string
	Start   string
	End     string
}

type ExistingEvent struct {
	ID      string
	ShiftID string
	Summary string
	Start   string
	End     string
}

type UpdatePair struct {
	Existing ExistingEvent
	Desired  DesiredEvent
}

type Plan struct {
	Create []DesiredEvent
	Update []UpdatePair
	Delete []ExistingEvent
}

func sameInstant(a, b string) bool {
	ta, ea := time.Parse(time.RFC3339, a)
	tb, eb := time.Parse(time.RFC3339, b)
	if ea != nil || eb != nil {
		return a == b // fall back to string compare if either is unparseable
	}
	return ta.Equal(tb)
}

func Reconcile(desired []DesiredEvent, existing []ExistingEvent) Plan {
	var p Plan
	existingByShift := make(map[string]ExistingEvent, len(existing))
	for _, e := range existing {
		if _, dup := existingByShift[e.ShiftID]; dup {
			p.Delete = append(p.Delete, e) // orphaned duplicate, clean it up
			continue
		}
		existingByShift[e.ShiftID] = e
	}
	desiredByShift := make(map[string]struct{}, len(desired))

	for _, d := range desired {
		desiredByShift[d.ShiftID] = struct{}{}
		e, ok := existingByShift[d.ShiftID]
		if !ok {
			p.Create = append(p.Create, d)
			continue
		}
		if e.Summary != d.Summary || !sameInstant(e.Start, d.Start) || !sameInstant(e.End, d.End) {
			p.Update = append(p.Update, UpdatePair{Existing: e, Desired: d})
		}
	}
	for _, e := range existingByShift {
		if _, ok := desiredByShift[e.ShiftID]; !ok {
			p.Delete = append(p.Delete, e)
		}
	}
	return p
}

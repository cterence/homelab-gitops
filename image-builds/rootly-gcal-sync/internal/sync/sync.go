package sync

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

func Reconcile(desired []DesiredEvent, existing []ExistingEvent) Plan {
	existingByShift := make(map[string]ExistingEvent, len(existing))
	for _, e := range existing {
		existingByShift[e.ShiftID] = e
	}
	desiredByShift := make(map[string]struct{}, len(desired))

	var p Plan
	for _, d := range desired {
		desiredByShift[d.ShiftID] = struct{}{}
		e, ok := existingByShift[d.ShiftID]
		if !ok {
			p.Create = append(p.Create, d)
			continue
		}
		if e.Summary != d.Summary || e.Start != d.Start || e.End != d.End {
			p.Update = append(p.Update, UpdatePair{Existing: e, Desired: d})
		}
	}
	for _, e := range existing {
		if _, ok := desiredByShift[e.ShiftID]; !ok {
			p.Delete = append(p.Delete, e)
		}
	}
	return p
}

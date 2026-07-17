package gcal

import (
	"context"
	"fmt"
	"log"
	"time"

	calendar "google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"

	sync "github.com/cterence/homelab-gitops/rootly-gcal-sync/internal/sync"
)

const (
	MarkerKey = "managedBy"
	MarkerVal = "rootly-gcal-sync"
	ShiftKey  = "rootlyShiftId"
)

type Client struct {
	srv        *calendar.Service
	calendarID string
}

func New(ctx context.Context, credsFile, calendarID string) (*Client, error) {
	srv, err := calendar.NewService(ctx,
		option.WithCredentialsFile(credsFile),
		option.WithScopes(calendar.CalendarScope),
	)
	if err != nil {
		return nil, fmt.Errorf("calendar service: %w", err)
	}
	return &Client{srv: srv, calendarID: calendarID}, nil
}

func (c *Client) ListManaged(ctx context.Context, syncDays int) ([]sync.ExistingEvent, error) {
	now := time.Now().UTC()
	timeMin := now.Format(time.RFC3339)
	timeMax := now.AddDate(0, 0, syncDays).Format(time.RFC3339)

	var out []sync.ExistingEvent
	call := c.srv.Events.List(c.calendarID).
		PrivateExtendedProperty(MarkerKey + "=" + MarkerVal).
		TimeMin(timeMin).TimeMax(timeMax).
		SingleEvents(true).ShowDeleted(false).MaxResults(2500).
		Context(ctx)

	err := call.Pages(ctx, func(page *calendar.Events) error {
		for _, it := range page.Items {
			if it.Start == nil || it.End == nil {
				continue
			}
			out = append(out, sync.ExistingEvent{
				ID:      it.Id,
				ShiftID: it.ExtendedProperties.Private[ShiftKey],
				Summary: it.Summary,
				Start:   it.Start.DateTime,
				End:     it.End.DateTime,
			})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	return out, nil
}

func toGEvent(d sync.DesiredEvent) *calendar.Event {
	return &calendar.Event{
		Summary: d.Summary,
		Start:   &calendar.EventDateTime{DateTime: d.Start},
		End:     &calendar.EventDateTime{DateTime: d.End},
		ExtendedProperties: &calendar.EventExtendedProperties{
			Private: map[string]string{MarkerKey: MarkerVal, ShiftKey: d.ShiftID},
		},
	}
}

func (c *Client) Apply(ctx context.Context, p sync.Plan, dryRun bool) error {
	for _, d := range p.Create {
		log.Printf("create shift=%s %q %s..%s", d.ShiftID, d.Summary, d.Start, d.End)
		if dryRun {
			continue
		}
		if _, err := c.srv.Events.Insert(c.calendarID, toGEvent(d)).Context(ctx).Do(); err != nil {
			return fmt.Errorf("insert shift %s: %w", d.ShiftID, err)
		}
	}
	for _, u := range p.Update {
		log.Printf("update shift=%s event=%s %q", u.Desired.ShiftID, u.Existing.ID, u.Desired.Summary)
		if dryRun {
			continue
		}
		if _, err := c.srv.Events.Update(c.calendarID, u.Existing.ID, toGEvent(u.Desired)).Context(ctx).Do(); err != nil {
			return fmt.Errorf("update event %s: %w", u.Existing.ID, err)
		}
	}
	for _, e := range p.Delete {
		log.Printf("delete shift=%s event=%s", e.ShiftID, e.ID)
		if dryRun {
			continue
		}
		if err := c.srv.Events.Delete(c.calendarID, e.ID).Context(ctx).Do(); err != nil {
			return fmt.Errorf("delete event %s: %w", e.ID, err)
		}
	}
	return nil
}

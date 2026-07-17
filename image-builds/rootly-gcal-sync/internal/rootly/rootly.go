package rootly

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	rootly "github.com/rootlyhq/rootly-go"

	sync "github.com/cterence/homelab-gitops/rootly-gcal-sync/internal/sync"
)

const baseURLDefault = "https://api.rootly.com"

type Client struct {
	api *rootly.ClientWithResponses
}

func New(baseURL, token string) (*Client, error) {
	if baseURL == "" {
		baseURL = baseURLDefault
	}
	api, err := rootly.NewClientWithResponses(baseURL, rootly.WithRequestEditorFn(
		func(ctx context.Context, req *http.Request) error {
			req.Header.Set("Authorization", "Bearer "+token)
			return nil
		},
	))
	if err != nil {
		return nil, fmt.Errorf("rootly client: %w", err)
	}
	return &Client{api: api}, nil
}

func (c *Client) ResolveUserID(ctx context.Context, email string) (int, error) {
	resp, err := c.api.ListUsersWithResponse(ctx, &rootly.ListUsersParams{FilterEmail: &email})
	if err != nil {
		return 0, fmt.Errorf("list users: %w", err)
	}
	if resp.StatusCode() != http.StatusOK || resp.ApplicationVndAPIJSON200 == nil {
		return 0, fmt.Errorf("list users status %d", resp.StatusCode())
	}
	data := resp.ApplicationVndAPIJSON200.Data
	if len(data) != 1 {
		return 0, fmt.Errorf("expected exactly 1 user for %q, got %d", email, len(data))
	}
	id, err := strconv.Atoi(data[0].ID)
	if err != nil {
		return 0, fmt.Errorf("parse user id %q: %w", data[0].ID, err)
	}
	return id, nil
}

func (c *Client) scheduleNames(ctx context.Context) (map[string]string, error) {
	names := map[string]string{}
	page := 1
	for {
		size := 100
		resp, err := c.api.ListSchedulesWithResponse(ctx, &rootly.ListSchedulesParams{
			PageNumber: &page, PageSize: &size,
		})
		if err != nil {
			return nil, fmt.Errorf("list schedules: %w", err)
		}
		if resp.StatusCode() != http.StatusOK || resp.ApplicationVndAPIJSON200 == nil {
			return nil, fmt.Errorf("list schedules status %d", resp.StatusCode())
		}
		body := resp.ApplicationVndAPIJSON200
		for _, s := range body.Data {
			names[s.ID] = s.Attributes.Name
		}
		if page >= body.Meta.TotalPages {
			break
		}
		page++
	}
	return names, nil
}

func (c *Client) DesiredEvents(ctx context.Context, userID, syncDays int) ([]sync.DesiredEvent, error) {
	names, err := c.scheduleNames(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	from := now.Format(time.RFC3339)
	to := now.AddDate(0, 0, syncDays).Format(time.RFC3339)

	var out []sync.DesiredEvent
	page := 1
	for {
		size := 1000
		resp, err := c.api.ListShiftsWithResponse(ctx, &rootly.ListShiftsParams{
			From: &from, To: &to, UserIDs: []int{userID},
			PageNumber: &page, PageSize: &size,
		})
		if err != nil {
			return nil, fmt.Errorf("list shifts: %w", err)
		}
		if resp.StatusCode() != http.StatusOK || resp.ApplicationVndAPIJSON200 == nil {
			return nil, fmt.Errorf("list shifts status %d", resp.StatusCode())
		}
		body := resp.ApplicationVndAPIJSON200
		for _, s := range body.Data {
			a := s.Attributes
			name := names[a.ScheduleID]
			if name == "" {
				name = a.ScheduleID
			}
			out = append(out, sync.DesiredEvent{
				ShiftID: s.ID,
				Summary: "On-call: " + name,
				Start:   a.StartsAt,
				End:     a.EndsAt,
			})
		}
		if body.Meta == nil || page >= body.Meta.TotalPages {
			break
		}
		page++
	}
	return out, nil
}

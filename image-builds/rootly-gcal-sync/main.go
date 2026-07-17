package main

import (
	"context"
	"log"

	"github.com/cterence/homelab-gitops/rootly-gcal-sync/internal/config"
	"github.com/cterence/homelab-gitops/rootly-gcal-sync/internal/gcal"
	"github.com/cterence/homelab-gitops/rootly-gcal-sync/internal/rootly"
	syncpkg "github.com/cterence/homelab-gitops/rootly-gcal-sync/internal/sync"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("rootly-gcal-sync: %v", err)
	}
}

func run() error {
	ctx := context.Background()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	rc, err := rootly.New("", cfg.RootlyToken)
	if err != nil {
		return err
	}
	userID, err := rc.ResolveUserID(ctx, cfg.RootlyUserEmail)
	if err != nil {
		return err
	}
	log.Printf("resolved rootly user %s -> id %d", cfg.RootlyUserEmail, userID)

	desired, err := rc.DesiredEvents(ctx, userID, cfg.SyncDays)
	if err != nil {
		return err
	}
	log.Printf("fetched %d desired shift(s) over %d day(s)", len(desired), cfg.SyncDays)

	gc, err := gcal.New(ctx, cfg.GoogleCredsFile, cfg.GCalCalendarID)
	if err != nil {
		return err
	}
	existing, err := gc.ListManaged(ctx, cfg.SyncDays)
	if err != nil {
		return err
	}
	log.Printf("found %d managed calendar event(s)", len(existing))

	plan := syncpkg.Reconcile(desired, existing)
	log.Printf("plan: create=%d update=%d delete=%d (dry_run=%v)",
		len(plan.Create), len(plan.Update), len(plan.Delete), cfg.DryRun)

	if err := gc.Apply(ctx, plan, cfg.DryRun); err != nil {
		return err
	}
	log.Printf("sync complete")
	return nil
}

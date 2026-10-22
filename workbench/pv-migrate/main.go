// pv-migrate wraps the pv-migrate CLI to migrate data from a Released
// PersistentVolume to a destination PVC. It creates a temporary PVC that
// statically binds the source PV, runs pv-migrate, then deletes the temp PVC
// — leaving the source PV intact (under a Retain reclaim policy) and the
// destination PVC populated.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	cli "github.com/urfave/cli/v3"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const defaultBindTimeout = 5 * time.Minute

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "pv-migrate: %v\n", err)
		os.Exit(1)
	}
}

type config struct {
	sourcePV             string
	destPVC              string
	destNamespace        string
	tempNamespace        string
	kubeconfig           string
	allowDeleteReclaim   bool
	allowDestMismatch    bool
	dryRun               bool
	bindTimeout          time.Duration
	strategies           string
	helmTimeout          string
	logLevel             string
	deleteExtraneous     bool
	ignoreMounted        bool
	nonRoot              bool
	noChown              bool
	sourceMountReadWrite bool
	noCompress           bool
	sourceNode           string
	destNode             string
}

func run(ctx context.Context, args []string) error {
	var cfg config

	cmd := &cli.Command{
		Name:  "pv-migrate",
		Usage: "migrate data from a Released PersistentVolume to a destination PVC",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "source-pv", Destination: &cfg.sourcePV, Usage: "name of the source PersistentVolume (Released or Available)"},
			&cli.StringFlag{Name: "dest-pvc", Destination: &cfg.destPVC, Usage: "name of the destination PVC (must already exist)"},
			&cli.StringFlag{Name: "dest-namespace", Destination: &cfg.destNamespace, Usage: "namespace of the destination PVC"},
			&cli.StringFlag{Name: "temp-namespace", Destination: &cfg.tempNamespace, Usage: "namespace for the temporary source PVC (default: source PV's old claimRef namespace)"},
			&cli.StringFlag{Name: "kubeconfig", Destination: &cfg.kubeconfig, Usage: "path to kubeconfig (defaults to in-cluster or ~/.kube/config)"},
			&cli.BoolFlag{Name: "allow-delete-reclaim", Destination: &cfg.allowDeleteReclaim, Usage: "allow migrating from a PV with a Delete reclaim policy (dangerous: deleting the temp PVC deletes the PV)"},
			&cli.BoolFlag{Name: "allow-dest-mismatch", Destination: &cfg.allowDestMismatch, Usage: "skip the check that the dest PVC name matches the source PV's previous consumer"},
			&cli.BoolFlag{Name: "dry-run", Destination: &cfg.dryRun, Usage: "print the plan and exit without creating or modifying anything"},
			&cli.DurationFlag{Name: "bind-timeout", Value: defaultBindTimeout, Destination: &cfg.bindTimeout, Usage: "how long to wait for the temp PVC to bind"},
			&cli.StringFlag{Name: "strategies", Destination: &cfg.strategies, Usage: "comma-separated pv-migrate strategies (default: pv-migrate's built-in default)"},
			&cli.StringFlag{Name: "helm-timeout", Destination: &cfg.helmTimeout, Usage: "helm install/uninstall timeout, e.g. 2m (default: pv-migrate's built-in default)"},
			&cli.StringFlag{Name: "log-level", Destination: &cfg.logLevel, Usage: "pv-migrate log level: DEBUG, INFO, WARN, ERROR (default: pv-migrate's built-in default)"},
			&cli.BoolFlag{Name: "delete-extraneous-files", Destination: &cfg.deleteExtraneous, Usage: "pass --dest-delete-extraneous-files to pv-migrate (rsync --delete)"},
			&cli.BoolFlag{Name: "ignore-mounted", Destination: &cfg.ignoreMounted, Usage: "do not fail if the source or destination PVC is mounted"},
			&cli.BoolFlag{Name: "non-root", Destination: &cfg.nonRoot, Usage: "run migration containers as non-root (restricted PodSecurity)"},
			&cli.BoolFlag{Name: "no-chown", Destination: &cfg.noChown, Usage: "omit chown during rsync"},
			&cli.BoolFlag{Name: "source-mount-read-write", Destination: &cfg.sourceMountReadWrite, Usage: "mount the source PVC read-write during migration"},
			&cli.BoolFlag{Name: "no-compress", Destination: &cfg.noCompress, Usage: "disable rsync compression"},
			&cli.StringFlag{Name: "source-node", Destination: &cfg.sourceNode, Usage: "pin the source-side pod (sshd in pull mode) to this node"},
			&cli.StringFlag{Name: "dest-node", Destination: &cfg.destNode, Usage: "pin the dest-side pod (rsync in pull mode) to this node — triggers WaitForFirstConsumer PVC provisioning on that node"},
		},
		Action: func(ctx context.Context, _ *cli.Command) error {
			return migrate(ctx, cfg)
		},
	}

	return cmd.Run(ctx, args)
}

// migrate is the command body: everything after flag parsing.
func migrate(ctx context.Context, cfg config) error {
	if cfg.sourcePV == "" || cfg.destPVC == "" || cfg.destNamespace == "" {
		return errors.New("--source-pv, --dest-pvc and --dest-namespace are required")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	client, err := newK8sClient(cfg.kubeconfig)
	if err != nil {
		return err
	}

	// Gather state.
	pv, err := client.getPV(ctx, cfg.sourcePV)
	if err != nil {
		return err
	}

	live, err := client.pvcExists(ctx, pv.Spec.ClaimRef)
	if err != nil {
		return err
	}

	if err := validatePVState(pv.Status.Phase, pv.Spec.PersistentVolumeReclaimPolicy, live, cfg.allowDeleteReclaim); err != nil {
		return err
	}

	if err := validateDestName(pv.Spec.ClaimRef, cfg.destPVC, cfg.allowDestMismatch); err != nil {
		return err
	}

	if _, err := client.core.CoreV1().PersistentVolumeClaims(cfg.destNamespace).Get(
		ctx, cfg.destPVC, metav1.GetOptions{}); err != nil {
		return fmt.Errorf("getting destination PVC %s/%s: %w", cfg.destNamespace, cfg.destPVC, err)
	}

	tempNS := tempNamespace(pv, cfg.tempNamespace, cfg.destNamespace)
	tempName := tempPVCName(pv.Name)

	slog.Info("plan",
		"source_pv", pv.Name,
		"source_phase", pv.Status.Phase,
		"reclaim_policy", pv.Spec.PersistentVolumeReclaimPolicy,
		"temp_pvc", tempName,
		"temp_namespace", tempNS,
		"dest_pvc", cfg.destPVC,
		"dest_namespace", cfg.destNamespace,
		"source_node", cfg.sourceNode,
		"dest_node", cfg.destNode,
	)

	if cfg.dryRun {
		slog.Info("dry-run: no resources will be created or modified")
		return nil
	}

	return doMigrate(ctx, client, pv, cfg, tempNS, tempName)
}

// doMigrate runs the full sequence: clear claimRef, create temp PVC, wait for
// bind, invoke pv-migrate. It defers temp-PVC cleanup so the PVC is deleted on
// success, failure, or context cancellation. Under Retain the PV returns to
// Released after temp-PVC deletion; under Delete (opted in via
// --allow-delete-reclaim) the PV is gone, which is expected.
func doMigrate(ctx context.Context, client *k8sClient, pv *corev1.PersistentVolume,
	cfg config, tempNS, tempName string) error {
	tempCreated := false

	// cleanup always uses a fresh context: the run context may be canceled,
	// but we still need to delete the temp PVC. The timeout bounds the
	// deletion attempt so a stuck API server does not hang the process.
	defer func() {
		if !tempCreated {
			return
		}

		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()

		if err := client.deleteTempPVC(cleanupCtx, tempName, tempNS); err != nil {
			slog.Error("cleanup: deleting temp PVC", "error", err)
		}

		// pv-migrate may leave Helm releases behind if it was interrupted
		// before its own cleanup ran. Sweep them so nothing lingers.
		cleanupPVMigrateReleases(cfg.kubeconfig)
	}()

	// Make the PV Available if it is still Released (claimRef set).
	if pv.Spec.ClaimRef != nil {
		slog.Info("clearing PV claimRef to make it Available", "pv", pv.Name)

		if err := client.patchPVClaimRefNil(ctx, pv.Name); err != nil {
			return err
		}
	}

	// Create the temp PVC that statically binds to the source PV.
	claim := buildTempPVC(pv, tempName, tempNS)
	slog.Info("creating temp PVC", "pvc", tempName, "namespace", tempNS)

	if err := client.createTempPVC(ctx, claim); err != nil {
		return err
	}

	tempCreated = true

	// Wait for the temp PVC to bind to the source PV.
	bindCtx, cancel := context.WithTimeout(ctx, cfg.bindTimeout)
	defer cancel()

	bound, err := client.waitForPVCBound(bindCtx, tempName, tempNS)
	if err != nil {
		return err
	}

	slog.Info("temp PVC bound", "pvc", tempName, "pv", bound.Spec.VolumeName)

	// Run the migration.
	var strategies []string
	if cfg.strategies != "" {
		strategies = splitCSV(cfg.strategies)
	}

	strategies = resolveStrategies(strategies, cfg.sourceNode, cfg.destNode)

	args := pvmigrateArgs(tempName, tempNS, cfg, strategies)
	if err := runMigration(ctx, args); err != nil {
		return err
	}

	slog.Info("migration complete", "source_pv", pv.Name, "dest_pvc", cfg.destPVC)

	return nil
}

func splitCSV(s string) []string {
	var out []string

	for _, f := range strings.Split(s, ",") {
		if f != "" {
			out = append(out, f)
		}
	}

	return out
}

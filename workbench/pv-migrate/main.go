// pv-migrate wraps the pv-migrate CLI to migrate data from a Released
// PersistentVolume to a destination PVC. It creates a temporary PVC that
// statically binds the source PV, runs pv-migrate, then deletes the temp PVC
// — leaving the source PV intact (under a Retain reclaim policy) and the
// destination PVC populated.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	corev1 "k8s.io/api/core/v1"
)

const defaultBindTimeout = 5 * time.Minute

func main() {
	if err := run(); err != nil {
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

func run() error {
	var cfg config

	flag.StringVar(&cfg.sourcePV, "source-pv", "", "name of the source PersistentVolume (Released or Available)")
	flag.StringVar(&cfg.destPVC, "dest-pvc", "", "name of the destination PVC (must already exist)")
	flag.StringVar(&cfg.destNamespace, "dest-namespace", "", "namespace of the destination PVC")
	flag.StringVar(&cfg.tempNamespace, "temp-namespace", "", "namespace for the temporary source PVC (default: source PV's old claimRef namespace)")
	flag.StringVar(&cfg.kubeconfig, "kubeconfig", "", "path to kubeconfig (defaults to in-cluster or ~/.kube/config)")
	flag.BoolVar(&cfg.allowDeleteReclaim, "allow-delete-reclaim", false, "allow migrating from a PV with a Delete reclaim policy (dangerous: deleting the temp PVC deletes the PV)")
	flag.BoolVar(&cfg.allowDestMismatch, "allow-dest-mismatch", false, "skip the check that the dest PVC name matches the source PV's previous consumer")
	flag.BoolVar(&cfg.dryRun, "dry-run", false, "print the plan and exit without creating or modifying anything")
	flag.DurationVar(&cfg.bindTimeout, "bind-timeout", defaultBindTimeout, "how long to wait for the temp PVC to bind")
	flag.StringVar(&cfg.strategies, "strategies", "", "comma-separated pv-migrate strategies (default: pv-migrate's built-in default)")
	flag.StringVar(&cfg.helmTimeout, "helm-timeout", "", "helm install/uninstall timeout, e.g. 2m (default: pv-migrate's built-in default)")
	flag.StringVar(&cfg.logLevel, "log-level", "", "pv-migrate log level: DEBUG, INFO, WARN, ERROR (default: pv-migrate's built-in default)")
	flag.BoolVar(&cfg.deleteExtraneous, "delete-extraneous-files", false, "pass --dest-delete-extraneous-files to pv-migrate (rsync --delete)")
	flag.BoolVar(&cfg.ignoreMounted, "ignore-mounted", false, "do not fail if the source or destination PVC is mounted")
	flag.BoolVar(&cfg.nonRoot, "non-root", false, "run migration containers as non-root (restricted PodSecurity)")
	flag.BoolVar(&cfg.noChown, "no-chown", false, "omit chown during rsync")
	flag.BoolVar(&cfg.sourceMountReadWrite, "source-mount-read-write", false, "mount the source PVC read-write during migration")
	flag.BoolVar(&cfg.noCompress, "no-compress", false, "disable rsync compression")
	flag.StringVar(&cfg.sourceNode, "source-node", "", "pin the source-side pod (sshd in pull mode) to this node")
	flag.StringVar(&cfg.destNode, "dest-node", "", "pin the dest-side pod (rsync in pull mode) to this node — triggers WaitForFirstConsumer PVC provisioning on that node")
	flag.Parse()

	if cfg.sourcePV == "" || cfg.destPVC == "" || cfg.destNamespace == "" {
		return errors.New("--source-pv, --dest-pvc and --dest-namespace are required")
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	if _, err := client.getDestPVC(ctx, cfg.destPVC, cfg.destNamespace); err != nil {
		return err
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
		cleanupPVMigrateReleases(cfg.kubeconfig, os.Stderr)
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

	opts := migrationOpts{
		deleteExtraneous:     cfg.deleteExtraneous,
		ignoreMounted:        cfg.ignoreMounted,
		nonRoot:              cfg.nonRoot,
		noChown:              cfg.noChown,
		sourceMountReadWrite: cfg.sourceMountReadWrite,
		noCompress:           cfg.noCompress,
		strategies:           strategies,
		helmTimeout:          cfg.helmTimeout,
		logLevel:             cfg.logLevel,
		sourceNode:           cfg.sourceNode,
		destNode:             cfg.destNode,
	}

	args := pvmigrateArgs(tempName, tempNS, cfg.destPVC, cfg.destNamespace, cfg.kubeconfig, opts)
	if err := runMigration(ctx, args, os.Stdout, os.Stderr); err != nil {
		return err
	}

	slog.Info("migration complete", "source_pv", pv.Name, "dest_pvc", cfg.destPVC)

	return nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}

	var out []string

	start := 0

	for i := 0; i <= len(s); i++ {
		if i == len(s) || s[i] == ',' {
			if i > start {
				out = append(out, s[start:i])
			}

			start = i + 1
		}
	}

	return out
}

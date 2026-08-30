package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

// errPVMigrateNotFound is returned when the pv-migrate CLI is not on PATH.
var errPVMigrateNotFound = errors.New(
	"pv-migrate CLI not found on PATH: install it " +
		"(see https://github.com/utkuozdemir/pv-migrate#installation) " +
		"and ensure it is executable")

// runMigration invokes the pv-migrate CLI with the given argument list. It
// streams the child's stdout/stderr to the parent so progress bars and logs
// are visible. The CLI must be on PATH; otherwise it returns
// errPVMigrateNotFound.
func runMigration(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	bin, err := exec.LookPath("pv-migrate")
	if err != nil {
		return errPVMigrateNotFound
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// pv-migrate shows a progress bar on stderr when it is a TTY; we let the
	// child inherit the parent's TTY status by leaving stdin untouched.
	cmd.Stdin = os.Stdin

	slog.Info("running pv-migrate", "bin", bin, "args", args)

	if err := cmd.Run(); err != nil {
		// exec.ExitError carries the child's stderr through Run's returned
		// error message, but we already streamed it; surface a concise line.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("pv-migrate exited with code %d: %w", exitErr.ExitCode(), exitErr)
		}

		return fmt.Errorf("running pv-migrate: %w", err)
	}

	return nil
}

// cleanupPVMigrateReleases runs `pv-migrate cleanup --all --force` to remove
// any Helm releases left behind by an interrupted or failed migration. When
// pv-migrate is killed by a signal (e.g. SIGINT), it does not uninstall its
// own Helm releases, leaving orphaned jobs, pods and secrets. This is a
// best-effort sweep: if the CLI is missing or the cleanup fails, the error is
// logged but not returned — the caller should still delete the temp PVC.
//
// kubeconfig may be empty to let pv-migrate use its default discovery.
func cleanupPVMigrateReleases(kubeconfig string, stderr io.Writer) {
	bin, err := exec.LookPath("pv-migrate")
	if err != nil {
		return
	}

	args := []string{"cleanup", "--all", "--force"}
	if kubeconfig != "" {
		args = append(args, "--kubeconfig", kubeconfig)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		slog.Warn("pv-migrate cleanup had errors (orphaned releases may remain)", "error", err)
		return
	}

	slog.Info("pv-migrate cleanup done")
}

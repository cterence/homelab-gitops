package main

import (
	"errors"
	"fmt"
	"os"
)

// checkLockFile reports whether the lock file is present. An empty path means
// no locking is configured and the run should proceed.
func checkLockFile(path string) (bool, error) {
	if path == "" {
		return false, nil
	}

	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}

	return false, fmt.Errorf("failed to check lock file %s: %w", path, err)
}

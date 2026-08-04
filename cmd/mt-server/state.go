package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/MingYuan0415/mt-server/internal/platform/config"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

func runState(arguments []string) (resultErr error) {
	if len(arguments) != 1 || arguments[0] != "restore-v3-backup" {
		return errors.New("usage: state restore-v3-backup")
	}
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	store, err := state.NewStore(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("open state: %w", err)
	}
	stateLock, err := store.AcquireLock()
	if err != nil {
		if errors.Is(err, state.ErrLocked) {
			return errors.New("stop mt-server before restoring the migration backup")
		}
		return err
	}
	defer func() {
		if err := stateLock.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("release state lock: %w", err)
		}
	}()
	result, err := store.RestoreV3Backup()
	if err != nil {
		return err
	}
	if !result.DurabilityConfirmed {
		_, _ = fmt.Fprintln(os.Stderr,
			"warning: schema v3 was restored, but directory durability could not be confirmed")
	}
	_, _ = fmt.Fprintln(os.Stdout, "schema v3 migration backup restored")
	return nil
}

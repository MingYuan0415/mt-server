package main

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/config"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

func runAdminOrigin(arguments []string) (resultErr error) {
	if len(arguments) < 1 || len(arguments) > 2 {
		return errors.New("usage: admin-origin list|add <https-origin>|remove <https-origin>")
	}
	command := arguments[0]
	if (command == "list" && len(arguments) != 1) ||
		((command == "add" || command == "remove") && len(arguments) != 2) {
		return errors.New("usage: admin-origin list|add <https-origin>|remove <https-origin>")
	}
	if command != "list" && command != "add" && command != "remove" {
		return errors.New("usage: admin-origin list|add <https-origin>|remove <https-origin>")
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
			return errors.New("stop mt-server before running offline origin maintenance")
		}
		return err
	}
	defer func() {
		if err := stateLock.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("release state lock: %w", err)
		}
	}()
	value, err := store.Load()
	if err != nil {
		return err
	}
	origins, err := adminauth.NormalizePublicOrigins(value.Admin.PublicOrigins)
	if err != nil || !slices.Equal(origins, value.Admin.PublicOrigins) {
		return errors.New("stored management origins are invalid or not canonical")
	}
	if command == "list" {
		for _, origin := range origins {
			_, _ = fmt.Fprintln(os.Stdout, origin)
		}
		return nil
	}
	origin, err := adminauth.NormalizePublicOrigin(arguments[1])
	if err != nil {
		return err
	}
	switch command {
	case "add":
		if slices.Contains(origins, origin) {
			return errors.New("management origin already exists")
		}
		if len(origins) >= adminauth.MaximumPublicOrigins {
			return fmt.Errorf("at most %d origins are allowed", adminauth.MaximumPublicOrigins)
		}
		origins = append(origins, origin)
	case "remove":
		index := slices.Index(origins, origin)
		if index < 0 {
			return errors.New("management origin not found")
		}
		origins = append(origins[:index], origins[index+1:]...)
	}
	policy := adminauth.NewTransportPolicy(
		cfg.AdminAllowInsecureHTTP, cfg.AdminBehindHTTPSProxy)
	origins, err = policy.ValidatePublicOrigins(origins)
	if err != nil {
		return err
	}
	value.Admin.PublicOrigins = origins
	value.UpdatedAt = time.Now().UTC()
	result, err := store.Save(value)
	if err != nil {
		return err
	}
	if !result.DurabilityConfirmed {
		_, _ = fmt.Fprintln(os.Stderr,
			"warning: state was committed, but directory durability could not be confirmed")
	}
	_, _ = fmt.Fprintln(os.Stdout, "management origins updated")
	return nil
}

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

func TestRunAdminOriginMaintainsStateOffline(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("MT_STATE_DIR", directory)
	t.Setenv("MT_ADMIN_BEHIND_HTTPS_PROXY", "true")
	t.Setenv("MT_ADMIN_ALLOW_INSECURE_HTTP", "false")
	store, err := state.NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	value := state.State{
		SchemaVersion: state.SchemaVersion,
		UpdatedAt:     time.Now().UTC(),
		Admin: state.AdminState{
			PublicOrigins: []string{"https://old.example.com"},
		},
	}
	if _, err := store.CommitInitial(value); err != nil {
		t.Fatal(err)
	}
	if err := runAdminOrigin([]string{"add", "https://NEW.EXAMPLE.COM:443"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded.Admin.PublicOrigins,
		[]string{"https://old.example.com", "https://new.example.com"}) {
		t.Fatalf("unexpected origins %#v", loaded.Admin.PublicOrigins)
	}
	stateLock, err := store.AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if err := runAdminOrigin([]string{"add", "https://blocked.example.com"}); err == nil ||
		!strings.Contains(err.Error(), "stop mt-server") {
		t.Fatalf("active service lock was not enforced: %v", err)
	}
	if err := stateLock.Close(); err != nil {
		t.Fatal(err)
	}
	if err := runAdminOrigin([]string{"remove", "https://old.example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := runAdminOrigin([]string{"remove", "https://new.example.com"}); err == nil {
		t.Fatal("proxy mode allowed removal of the last origin")
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(loaded.Admin.PublicOrigins, []string{"https://new.example.com"}) {
		t.Fatalf("failed maintenance changed state: %#v", loaded.Admin.PublicOrigins)
	}
}

func TestRunAdminOriginRejectsInvalidUsage(t *testing.T) {
	for _, arguments := range [][]string{{}, {"unknown"}, {"add"}, {"list", "extra"}} {
		if err := runAdminOrigin(arguments); err == nil {
			t.Fatalf("invalid arguments %#v were accepted", arguments)
		}
	}
}

func TestRunStateRestoresMigrationBackupOffline(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("MT_STATE_DIR", directory)
	store, err := state.NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	current := state.State{SchemaVersion: state.SchemaVersion, UpdatedAt: time.Now().UTC(),
		Cache: state.DefaultCache()}
	if _, err := store.CommitInitial(current); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{
		"schema_version": 3,
		"updated_at":     time.Now().UTC(),
		"admin":          map[string]any{"password": map[string]any{}, "public_origins": []string{}},
		"qweather":       map[string]any{},
		"cache": map[string]any{
			"current_ttl_seconds": 1200, "current_stale_max_seconds": 21600,
			"hourly_ttl_seconds": 3600, "hourly_stale_max_seconds": 43200,
			"daily_ttl_seconds": 14400, "daily_stale_max_seconds": 172800,
			"max_locations": 64,
		},
		"device_tokens": []any{},
	}
	contents, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.v3.backup.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runState([]string{"restore-v3-backup"}); err != nil {
		t.Fatal(err)
	}
	compatible, err := store.LoadCompatible()
	if err != nil || compatible.SourceVersion != 3 {
		t.Fatalf("unexpected restored state %#v %v", compatible, err)
	}
	if err := runState([]string{"restore-v3-backup"}); err == nil {
		t.Fatal("second restore accepted a schema-v3 current state")
	}
	if err := runState([]string{"unknown"}); err == nil {
		t.Fatal("invalid state command was accepted")
	}
}

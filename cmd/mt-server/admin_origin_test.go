package main

import (
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

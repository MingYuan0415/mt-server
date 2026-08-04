package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
	"github.com/MingYuan0415/mt-server/internal/providers/qweather"
)

func TestFixtureIsAValidSchemaV3MigrationCandidate(t *testing.T) {
	contents, err := fixtureBytes()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "state.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := state.NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadCompatible()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SourceVersion != 3 || loaded.State.SchemaVersion != state.SchemaVersion ||
		loaded.State.Cache.AlertsTTL != 600 || loaded.State.Cache.AlertsStaleMax != 3600 {
		t.Fatalf("unexpected migration candidate %#v", loaded)
	}
	if !adminauth.ValidPasswordHash(loaded.State.Admin.Password) {
		t.Fatal("fixture administrator verifier is invalid")
	}
	if _, err := qweather.PublicKeyFingerprint([]byte(loaded.State.QWeather.PrivateKeyPEM)); err != nil {
		t.Fatalf("fixture Ed25519 key is invalid: %v", err)
	}
	if len(loaded.State.DeviceTokens) != 1 {
		t.Fatalf("unexpected fixture device tokens %#v", loaded.State.DeviceTokens)
	}
	token := loaded.State.DeviceTokens[0]
	if _, ok := auth.CredentialFromHex(token.ID, token.Hash); !ok {
		t.Fatal("fixture device token verifier is invalid")
	}
}

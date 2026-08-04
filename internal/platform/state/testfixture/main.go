// Command testfixture emits a valid schema-v3 state for migration tests.
// It is intentionally kept outside the production command and is never
// included in the server image.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/MingYuan0415/mt-server/internal/platform/adminauth"
	"github.com/MingYuan0415/mt-server/internal/platform/auth"
	"github.com/MingYuan0415/mt-server/internal/platform/state"
)

const fixtureOwner = 65532

type cacheV3 struct {
	CurrentTTL      int64 `json:"current_ttl_seconds"`
	CurrentStaleMax int64 `json:"current_stale_max_seconds"`
	HourlyTTL       int64 `json:"hourly_ttl_seconds"`
	HourlyStaleMax  int64 `json:"hourly_stale_max_seconds"`
	DailyTTL        int64 `json:"daily_ttl_seconds"`
	DailyStaleMax   int64 `json:"daily_stale_max_seconds"`
	MaxLocations    int   `json:"max_locations"`
}

type fixtureV3 struct {
	SchemaVersion int                 `json:"schema_version"`
	UpdatedAt     time.Time           `json:"updated_at"`
	Admin         state.AdminState    `json:"admin"`
	QWeather      state.QWeatherState `json:"qweather"`
	Cache         cacheV3             `json:"cache"`
	DeviceTokens  []state.DeviceToken `json:"device_tokens"`
}

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = writeFixture(os.Stdout)
	case len(os.Args) == 3 && os.Args[1] == "write-volume":
		err = writeVolume(os.Args[2])
	case len(os.Args) == 3 && os.Args[1] == "verify-migrated":
		err = verifyMigrated(os.Args[2])
	case len(os.Args) == 3 && os.Args[1] == "verify-restored":
		err = verifyRestored(os.Args[2])
	default:
		err = fmt.Errorf("usage: testfixture [write-volume|verify-migrated|verify-restored DIRECTORY]")
	}
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func writeFixture(output *os.File) error {
	contents, err := fixtureBytes()
	if err != nil {
		return err
	}
	_, err = output.Write(contents)
	return err
}

func fixtureBytes() ([]byte, error) {
	fixture, err := newFixture()
	if err != nil {
		return nil, err
	}
	var contents bytes.Buffer
	encoder := json.NewEncoder(&contents)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(fixture); err != nil {
		return nil, err
	}
	return contents.Bytes(), nil
}

func writeVolume(directory string) error {
	contents, err := fixtureBytes()
	if err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return fmt.Errorf("protect fixture directory: %w", err)
	}
	if err := os.Chown(directory, fixtureOwner, fixtureOwner); err != nil {
		return fmt.Errorf("own fixture directory: %w", err)
	}
	for _, name := range []string{"state.json", "original-v3.json"} {
		if err := writePrivate(filepath.Join(directory, name), contents); err != nil {
			return err
		}
	}
	return nil
}

func writePrivate(path string, contents []byte) error {
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("protect %s: %w", filepath.Base(path), err)
	}
	if err := os.Chown(path, fixtureOwner, fixtureOwner); err != nil {
		return fmt.Errorf("own %s: %w", filepath.Base(path), err)
	}
	return nil
}

func verifyMigrated(directory string) error {
	current, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		return err
	}
	var value state.State
	if err := json.Unmarshal(current, &value); err != nil {
		return fmt.Errorf("decode migrated state: %w", err)
	}
	if value.SchemaVersion != state.SchemaVersion || value.Cache.AlertsTTL != 600 ||
		value.Cache.AlertsStaleMax != 3600 {
		return fmt.Errorf("unexpected migrated schema or alert cache defaults")
	}
	original, err := os.ReadFile(filepath.Join(directory, "original-v3.json"))
	if err != nil {
		return err
	}
	backup, err := os.ReadFile(filepath.Join(directory, "state.v3.backup.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(original, backup) {
		return fmt.Errorf("schema-v3 migration backup differs from its source")
	}
	for _, name := range []string{"state.json", "state.v3.backup.json", "original-v3.json"} {
		if err := verifyPrivate(filepath.Join(directory, name)); err != nil {
			return err
		}
	}
	return nil
}

func verifyRestored(directory string) error {
	current, err := os.ReadFile(filepath.Join(directory, "state.json"))
	if err != nil {
		return err
	}
	original, err := os.ReadFile(filepath.Join(directory, "original-v3.json"))
	if err != nil {
		return err
	}
	if !bytes.Equal(original, current) {
		return fmt.Errorf("restored state differs from the original schema-v3 fixture")
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(current, &header); err != nil || header.SchemaVersion != 3 {
		return fmt.Errorf("restored state is not schema version 3")
	}
	return verifyPrivate(filepath.Join(directory, "state.json"))
}

func verifyPrivate(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || info.Mode().Perm() != 0o600 || stat.Uid != fixtureOwner || stat.Gid != fixtureOwner {
		return fmt.Errorf("%s must be mode 0600 and owned by 65532:65532", filepath.Base(path))
	}
	return nil
}

func newFixture() (fixtureV3, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fixtureV3{}, fmt.Errorf("generate Ed25519 key: %w", err)
	}
	encodedKey, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fixtureV3{}, fmt.Errorf("encode Ed25519 key: %w", err)
	}
	password, err := adminauth.HashPassword("fixture administrator password")
	if err != nil {
		return fixtureV3{}, fmt.Errorf("generate Argon2id verifier: %w", err)
	}
	rawToken := make([]byte, 32)
	if _, err := rand.Read(rawToken); err != nil {
		return fixtureV3{}, fmt.Errorf("generate device token: %w", err)
	}
	defaults := state.DefaultCache()
	return fixtureV3{
		SchemaVersion: 3,
		UpdatedAt:     time.Now().UTC().Truncate(time.Second),
		Admin: state.AdminState{
			Password:      password,
			PublicOrigins: []string{"https://admin.example.test"},
		},
		QWeather: state.QWeatherState{
			APIHost:   "account.example.qweatherapi.com",
			ProjectID: "fixture-project", CredentialID: "fixture-credential",
			PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encodedKey})),
			Language:      "zh", Unit: "m", RequestTimeoutSeconds: 10, CircuitCooldownSeconds: 900,
		},
		Cache: cacheV3{
			CurrentTTL: defaults.CurrentTTL, CurrentStaleMax: defaults.CurrentStaleMax,
			HourlyTTL: defaults.HourlyTTL, HourlyStaleMax: defaults.HourlyStaleMax,
			DailyTTL: defaults.DailyTTL, DailyStaleMax: defaults.DailyStaleMax,
			MaxLocations: defaults.MaxLocations,
		},
		DeviceTokens: []state.DeviceToken{{
			ID: "device_fixture", Name: "Fixture Device", Hash: auth.HashToken(string(rawToken)),
			CreatedAt: time.Now().UTC().Truncate(time.Second),
		}},
	}, nil
}

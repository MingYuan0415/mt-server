package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreInitialCommitAndRestart(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected uninitialized store, got %v", err)
	}
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	value := minimalState(now)
	if _, err := store.CommitInitial(value); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "bootstrap.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initialization created a bootstrap record: %v", err)
	}
	info, err := os.Stat(filepath.Join(directory, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected state permissions %o", info.Mode().Perm())
	}

	reopened, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SchemaVersion != SchemaVersion || loaded.QWeather.APIHost != value.QWeather.APIHost {
		t.Fatalf("unexpected reloaded state %#v", loaded)
	}
}

func TestStoreConcurrentInitialCommitHasOneWinner(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var workers sync.WaitGroup
	for index := 0; index < 2; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			_, err := store.CommitInitial(minimalState(time.Now().UTC()))
			results <- err
		}()
	}
	close(start)
	workers.Wait()
	close(results)
	successes := 0
	alreadyInitialized := 0
	for result := range results {
		switch {
		case result == nil:
			successes++
		case errors.Is(result, ErrAlreadyInitialized):
			alreadyInitialized++
		default:
			t.Fatalf("unexpected initial commit result: %v", result)
		}
	}
	if successes != 1 || alreadyInitialized != 1 {
		t.Fatalf("unexpected commit results: %d success, %d already initialized",
			successes, alreadyInitialized)
	}
}

func TestStateDirectoryLockIsExclusive(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.AcquireLock()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcquireLock(); !errors.Is(err, ErrLocked) {
		t.Fatalf("expected exclusive lock rejection, got %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := store.AcquireLock()
	if err != nil {
		t.Fatalf("released lock could not be reacquired: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStoreRejectsCorruptState(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte(`{"schema_version":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "decode application state") {
		t.Fatalf("expected corrupt-state error, got %v", err)
	}
}

func TestStoreRejectsUnknownSchema(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"),
		[]byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("expected unsupported schema error, got %v", err)
	}
}

func TestStoreRejectsPreviousLocationSchemaBeforeUnknownFields(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	legacy := `{"schema_version":1,"location":{"mode":"trusted_headers"}}`
	if err := os.WriteFile(filepath.Join(directory, "state.json"), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil ||
		!strings.Contains(err.Error(), "unsupported state schema version 1") {
		t.Fatalf("expected explicit previous-schema error, got %v", err)
	}
}

func TestStoreRejectsSchemaV2WithUpgradeGuidance(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"),
		[]byte(`{"schema_version":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "back up and clear") {
		t.Fatalf("expected explicit v2 upgrade guidance, got %v", err)
	}
}

func TestStoreMigratesV3AndRestoresBackup(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	original := minimalStateV3(time.Date(2026, 8, 3, 1, 2, 3, 0, time.UTC))
	writeV3State(t, directory, original)
	loaded, err := store.LoadCompatible()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.SourceVersion != 3 || loaded.State.SchemaVersion != SchemaVersion ||
		loaded.State.Cache.AlertsTTL != int64((10*time.Minute)/time.Second) ||
		loaded.State.Cache.AlertsStaleMax != int64(time.Hour/time.Second) {
		t.Fatalf("unexpected migration candidate %#v", loaded)
	}
	result, err := store.CommitMigration(loaded)
	if err != nil || !result.DurabilityConfirmed {
		t.Fatalf("migration failed: %#v %v", result, err)
	}
	current, err := store.Load()
	if err != nil || current.SchemaVersion != SchemaVersion ||
		current.QWeather.APIHost != original.QWeather.APIHost {
		t.Fatalf("unexpected migrated state %#v %v", current, err)
	}
	backupPath := filepath.Join(directory, migrationBackupName)
	info, err := os.Stat(backupPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected migration backup %#v %v", info, err)
	}
	result, err = store.RestoreV3Backup()
	if err != nil || !result.DurabilityConfirmed {
		t.Fatalf("restore failed: %#v %v", result, err)
	}
	if _, err := store.Load(); err == nil || !strings.Contains(err.Error(), "version 3") {
		t.Fatalf("restored v3 state was unexpectedly current: %v", err)
	}
	restored, err := store.LoadCompatible()
	if err != nil || restored.SourceVersion != 3 ||
		restored.State.QWeather.APIHost != original.QWeather.APIHost {
		t.Fatalf("unexpected restored state %#v %v", restored, err)
	}
}

func TestStoreMigrationFailurePreservesV3State(t *testing.T) {
	for _, test := range []struct {
		name   string
		inject func(*Store)
	}{
		{name: "backup sync", inject: func(store *Store) {
			store.syncDirectory = func(string) error { return errors.New("backup sync failed") }
		}},
		{name: "state rename", inject: func(store *Store) {
			store.rename = func(oldPath, newPath string) error {
				if filepath.Base(newPath) == "state.json" {
					return errors.New("state rename failed")
				}
				return os.Rename(oldPath, newPath)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := t.TempDir()
			store, err := NewStore(directory)
			if err != nil {
				t.Fatal(err)
			}
			writeV3State(t, directory, minimalStateV3(time.Now().UTC()))
			loaded, err := store.LoadCompatible()
			if err != nil {
				t.Fatal(err)
			}
			test.inject(store)
			if _, err := store.CommitMigration(loaded); err == nil {
				t.Fatal("expected migration failure")
			}
			contents, err := os.ReadFile(filepath.Join(directory, "state.json"))
			if err != nil {
				t.Fatal(err)
			}
			version, err := schemaVersion(contents)
			if err != nil || version != 3 {
				t.Fatalf("failed migration changed active state: version=%d err=%v", version, err)
			}
		})
	}
}

func TestStoreMigrationFinalDirectorySyncIsLogicalCommit(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	writeV3State(t, directory, minimalStateV3(time.Now().UTC()))
	loaded, err := store.LoadCompatible()
	if err != nil {
		t.Fatal(err)
	}
	var syncs int
	store.syncDirectory = func(path string) error {
		syncs++
		if syncs == 2 {
			return errors.New("final directory sync failed")
		}
		return syncDirectory(path)
	}
	result, err := store.CommitMigration(loaded)
	if err != nil || result.DurabilityConfirmed || result.DurabilityWarning == nil {
		t.Fatalf("unexpected migration result %#v %v", result, err)
	}
	if current, err := store.Load(); err != nil || current.SchemaVersion != SchemaVersion {
		t.Fatalf("logical migration commit is not visible: %#v %v", current, err)
	}
}

func TestStoreMigrationRejectsChangedCandidateAndInvalidRestore(t *testing.T) {
	directory := t.TempDir()
	store, err := NewStore(directory)
	if err != nil {
		t.Fatal(err)
	}
	original := minimalStateV3(time.Now().UTC())
	writeV3State(t, directory, original)
	loaded, err := store.LoadCompatible()
	if err != nil {
		t.Fatal(err)
	}
	original.QWeather.APIHost = "changed.re.qweatherapi.com"
	writeV3State(t, directory, original)
	if _, err := store.CommitMigration(loaded); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed state was migrated: %v", err)
	}
	if _, err := store.RestoreV3Backup(); err == nil {
		t.Fatal("restore accepted a non-v4 current state")
	}
}

func TestStoreSaveIsAtomicAndRequiresInitialization(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(minimalState(time.Now())); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected uninitialized error, got %v", err)
	}
	now := time.Now().UTC()
	value := minimalState(now)
	if _, err := store.CommitInitial(value); err != nil {
		t.Fatal(err)
	}
	value.QWeather.APIHost = "second.re.qweatherapi.com"
	if _, err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.QWeather.APIHost != value.QWeather.APIHost {
		t.Fatalf("save did not replace state: %#v %v", loaded, err)
	}
}

func TestStoreWriteCommitBoundaryAndDurabilityRecovery(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	original := minimalState(time.Now().UTC())
	if _, err := store.CommitInitial(original); err != nil {
		t.Fatal(err)
	}

	candidate := original
	candidate.QWeather.APIHost = "candidate.re.qweatherapi.com"
	store.createTemporary = func(string, string) (*os.File, error) {
		return nil, errors.New("injected temporary-file failure")
	}
	if _, err := store.Save(candidate); err == nil {
		t.Fatal("expected temporary-file failure")
	}
	store.createTemporary = os.CreateTemp
	store.syncFile = func(*os.File) error { return errors.New("injected file sync failure") }
	if _, err := store.Save(candidate); err == nil {
		t.Fatal("expected pre-commit file sync failure")
	}
	store.syncFile = func(file *os.File) error { return file.Sync() }
	store.rename = func(string, string) error { return errors.New("injected rename failure") }
	if _, err := store.Save(candidate); err == nil {
		t.Fatal("expected pre-commit rename failure")
	}
	loaded, err := store.Load()
	if err != nil || loaded.QWeather.APIHost != original.QWeather.APIHost {
		t.Fatalf("pre-commit failure changed state: %#v %v", loaded, err)
	}

	store.rename = os.Rename
	store.syncDirectory = func(string) error { return errors.New("injected directory sync failure") }
	result, err := store.Save(candidate)
	if err != nil || result.DurabilityConfirmed || result.DurabilityWarning == nil {
		t.Fatalf("unexpected post-commit result %#v %v", result, err)
	}
	if store.DurabilityConfirmed() {
		t.Fatal("durability warning was not retained")
	}
	loaded, err = store.Load()
	if err != nil || loaded.QWeather.APIHost != candidate.QWeather.APIHost {
		t.Fatalf("post-commit state was not visible: %#v %v", loaded, err)
	}

	store.syncDirectory = syncDirectory
	candidate.QWeather.APIHost = "confirmed.re.qweatherapi.com"
	result, err = store.Save(candidate)
	if err != nil || !result.DurabilityConfirmed || !store.DurabilityConfirmed() {
		t.Fatalf("successful sync did not clear warning: %#v %v", result, err)
	}
}

func minimalState(now time.Time) State {
	return State{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     now,
		Admin: AdminState{
			Password:      PasswordHash{Algorithm: "test"},
			PublicOrigins: []string{"https://admin.example.com"},
		},
		QWeather:     QWeatherState{APIHost: "account.re.qweatherapi.com"},
		Cache:        DefaultCache(),
		DeviceTokens: []DeviceToken{{ID: "device", Name: "Device", Hash: strings.Repeat("0", 64)}},
	}
}

func minimalStateV3(now time.Time) stateV3 {
	defaults := DefaultCache()
	return stateV3{
		SchemaVersion: 3,
		UpdatedAt:     now,
		Admin: AdminState{
			Password:      PasswordHash{Algorithm: "test"},
			PublicOrigins: []string{"https://admin.example.com"},
		},
		QWeather: QWeatherState{APIHost: "account.re.qweatherapi.com"},
		Cache: cacheStateV3{
			CurrentTTL: defaults.CurrentTTL, CurrentStaleMax: defaults.CurrentStaleMax,
			HourlyTTL: defaults.HourlyTTL, HourlyStaleMax: defaults.HourlyStaleMax,
			DailyTTL: defaults.DailyTTL, DailyStaleMax: defaults.DailyStaleMax,
			MaxLocations: defaults.MaxLocations,
		},
		DeviceTokens: []DeviceToken{{ID: "device", Name: "Device", Hash: strings.Repeat("0", 64)}},
	}
}

func writeV3State(t *testing.T, directory string, value stateV3) {
	t.Helper()
	contents, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "state.json"), contents, 0o600); err != nil {
		t.Fatal(err)
	}
}

package state

import (
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
	if err := store.CommitInitial(value); err != nil {
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
			results <- store.CommitInitial(minimalState(time.Now().UTC()))
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

func TestStoreSaveIsAtomicAndRequiresInitialization(t *testing.T) {
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(minimalState(time.Now())); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("expected uninitialized error, got %v", err)
	}
	now := time.Now().UTC()
	value := minimalState(now)
	if err := store.CommitInitial(value); err != nil {
		t.Fatal(err)
	}
	value.QWeather.APIHost = "second.re.qweatherapi.com"
	if err := store.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || loaded.QWeather.APIHost != value.QWeather.APIHost {
		t.Fatalf("save did not replace state: %#v %v", loaded, err)
	}
}

func minimalState(now time.Time) State {
	return State{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     now,
		Admin:         AdminState{Password: PasswordHash{Algorithm: "test"}},
		QWeather:      QWeatherState{APIHost: "account.re.qweatherapi.com"},
		Cache:         DefaultCache(),
		DeviceTokens:  []DeviceToken{{ID: "device", Name: "Device", Hash: strings.Repeat("0", 64)}},
	}
}

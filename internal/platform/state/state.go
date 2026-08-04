// Package state persists versioned application configuration.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// SchemaVersion is the current on-disk format.
	SchemaVersion       = 4
	previousSchema      = 3
	maximumSize         = 1024 * 1024
	migrationBackupName = "state.v3.backup.json"
)

var (
	// ErrNotInitialized means no application state exists yet.
	ErrNotInitialized = errors.New("application state is not initialized")
	// ErrAlreadyInitialized means setup has already completed.
	ErrAlreadyInitialized = errors.New("application state is already initialized")
)

// State is the complete persistent application configuration.
type State struct {
	SchemaVersion int           `json:"schema_version"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Admin         AdminState    `json:"admin"`
	QWeather      QWeatherState `json:"qweather"`
	Cache         CacheState    `json:"cache"`
	DeviceTokens  []DeviceToken `json:"device_tokens"`
}

// AdminState contains management authentication and browser trust settings.
type AdminState struct {
	Password      PasswordHash `json:"password"`
	PublicOrigins []string     `json:"public_origins"`
}

// PasswordHash records an Argon2id verifier and its parameters.
type PasswordHash struct {
	Algorithm   string `json:"algorithm"`
	Salt        string `json:"salt"`
	Digest      string `json:"digest"`
	MemoryKiB   uint32 `json:"memory_kib"`
	Iterations  uint32 `json:"iterations"`
	Parallelism uint8  `json:"parallelism"`
}

// QWeatherState contains the configured QWeather credential.
type QWeatherState struct {
	APIHost                string `json:"api_host"`
	ProjectID              string `json:"project_id"`
	CredentialID           string `json:"credential_id"`
	PrivateKeyPEM          string `json:"private_key_pem"`
	Language               string `json:"language"`
	Unit                   string `json:"unit"`
	RequestTimeoutSeconds  int64  `json:"request_timeout_seconds"`
	CircuitCooldownSeconds int64  `json:"circuit_cooldown_seconds"`
}

// CacheState contains cache durations in seconds and the location bound.
type CacheState struct {
	CurrentTTL      int64 `json:"current_ttl_seconds"`
	CurrentStaleMax int64 `json:"current_stale_max_seconds"`
	HourlyTTL       int64 `json:"hourly_ttl_seconds"`
	HourlyStaleMax  int64 `json:"hourly_stale_max_seconds"`
	DailyTTL        int64 `json:"daily_ttl_seconds"`
	DailyStaleMax   int64 `json:"daily_stale_max_seconds"`
	AlertsTTL       int64 `json:"alerts_ttl_seconds"`
	AlertsStaleMax  int64 `json:"alerts_stale_max_seconds"`
	MaxLocations    int   `json:"max_locations"`
}

// DeviceToken stores a named token verifier. The raw token is never persisted.
type DeviceToken struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Hash      string    `json:"hash"`
	CreatedAt time.Time `json:"created_at"`
}

// DefaultCache returns the v1 weather cache policy.
func DefaultCache() CacheState {
	return CacheState{
		CurrentTTL:      int64((20 * time.Minute) / time.Second),
		CurrentStaleMax: int64((6 * time.Hour) / time.Second),
		HourlyTTL:       int64(time.Hour / time.Second),
		HourlyStaleMax:  int64((12 * time.Hour) / time.Second),
		DailyTTL:        int64((4 * time.Hour) / time.Second),
		DailyStaleMax:   int64((48 * time.Hour) / time.Second),
		AlertsTTL:       int64((10 * time.Minute) / time.Second),
		AlertsStaleMax:  int64(time.Hour / time.Second),
		MaxLocations:    64,
	}
}

// CompatibleState is a validated JSON shape that may still require an atomic
// on-disk migration before its runtime can be activated.
type CompatibleState struct {
	State         State
	SourceVersion int
	source        []byte
}

type stateV3 struct {
	SchemaVersion int           `json:"schema_version"`
	UpdatedAt     time.Time     `json:"updated_at"`
	Admin         AdminState    `json:"admin"`
	QWeather      QWeatherState `json:"qweather"`
	Cache         cacheStateV3  `json:"cache"`
	DeviceTokens  []DeviceToken `json:"device_tokens"`
}

type cacheStateV3 struct {
	CurrentTTL      int64 `json:"current_ttl_seconds"`
	CurrentStaleMax int64 `json:"current_stale_max_seconds"`
	HourlyTTL       int64 `json:"hourly_ttl_seconds"`
	HourlyStaleMax  int64 `json:"hourly_stale_max_seconds"`
	DailyTTL        int64 `json:"daily_ttl_seconds"`
	DailyStaleMax   int64 `json:"daily_stale_max_seconds"`
	MaxLocations    int   `json:"max_locations"`
}

// Store owns the private application state directory.
type Store struct {
	directory           string
	statePath           string
	mu                  sync.Mutex
	createTemporary     func(string, string) (*os.File, error)
	syncFile            func(*os.File) error
	rename              func(string, string) error
	syncDirectory       func(string) error
	durabilityConfirmed bool
}

// WriteResult describes a logically committed state replacement.
type WriteResult struct {
	DurabilityConfirmed bool
	DurabilityWarning   error
}

// NewStore opens or creates a private state directory.
func NewStore(directory string) (*Store, error) {
	if !filepath.IsAbs(directory) {
		return nil, errors.New("state directory must be absolute")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, fmt.Errorf("protect state directory: %w", err)
	}
	return &Store{
		directory:           directory,
		statePath:           filepath.Join(directory, "state.json"),
		createTemporary:     os.CreateTemp,
		syncFile:            func(file *os.File) error { return file.Sync() },
		rename:              os.Rename,
		syncDirectory:       syncDirectory,
		durabilityConfirmed: true,
	}, nil
}

// Load reads a complete state snapshot.
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// LoadCompatible reads the current state or converts schema v3 in memory. A
// converted state must be passed to CommitMigration before it is activated.
func (s *Store) LoadCompatible() (CompatibleState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	contents, err := s.readStateLocked()
	if err != nil {
		return CompatibleState{}, err
	}
	version, err := schemaVersion(contents)
	if err != nil {
		return CompatibleState{}, err
	}
	switch version {
	case SchemaVersion:
		value, err := decodeState(contents)
		return CompatibleState{State: value, SourceVersion: version, source: contents}, err
	case previousSchema:
		value, err := decodeStateV3(contents)
		if err != nil {
			return CompatibleState{}, err
		}
		return CompatibleState{State: migrateV3(value), SourceVersion: version, source: contents}, nil
	case 2:
		return CompatibleState{}, errors.New("unsupported state schema version 2; back up and clear the v0.1 state before setup")
	default:
		return CompatibleState{}, fmt.Errorf("unsupported state schema version %d", version)
	}
}

// CommitMigration saves the original v3 state as a private backup before
// atomically replacing the active file with schema v4.
func (s *Store) CommitMigration(loaded CompatibleState) (WriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if loaded.SourceVersion != previousSchema || loaded.State.SchemaVersion != SchemaVersion ||
		len(loaded.source) == 0 {
		return WriteResult{}, errors.New("state migration candidate is invalid")
	}
	current, err := readBounded(s.statePath)
	if err != nil {
		return WriteResult{}, fmt.Errorf("read application state before migration: %w", err)
	}
	if !bytes.Equal(current, loaded.source) {
		return WriteResult{}, errors.New("application state changed before migration")
	}
	backupPath := filepath.Join(s.directory, migrationBackupName)
	backupResult, err := s.atomicWriteContents(backupPath, loaded.source)
	if err != nil {
		return WriteResult{}, fmt.Errorf("write migration backup: %w", err)
	}
	if !backupResult.DurabilityConfirmed {
		return WriteResult{}, fmt.Errorf("migration backup durability is unconfirmed: %w",
			backupResult.DurabilityWarning)
	}
	return s.writeStateLocked(loaded.State)
}

// RestoreV3Backup atomically restores the private schema-v3 migration backup.
// The caller must hold the process-wide state directory lock.
func (s *Store) RestoreV3Backup() (WriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := readBounded(s.statePath)
	if err != nil {
		return WriteResult{}, fmt.Errorf("read current application state: %w", err)
	}
	version, err := schemaVersion(current)
	if err != nil {
		return WriteResult{}, err
	}
	if version != SchemaVersion {
		return WriteResult{}, fmt.Errorf("current state schema must be %d", SchemaVersion)
	}
	backup, err := readBounded(filepath.Join(s.directory, migrationBackupName))
	if err != nil {
		return WriteResult{}, fmt.Errorf("read schema v3 migration backup: %w", err)
	}
	backupVersion, err := schemaVersion(backup)
	if err != nil {
		return WriteResult{}, err
	}
	if backupVersion != previousSchema {
		return WriteResult{}, errors.New("migration backup is not schema version 3")
	}
	if _, err := decodeStateV3(backup); err != nil {
		return WriteResult{}, err
	}
	return s.atomicWriteContents(s.statePath, backup)
}

// Save atomically replaces initialized state.
func (s *Store) Save(value State) (WriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.statePath); errors.Is(err, os.ErrNotExist) {
		return WriteResult{}, ErrNotInitialized
	} else if err != nil {
		return WriteResult{}, err
	}
	return s.writeStateLocked(value)
}

// CommitInitial creates the state for the first successful initialization.
// The state-file existence check and write are serialized so concurrent setup
// submissions cannot both initialize the service.
func (s *Store) CommitInitial(value State) (WriteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.statePath); err == nil {
		return WriteResult{}, ErrAlreadyInitialized
	} else if !errors.Is(err, os.ErrNotExist) {
		return WriteResult{}, err
	}
	return s.writeStateLocked(value)
}

// DurabilityConfirmed reports whether the most recent committed write completed directory sync.
func (s *Store) DurabilityConfirmed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.durabilityConfirmed
}

// SyncDirectoryForTest injects directory synchronization behavior for tests in
// other internal packages.
func (s *Store) SyncDirectoryForTest(sync func(string) error) {
	s.mu.Lock()
	s.syncDirectory = sync
	s.mu.Unlock()
}

// RenameForTest injects the atomic replacement operation for tests in other
// internal packages.
func (s *Store) RenameForTest(rename func(string, string) error) {
	s.mu.Lock()
	s.rename = rename
	s.mu.Unlock()
}

func (s *Store) loadLocked() (State, error) {
	contents, err := s.readStateLocked()
	if err != nil {
		return State{}, err
	}
	version, err := schemaVersion(contents)
	if err != nil {
		return State{}, err
	}
	if version != SchemaVersion {
		if version == 2 {
			return State{}, errors.New("unsupported state schema version 2; back up and clear the v0.1 state before setup")
		}
		return State{}, fmt.Errorf("unsupported state schema version %d", version)
	}
	return decodeState(contents)
}

func (s *Store) readStateLocked() ([]byte, error) {
	contents, err := readBounded(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotInitialized
	}
	if err != nil {
		return nil, fmt.Errorf("read application state: %w", err)
	}
	return contents, nil
}

func schemaVersion(contents []byte) (int, error) {
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(contents, &header); err != nil {
		return 0, fmt.Errorf("decode application state: %w", err)
	}
	return header.SchemaVersion, nil
}

func decodeState(contents []byte) (State, error) {
	var value State
	if err := decodeStrict(contents, &value); err != nil {
		return State{}, err
	}
	return value, nil
}

func decodeStateV3(contents []byte) (stateV3, error) {
	var value stateV3
	if err := decodeStrict(contents, &value); err != nil {
		return stateV3{}, err
	}
	return value, nil
}

func decodeStrict(contents []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("decode application state: %w", err)
	}
	return ensureJSONEOF(decoder)
}

func migrateV3(value stateV3) State {
	defaults := DefaultCache()
	return State{
		SchemaVersion: SchemaVersion,
		UpdatedAt:     value.UpdatedAt,
		Admin:         value.Admin,
		QWeather:      value.QWeather,
		Cache: CacheState{
			CurrentTTL: value.Cache.CurrentTTL, CurrentStaleMax: value.Cache.CurrentStaleMax,
			HourlyTTL: value.Cache.HourlyTTL, HourlyStaleMax: value.Cache.HourlyStaleMax,
			DailyTTL: value.Cache.DailyTTL, DailyStaleMax: value.Cache.DailyStaleMax,
			AlertsTTL: defaults.AlertsTTL, AlertsStaleMax: defaults.AlertsStaleMax,
			MaxLocations: value.Cache.MaxLocations,
		},
		DeviceTokens: value.DeviceTokens,
	}
}

func (s *Store) writeStateLocked(value State) (WriteResult, error) {
	if value.SchemaVersion != SchemaVersion {
		return WriteResult{}, fmt.Errorf("state schema version must be %d", SchemaVersion)
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = time.Now().UTC()
	}
	return s.atomicWrite(s.statePath, value)
}

func (s *Store) atomicWrite(path string, value any) (WriteResult, error) {
	var contents bytes.Buffer
	encoder := json.NewEncoder(&contents)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return WriteResult{}, err
	}
	if contents.Len() > maximumSize {
		return WriteResult{}, errors.New("state exceeds 1 MiB")
	}
	return s.atomicWriteContents(path, contents.Bytes())
}

func (s *Store) atomicWriteContents(path string, contents []byte) (WriteResult, error) {
	if len(contents) > maximumSize {
		return WriteResult{}, errors.New("state exceeds 1 MiB")
	}
	temporary, err := s.createTemporary(s.directory, ".mt-server-*")
	if err != nil {
		return WriteResult{}, fmt.Errorf("create temporary state: %w", err)
	}
	temporaryPath := temporary.Name()
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return WriteResult{}, err
	}
	if _, err := temporary.Write(contents); err != nil {
		return WriteResult{}, err
	}
	if err := s.syncFile(temporary); err != nil {
		return WriteResult{}, err
	}
	if err := temporary.Close(); err != nil {
		return WriteResult{}, err
	}
	if err := s.rename(temporaryPath, path); err != nil {
		return WriteResult{}, fmt.Errorf("replace state: %w", err)
	}
	removeTemporary = false
	if err := s.syncDirectory(s.directory); err != nil {
		s.durabilityConfirmed = false
		return WriteResult{DurabilityConfirmed: false, DurabilityWarning: err}, nil
	}
	s.durabilityConfirmed = true
	return WriteResult{DurabilityConfirmed: true}, nil
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	contents, err := io.ReadAll(io.LimitReader(file, maximumSize+1))
	if err != nil {
		return nil, err
	}
	if len(contents) > maximumSize {
		return nil, errors.New("state exceeds 1 MiB")
	}
	return contents, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("application state contains trailing JSON")
		}
		return fmt.Errorf("decode application state: %w", err)
	}
	return nil
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

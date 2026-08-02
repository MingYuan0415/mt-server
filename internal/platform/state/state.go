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
	SchemaVersion = 2
	maximumSize   = 1024 * 1024
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

// AdminState contains the password verifier only.
type AdminState struct {
	Password PasswordHash `json:"password"`
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
		MaxLocations:    64,
	}
}

// Store owns the private application state directory.
type Store struct {
	directory string
	statePath string
	mu        sync.Mutex
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
		directory: directory,
		statePath: filepath.Join(directory, "state.json"),
	}, nil
}

// Load reads a complete state snapshot.
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked()
}

// Save atomically replaces initialized state.
func (s *Store) Save(value State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.statePath); errors.Is(err, os.ErrNotExist) {
		return ErrNotInitialized
	} else if err != nil {
		return err
	}
	return s.writeStateLocked(value)
}

// CommitInitial creates the state for the first successful initialization.
// The state-file existence check and write are serialized so concurrent setup
// submissions cannot both initialize the service.
func (s *Store) CommitInitial(value State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.statePath); err == nil {
		return ErrAlreadyInitialized
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := s.writeStateLocked(value); err != nil {
		return err
	}
	return nil
}

func (s *Store) loadLocked() (State, error) {
	contents, err := readBounded(s.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotInitialized
	}
	if err != nil {
		return State{}, fmt.Errorf("read application state: %w", err)
	}
	var header struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(contents, &header); err != nil {
		return State{}, fmt.Errorf("decode application state: %w", err)
	}
	if header.SchemaVersion != SchemaVersion {
		return State{}, fmt.Errorf("unsupported state schema version %d", header.SchemaVersion)
	}
	var value State
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return State{}, fmt.Errorf("decode application state: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return State{}, err
	}
	return value, nil
}

func (s *Store) writeStateLocked(value State) error {
	if value.SchemaVersion != SchemaVersion {
		return fmt.Errorf("state schema version must be %d", SchemaVersion)
	}
	if value.UpdatedAt.IsZero() {
		value.UpdatedAt = time.Now().UTC()
	}
	return s.atomicWrite(s.statePath, value)
}

func (s *Store) atomicWrite(path string, value any) error {
	var contents bytes.Buffer
	encoder := json.NewEncoder(&contents)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return err
	}
	if contents.Len() > maximumSize {
		return errors.New("state exceeds 1 MiB")
	}
	temporary, err := os.CreateTemp(s.directory, ".mt-server-*")
	if err != nil {
		return fmt.Errorf("create temporary state: %w", err)
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
		return err
	}
	if _, err := temporary.Write(contents.Bytes()); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	removeTemporary = false
	return syncDirectory(s.directory)
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

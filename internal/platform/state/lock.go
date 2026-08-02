package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/sys/unix"
)

var ErrLocked = errors.New("state directory is already in use")

// Lock holds the process-wide exclusive lease for a state directory.
type Lock struct {
	file *os.File
	once sync.Once
}

// AcquireLock prevents the server and offline maintenance commands from writing concurrently.
func (s *Store) AcquireLock() (*Lock, error) {
	path := filepath.Join(s.directory, "state.lock")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open state lock: %w", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect state lock: %w", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock state directory: %w", err)
	}
	return &Lock{file: file}, nil
}

// Close releases the state-directory lease.
func (l *Lock) Close() error {
	var result error
	l.once.Do(func() {
		if err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN); err != nil {
			result = err
		}
		if err := l.file.Close(); result == nil {
			result = err
		}
	})
	return result
}

// Package adminauth implements management-plane authentication primitives.
package adminauth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"

	"github.com/MingYuan0415/mt-server/internal/platform/state"
	"golang.org/x/crypto/argon2"
)

const (
	passwordMemory      = 32 * 1024
	passwordIterations  = 3
	passwordParallelism = 1
	passwordSaltSize    = 16
	passwordDigestSize  = 32
)

// HashPassword creates an Argon2id password verifier.
func HashPassword(password string) (state.PasswordHash, error) {
	if len(password) < 12 || len(password) > 128 {
		return state.PasswordHash{}, errors.New("password must contain 12-128 bytes")
	}
	salt := make([]byte, passwordSaltSize)
	if _, err := rand.Read(salt); err != nil {
		return state.PasswordHash{}, err
	}
	digest := argon2.IDKey([]byte(password), salt, passwordIterations,
		passwordMemory, passwordParallelism, passwordDigestSize)
	return state.PasswordHash{
		Algorithm:   "argon2id",
		Salt:        base64.RawStdEncoding.EncodeToString(salt),
		Digest:      base64.RawStdEncoding.EncodeToString(digest),
		MemoryKiB:   passwordMemory,
		Iterations:  passwordIterations,
		Parallelism: passwordParallelism,
	}, nil
}

// VerifyPassword compares a password with a stored verifier.
func VerifyPassword(password string, value state.PasswordHash) bool {
	if len(password) > 128 || !ValidPasswordHash(value) {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(value.Salt)
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(value.Digest)
	if err != nil || len(expected) != passwordDigestSize {
		return false
	}
	candidate := argon2.IDKey([]byte(password), salt, value.Iterations,
		value.MemoryKiB, value.Parallelism, uint32(len(expected)))
	return subtle.ConstantTimeCompare(candidate, expected) == 1
}

// ValidPasswordHash validates stored Argon2id parameters and encodings.
func ValidPasswordHash(value state.PasswordHash) bool {
	if value.Algorithm != "argon2id" ||
		value.MemoryKiB < 8*1024 || value.MemoryKiB > 128*1024 ||
		value.Iterations < 1 || value.Iterations > 10 ||
		value.Parallelism < 1 || value.Parallelism > 4 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(value.Salt)
	if err != nil || len(salt) < 8 || len(salt) > 64 {
		return false
	}
	digest, err := base64.RawStdEncoding.DecodeString(value.Digest)
	return err == nil && len(digest) == passwordDigestSize
}

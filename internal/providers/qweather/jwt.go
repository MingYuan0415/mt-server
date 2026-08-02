package qweather

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"time"
)

// PublicKeyFingerprint validates a private key and returns a non-secret
// fingerprint suitable for the management interface.
func PublicKeyFingerprint(privateKeyPEM []byte) (string, error) {
	block, rest := pem.Decode(privateKeyPEM)
	if block == nil || len(rest) != 0 || block.Type != "PRIVATE KEY" {
		return "", errors.New("private key must contain exactly one PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("parse PKCS#8 private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return "", errors.New("private key is not Ed25519")
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(publicKey)
	return base64.RawURLEncoding.EncodeToString(digest[:]), nil
}

type signer struct {
	privateKey   ed25519.PrivateKey
	credentialID string
	projectID    string
	now          func() time.Time
}

func newSigner(privateKeyPEM []byte, credentialID, projectID string) (*signer, error) {
	block, rest := pem.Decode(privateKeyPEM)
	if block == nil || len(rest) != 0 || block.Type != "PRIVATE KEY" {
		return nil, errors.New("private key must contain exactly one PKCS#8 PRIVATE KEY PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse PKCS#8 private key: %w", err)
	}
	privateKey, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("private key is not Ed25519")
	}
	return &signer{
		privateKey:   privateKey,
		credentialID: credentialID,
		projectID:    projectID,
		now:          time.Now,
	}, nil
}

func (s *signer) token() (string, error) {
	now := s.now().UTC()
	header, err := json.Marshal(struct {
		Algorithm    string `json:"alg"`
		CredentialID string `json:"kid"`
	}{Algorithm: "EdDSA", CredentialID: s.credentialID})
	if err != nil {
		return "", err
	}
	claims, err := json.Marshal(struct {
		ProjectID string `json:"sub"`
		IssuedAt  int64  `json:"iat"`
		ExpiresAt int64  `json:"exp"`
	}{
		ProjectID: s.projectID,
		IssuedAt:  now.Add(-30 * time.Second).Unix(),
		ExpiresAt: now.Add(5 * time.Minute).Unix(),
	})
	if err != nil {
		return "", err
	}

	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." +
		base64.RawURLEncoding.EncodeToString(claims)
	signature := ed25519.Sign(s.privateKey, []byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

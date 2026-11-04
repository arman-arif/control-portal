// Package secrets encrypts values the panel stores in its database, using
// AES-256-GCM with the key from KEEL_SECRET_KEY. A sealed value is
// nonce‖ciphertext and goes in a BYTEA column.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

const keySize = 32

// ErrOpen means a sealed value could not be decrypted: it was made with a
// different key, or it has been damaged or changed.
var ErrOpen = errors.New("secrets: cannot decrypt value: wrong key or damaged data")

// Box seals and opens values with one key.
type Box struct {
	aead cipher.AEAD
}

// GenerateKey returns a new random key in the base64 form New accepts.
func GenerateKey() string {
	key := make([]byte, keySize)
	rand.Read(key) // never fails since Go 1.24; it crashes the program instead
	return base64.StdEncoding.EncodeToString(key)
}

// New makes a Box from a base64 key. Its errors never contain the key.
func New(encodedKey string) (*Box, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil {
		return nil, errors.New("key is not valid base64")
	}
	if len(key) != keySize {
		return nil, fmt.Errorf("key must be %d bytes, got %d", keySize, len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal encrypts plaintext under a new random nonce.
func (b *Box) Seal(plaintext []byte) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	rand.Read(nonce)
	return b.aead.Seal(nonce, nonce, plaintext, nil)
}

// Open decrypts a value made by Seal. Any failure returns ErrOpen.
func (b *Box) Open(sealed []byte) ([]byte, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return nil, ErrOpen
	}
	plaintext, err := b.aead.Open(nil, sealed[:n], sealed[n:], nil)
	if err != nil {
		return nil, ErrOpen
	}
	return plaintext, nil
}

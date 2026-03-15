package codegen

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
)

const encryptionKeyBytes = 32

// DeriveKey parses a hex-encoded encryption key and validates AES-256 length.
func DeriveKey(keyHex string) ([]byte, error) {
	key, err := hex.DecodeString(keyHex)
	if err != nil {
		return nil, fmt.Errorf("decode encryption key: %w", err)
	}
	if len(key) != encryptionKeyBytes {
		return nil, fmt.Errorf("invalid key length: got %d bytes, expected %d", len(key), encryptionKeyBytes)
	}
	return key, nil
}

// Encrypt encrypts plaintext using AES-256-GCM and returns nonce+ciphertext.
func Encrypt(plaintext []byte, key []byte) ([]byte, error) {
	if len(key) != encryptionKeyBytes {
		return nil, fmt.Errorf("invalid key length: got %d bytes, expected %d", len(key), encryptionKeyBytes)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ciphertext))
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	return out, nil
}

// Decrypt decrypts nonce-prefixed AES-256-GCM ciphertext.
func Decrypt(ciphertext []byte, key []byte) ([]byte, error) {
	if len(key) != encryptionKeyBytes {
		return nil, fmt.Errorf("invalid key length: got %d bytes, expected %d", len(key), encryptionKeyBytes)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, errors.New("ciphertext too short")
	}

	nonce := ciphertext[:nonceSize]
	encrypted := ciphertext[nonceSize:]

	plaintext, err := gcm.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return nil, fmt.Errorf("decrypt ciphertext: %w", err)
	}

	return plaintext, nil
}

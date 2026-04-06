package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

type Encryptor struct {
	key []byte // 32 bytes
}

func NewEncryptor(hexKey string) (*Encryptor, error) {
	key, err := hex.DecodeString(hexKey)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("HOPSSH_ENCRYPTION_KEY must be 64 hex characters (32 bytes)")
	}
	return &Encryptor{key: key}, nil
}

// Encrypt returns a []byte containing: nonce (12 bytes) || ciphertext
func (e *Encryptor) Encrypt(plaintext string) ([]byte, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

// EncryptBytes encrypts arbitrary binary data.
func (e *Encryptor) EncryptBytes(plaintext []byte) ([]byte, error) {
	return e.Encrypt(string(plaintext))
}

// DecryptBytes decrypts data and returns raw bytes instead of a string.
func (e *Encryptor) DecryptBytes(data []byte) ([]byte, error) {
	s, err := e.Decrypt(data)
	if err != nil {
		return nil, err
	}
	return []byte(s), nil
}

// Decrypt expects: nonce (12 bytes) || ciphertext
func (e *Encryptor) Decrypt(data []byte) (string, error) {
	block, err := aes.NewCipher(e.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", fmt.Errorf("ciphertext too short")
	}
	nonce, ciphertext := data[:gcm.NonceSize()], data[gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decryption failed (wrong key?): %w", err)
	}
	return string(plaintext), nil
}

// GenerateKey creates a random 32-byte encryption key and returns it as hex.
func GenerateKey() (string, error) {
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return hex.EncodeToString(key), nil
}

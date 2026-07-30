package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/browserwing/browserwing/config"
	"github.com/browserwing/browserwing/models"
)

const projectAuthStateEncryptionVersion = 1

// projectAuthStateCipher deliberately supports one active AES-256-GCM key.
// key_id identifies ciphertext provenance only; P4.7.6 does not implement a
// keyring or online rotation. Operators must clear auth states or run a future
// explicit rotation before changing the active key.
type projectAuthStateCipher struct {
	key   []byte
	keyID string
}

func newProjectAuthStateCipher(cfg *config.Config) (*projectAuthStateCipher, error) {
	raw := strings.TrimSpace(os.Getenv("PROJECT_AUTH_STATE_ENCRYPTION_KEY"))
	keyID := strings.TrimSpace(os.Getenv("PROJECT_AUTH_STATE_ENCRYPTION_KEY_ID"))
	if cfg != nil && cfg.Security != nil {
		if raw == "" {
			raw = strings.TrimSpace(cfg.Security.ProjectAuthStateEncryptionKey)
		}
		if keyID == "" {
			keyID = strings.TrimSpace(cfg.Security.ProjectAuthStateEncryptionKeyID)
		}
	}
	if raw == "" || keyID == "" {
		return nil, fmt.Errorf("project auth state encryption key and key id are required")
	}
	key, err := base64.StdEncoding.DecodeString(raw)
	if err != nil || len(key) != 32 {
		return nil, fmt.Errorf("project auth state encryption key must be base64 encoded 32 bytes")
	}
	return &projectAuthStateCipher{key: key, keyID: keyID}, nil
}

func (c *projectAuthStateCipher) encrypt(stateID, projectID uint, plaintext string) (ciphertext, nonce string, err error) {
	if c == nil || len(c.key) != 32 || strings.TrimSpace(c.keyID) == "" {
		return "", "", fmt.Errorf("project auth state encryption is not configured")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", err
	}
	nonceBytes := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonceBytes); err != nil {
		return "", "", err
	}
	sealed := gcm.Seal(nil, nonceBytes, []byte(plaintext), projectAuthStateAAD(stateID, projectID, projectAuthStateEncryptionVersion, c.keyID))
	return base64.StdEncoding.EncodeToString(sealed), base64.StdEncoding.EncodeToString(nonceBytes), nil
}

func (c *projectAuthStateCipher) decrypt(state models.ProjectAuthState) (string, error) {
	if c == nil || len(c.key) != 32 || strings.TrimSpace(c.keyID) == "" {
		return "", fmt.Errorf("project auth state encryption is not configured")
	}
	if state.EncryptionVersion != projectAuthStateEncryptionVersion || state.EncryptionKeyID != c.keyID {
		return "", fmt.Errorf("project auth state encryption key is unavailable")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(state.StateCiphertext)
	if err != nil {
		return "", fmt.Errorf("project auth state ciphertext is invalid")
	}
	nonce, err := base64.StdEncoding.DecodeString(state.StateNonce)
	if err != nil {
		return "", fmt.Errorf("project auth state nonce is invalid")
	}
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, projectAuthStateAAD(state.ID, state.ProjectID, state.EncryptionVersion, state.EncryptionKeyID))
	if err != nil {
		return "", fmt.Errorf("project auth state decrypt failed")
	}
	return string(plain), nil
}

// projectAuthStateAAD uses a length-prefixed binary sequence instead of
// ambiguous string concatenation. The fixed domain separates this ciphertext
// format from all future uses of the same service key.
func projectAuthStateAAD(stateID, projectID uint, version int, keyID string) []byte {
	parts := [][]byte{
		[]byte("project-auth-state"),
		uint64Bytes(uint64(stateID)),
		uint64Bytes(uint64(projectID)),
		uint64Bytes(uint64(version)),
		[]byte(keyID),
	}
	buffer := make([]byte, 0, 64+len(keyID))
	for _, part := range parts {
		length := make([]byte, 4)
		binary.BigEndian.PutUint32(length, uint32(len(part)))
		buffer = append(buffer, length...)
		buffer = append(buffer, part...)
	}
	return buffer
}

func uint64Bytes(value uint64) []byte {
	data := make([]byte, 8)
	binary.BigEndian.PutUint64(data, value)
	return data
}

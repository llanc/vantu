package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/argon2"
)

const encPrefixV1 = "enc:v1:"

// Vault manages AES-256-GCM field encryption and internal HMAC signing.
type Vault struct {
	key [32]byte
}

// NewVaultFromKeyOrFile loads a 32-byte key from rawKey (hex/base64/passphrase) or persists a random key at keyFilePath (0600).
func NewVaultFromKeyOrFile(rawKey, keyFilePath string) (*Vault, error) {
	var key [32]byte
	if strings.TrimSpace(rawKey) != "" {
		decoded, err := parseKeyMaterial(strings.TrimSpace(rawKey))
		if err != nil {
			return nil, err
		}
		key = decoded
		return &Vault{key: key}, nil
	}

	if data, err := os.ReadFile(keyFilePath); err == nil {
		decoded, err := parseKeyMaterial(strings.TrimSpace(string(data)))
		if err == nil {
			return &Vault{key: decoded}, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(keyFilePath), 0o700); err != nil {
		return nil, fmt.Errorf("create key dir: %w", err)
	}
	if _, err := rand.Read(key[:]); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	hexKey := hex.EncodeToString(key[:])
	if err := os.WriteFile(keyFilePath, []byte(hexKey+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("write master key file: %w", err)
	}
	return &Vault{key: key}, nil
}

func parseKeyMaterial(s string) ([32]byte, error) {
	var out [32]byte
	if b, err := hex.DecodeString(s); err == nil && len(b) == 32 {
		copy(out[:], b)
		return out, nil
	}
	if b, err := base64.StdEncoding.DecodeString(s); err == nil && len(b) == 32 {
		copy(out[:], b)
		return out, nil
	}
	// Derive deterministic 256-bit key if user supplied an arbitrary passphrase string.
	sum := sha256.Sum256([]byte("vantu-master-key-v1:" + s))
	return sum, nil
}

// Encrypt encrypts plaintext with AES-256-GCM and returns "enc:v1:<base64>".
func (v *Vault) Encrypt(plaintext string) (string, error) {
	block, err := aes.NewCipher(v.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return encPrefixV1 + base64.StdEncoding.EncodeToString(sealed), nil
}

// Decrypt decrypts an "enc:v1:<base64>" string back to plaintext.
func (v *Vault) Decrypt(encoded string) (string, error) {
	if !strings.HasPrefix(encoded, encPrefixV1) {
		return "", errors.New("unsupported ciphertext format")
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(encoded, encPrefixV1))
	if err != nil {
		return "", fmt.Errorf("decode ciphertext: %w", err)
	}
	block, err := aes.NewCipher(v.key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	ns := gcm.NonceSize()
	if len(raw) < ns {
		return "", errors.New("ciphertext too short")
	}
	plain, err := gcm.Open(nil, raw[:ns], raw[ns:], nil)
	if err != nil {
		return "", fmt.Errorf("decrypt ciphertext: %w", err)
	}
	return string(plain), nil
}

// ComputeRelayKey computes a route-scoped HMAC token used to authenticate Edge Caddy -> Local Relay traffic.
func (v *Vault) ComputeRelayKey(routeID string) string {
	mac := hmac.New(sha256.New, v.key[:])
	mac.Write([]byte("vantu-relay-v1:" + routeID))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifyRelayKey checks the route-scoped HMAC token in constant time.
func (v *Vault) VerifyRelayKey(routeID, provided string) bool {
	expected := v.ComputeRelayKey(routeID)
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

// HashPassword hashes an admin password with Argon2id.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, 2, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=2,p=4$%s$%s",
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword verifies a plaintext password against an Argon2id encoded hash.
func VerifyPassword(password, encodedHash string) bool {
	parts := strings.Split(encodedHash, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 2, 64*1024, 4, uint32(len(expected)))
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

// GenerateAPIToken creates a high-entropy token formatted as "vtu_<8hex>_<32hex>".
func GenerateAPIToken() (fullToken, prefix, sha256Hex string, err error) {
	var p [4]byte
	var s [16]byte
	if _, err = rand.Read(p[:]); err != nil {
		return "", "", "", err
	}
	if _, err = rand.Read(s[:]); err != nil {
		return "", "", "", err
	}
	prefix = "vtu_" + hex.EncodeToString(p[:])
	fullToken = prefix + "_" + hex.EncodeToString(s[:])
	sha256Hex = HashTokenSHA256(fullToken)
	return fullToken, prefix, sha256Hex, nil
}

// HashTokenSHA256 returns hex-encoded SHA-256 of an API or session token.
func HashTokenSHA256(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// GenerateRandomSecret generates a URL-safe hex secret with a custom prefix (e.g. "vtu_edge_...").
func GenerateRandomSecret(prefix string, numBytes int) (string, error) {
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(buf), nil
}

// MaskSecret redacts sensitive secrets for safe display (e.g. "cf_****9a2b").
func MaskSecret(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= 8 {
		return "********"
	}
	return s[:3] + "****" + s[len(s)-4:]
}

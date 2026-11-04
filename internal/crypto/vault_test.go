package crypto_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/llanc/vantu/internal/crypto"
)

func TestVaultEncryptDecryptAndPersistence(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "master.key")

	v1, err := crypto.NewVaultFromKeyOrFile("", keyPath)
	if err != nil {
		t.Fatalf("create vault: %v", err)
	}

	cipherText, err := v1.Encrypt("cf_super_secret_token_12345")
	if err != nil {
		t.Fatalf("encrypt: %v", err)
	}
	if !strings.HasPrefix(cipherText, "enc:v1:") {
		t.Fatalf("unexpected prefix: %s", cipherText)
	}

	// Reload from persisted key file
	v2, err := crypto.NewVaultFromKeyOrFile("", keyPath)
	if err != nil {
		t.Fatalf("reload vault: %v", err)
	}
	plain, err := v2.Decrypt(cipherText)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if plain != "cf_super_secret_token_12345" {
		t.Fatalf("got %q, want cf_super_secret_token_12345", plain)
	}
}

func TestRelayHMACAndArgon2idAndTokens(t *testing.T) {
	v, err := crypto.NewVaultFromKeyOrFile("test-passphrase-for-unit-tests", "")
	if err != nil {
		t.Fatalf("new vault: %v", err)
	}

	sig := v.ComputeRelayKey("01J8ROUTEID")
	if !v.VerifyRelayKey("01J8ROUTEID", sig) {
		t.Fatal("expected valid relay HMAC")
	}
	if v.VerifyRelayKey("01J8OTHER", sig) {
		t.Fatal("expected invalid relay HMAC for different route")
	}

	hash, err := crypto.HashPassword("VantuStrong#2026")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if !crypto.VerifyPassword("VantuStrong#2026", hash) {
		t.Fatal("expected password verification to succeed")
	}
	if crypto.VerifyPassword("wrong-password", hash) {
		t.Fatal("expected wrong password to fail")
	}

	full, prefix, sha, err := crypto.GenerateAPIToken()
	if err != nil {
		t.Fatalf("generate api token: %v", err)
	}
	if !strings.HasPrefix(full, prefix+"_") || crypto.HashTokenSHA256(full) != sha {
		t.Fatalf("unexpected token metadata: full=%s prefix=%s", full, prefix)
	}

	masked := crypto.MaskSecret("cf_live_token_9876")
	if strings.Contains(masked, "live_token") {
		t.Fatalf("secret not masked properly: %s", masked)
	}
}

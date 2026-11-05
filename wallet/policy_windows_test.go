package wallet

import (
	"crypto/ed25519"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsWalletPolicy(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "legacy.key")
	dest := filepath.Join(dir, "encrypted.key")
	if _, err := Create(plain); err == nil || !strings.Contains(err.Error(), "--passphrase-file") {
		t.Fatal(err)
	}
	if _, err := os.Stat(plain); !os.IsNotExist(err) {
		t.Fatal("plaintext creation left a file")
	}
	seed := make([]byte, ed25519.SeedSize)
	data := []byte(hex.EncodeToString(seed) + "\r\n")
	if err := os.WriteFile(plain, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(plain); err == nil {
		t.Fatal("normal plaintext load accepted")
	}
	address, err := EncryptFile(plain, dest, []byte("test password"))
	if err != nil {
		t.Fatal(err)
	}
	key, err := LoadEncrypted(dest, []byte("test password"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := Address(key)
	if err != nil || got != address {
		t.Fatal(got, err)
	}
	after, err := os.ReadFile(plain)
	if err != nil || string(after) != string(data) {
		t.Fatal("migration changed source", err)
	}
	if _, err = ReadSecret(dir, 4096); err == nil {
		t.Fatal("directory accepted")
	}
	if _, err = ReadSecret(plain, 1); err == nil {
		t.Fatal("input limit ignored")
	}
}

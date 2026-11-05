package wallet

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestEncryptedKeyLifecycle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "encrypted.key")
	password := []byte("a long test passphrase")
	address, err := CreateEncrypted(path, password)
	if err != nil {
		t.Fatal(err)
	}
	key, err := LoadEncrypted(path, password)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Address(key)
	if err != nil || got != address {
		t.Fatal(got, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, key.Seed()) {
		t.Fatal("plaintext seed in envelope")
	}
	info, err := os.Stat(path)
	if err != nil || (!RequiresEncryption && info.Mode().Perm() != 0600) {
		t.Fatal(info, err)
	}
	if _, err = LoadEncrypted(path, []byte("wrong")); !errors.Is(err, ErrDecrypt) {
		t.Fatal(err)
	}
	if _, err = CreateEncrypted(path, password); !errors.Is(err, os.ErrExist) {
		t.Fatal(err)
	}
	second := filepath.Join(dir, "second")
	if err = SaveEncrypted(second, key, password); err != nil {
		t.Fatal(err)
	}
	secondData, err := os.ReadFile(second)
	if err != nil || bytes.Equal(data, secondData) {
		t.Fatal("envelopes reused randomness", err)
	}
	for _, field := range []string{"salt", "nonce", "ciphertext", "version", "length"} {
		t.Run(field, func(t *testing.T) {
			var env envelope
			if err := json.Unmarshal(data, &env); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "salt":
				env.Salt[0] ^= 1
			case "nonce":
				env.Nonce[0] ^= 1
			case "ciphertext":
				env.Ciphertext[0] ^= 1
			case "version":
				env.Version = "v2"
			case "length":
				env.Nonce = nil
			}
			mutated, err := json.Marshal(env)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(t.TempDir(), "bad.key")
			if err = os.WriteFile(p, mutated, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err = LoadEncrypted(p, password); err == nil {
				t.Fatal("tampered envelope accepted")
			}
		})
	}
	if _, err = CreateEncrypted(filepath.Join(dir, "empty"), nil); err == nil {
		t.Fatal("empty passphrase accepted")
	}
	if !RequiresEncryption {
		if err = os.Chmod(path, 0644); err != nil {
			t.Fatal(err)
		}
		if _, err = LoadEncrypted(path, password); err == nil {
			t.Fatal("unsafe permissions accepted")
		}

	}
}

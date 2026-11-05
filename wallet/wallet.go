// Package wallet manages plaintext and encrypted Ed25519 seed files.
package wallet

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/mirstar13/go-blockchain/ledger"
)

func Create(path string) (ledger.Address, error) {
	if RequiresEncryption {
		return ledger.Address{}, fmt.Errorf("Windows requires encrypted wallets: use wallet create --passphrase-file <file>")
	}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return ledger.Address{}, e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return ledger.Address{}, e
	}
	data := hex.EncodeToString(priv.Seed()) + "\n"
	_, e = f.WriteString(data)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		_ = os.Remove(path)
		return ledger.Address{}, e
	}
	var a ledger.Address
	copy(a[:], pub)
	return a, nil
}
func Load(path string) (ed25519.PrivateKey, error) {
	if RequiresEncryption {
		return nil, fmt.Errorf("Windows requires encrypted wallets: supply --passphrase-file; migrate plaintext keys with wallet encrypt")
	}
	return loadPlaintext(path)
}

func loadPlaintext(path string) (ed25519.PrivateKey, error) {
	data, e := ReadSecret(path, 4096)
	if e != nil {
		return nil, e
	}
	defer clear(data)
	seed, e := hex.DecodeString(strings.TrimSpace(string(data)))
	if e != nil || len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("invalid Ed25519 seed file")
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
func Address(key ed25519.PrivateKey) (ledger.Address, error) {
	var a ledger.Address
	if len(key) != ed25519.PrivateKeySize {
		return a, fmt.Errorf("invalid private key length")
	}
	copy(a[:], key.Public().(ed25519.PublicKey))
	return a, nil
}
func Sign(key ed25519.PrivateKey, t ledger.Transaction) (ledger.Transaction, error) {
	a, e := Address(key)
	if e != nil {
		return t, e
	}
	t.Sender = a
	data, e := t.SigningBytes()
	if e != nil {
		return t, e
	}
	copy(t.Signature[:], ed25519.Sign(key, data))
	return t, nil
}

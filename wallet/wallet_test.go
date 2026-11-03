package wallet

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/mirstar13/go-blockchain/ledger"
)

func TestKeyLifecycle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "alice.key")
	a, e := Create(path)
	if e != nil {
		t.Fatal(e)
	}
	info, e := os.Stat(path)
	if e != nil {
		t.Fatal(e)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	k, e := Load(path)
	if e != nil {
		t.Fatal(e)
	}
	got, e := Address(k)
	if e != nil || got != a {
		t.Fatal(got, e)
	}
	if _, e = Create(path); !errors.Is(e, os.ErrExist) {
		t.Fatal(e)
	}
	tx, e := Sign(k, ledger.Transaction{NetworkID: ledger.NetworkID, Recipient: a, Amount: 1, Nonce: 1})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Verify(); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(path, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(path); e == nil {
		t.Fatal("permissive key accepted")
	}
	if _, e = Sign(nil, tx); e == nil {
		t.Fatal("invalid key accepted")
	}
	if e = os.Chmod(path, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, []byte("bad"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Load(path); e == nil {
		t.Fatal("bad seed accepted")
	}
}

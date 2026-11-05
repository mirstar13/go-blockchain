package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/mirstar13/go-blockchain/ledger"
)

func TestCLIWorkflow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger space тест")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "ledger.db")
	alice := filepath.Join(dir, "alice.key")
	bob := filepath.Join(dir, "bob.key")
	password := filepath.Join(dir, "passphrase.txt")
	if err := os.WriteFile(password, []byte("test workflow password\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		if args[0] == "transfer" || (args[0] == "wallet" && (args[1] == "create" || args[1] == "address")) {
			args = append(args, "--passphrase-file", password)
		}
		var out bytes.Buffer
		if e := run(context.Background(), args, &out); e != nil {
			t.Fatalf("%v: %v", args, e)
		}
		return strings.TrimSpace(out.String())
	}
	call("init", "--db", db)
	a := call("wallet", "create", "--out", alice)
	b := call("wallet", "create", "--out", bob)
	if got := call("wallet", "address", "--key", alice); got != a {
		t.Fatal(got)
	}
	call("mine", "--db", db, "--reward-to", a)
	id := call("transfer", "--db", db, "--key", alice, "--to", b, "--amount", "10")
	if len(id) != 64 {
		t.Fatal(id)
	}
	var pending []transactionDisplay
	if e := json.Unmarshal([]byte(call("pending", "--db", db)), &pending); e != nil || len(pending) != 1 {
		t.Fatal(pending, e)
	}
	if pending[0].ID != id || pending[0].Sender != a || pending[0].Recipient != b || len(pending[0].Signature) != 128 {
		t.Fatal(pending)
	}
	var reserved ledger.BalanceSummary
	if err := json.Unmarshal([]byte(call("balance", "--db", db, "--address", a)), &reserved); err != nil || reserved != (ledger.BalanceSummary{Confirmed: 50, Reserved: 10, Available: 40}) {
		t.Fatal(reserved, err)
	}
	backup := filepath.Join(dir, "snapshot.backup")
	restored := filepath.Join(dir, "restored.db")
	call("backup", "--db", db, "--out", backup)
	call("restore", "--from", backup, "--db", restored)
	call("validate", "--db", restored)
	if got := call("pending", "--db", restored); got != call("pending", "--db", db) {
		t.Fatal("restore lost queue")
	}
	call("mine", "--db", db, "--reward-to", a)
	if got := call("pending", "--db", db); got != "[]" {
		t.Fatal("empty queue", got)
	}
	var account ledger.BalanceSummary
	if e := json.Unmarshal([]byte(call("balance", "--db", db, "--address", b)), &account); e != nil || account.Confirmed != 10 || account.Available != 10 || account.Reserved != 0 {
		t.Fatal(account, e)
	}
	var blocks []blockDisplay
	if e := json.Unmarshal([]byte(call("blocks", "--db", db)), &blocks); e != nil || len(blocks) != 3 {
		t.Fatal(len(blocks), e)
	}
	if blocks[2].Previous != blocks[1].Hash || len(blocks[2].Hash) != 64 || blocks[2].RewardTo != a || blocks[2].Transactions[0].ID != id {
		t.Fatal(blocks)
	}
	call("validate", "--db", db)
	for _, args := range [][]string{{}, {"bogus"}, {"wallet"}, {"wallet", "create"}, {"balance", "--db", db, "--address", "bad"}, {"transfer", "--db", db, "--key", alice, "--to", b, "--amount", "999"}, {"init", "--db", db}, {"pending", "--db", db, "extra"}, {"backup", "--db", db}, {"restore", "--db", restored}, {"backup", "--db", db, "--out", backup}, {"restore", "--from", backup, "--db", db}} {
		var out bytes.Buffer
		if e := run(context.Background(), args, &out); e == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}

func TestEncryptedCancellationCLI(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ledger space тест")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "ledger.db")
	key := filepath.Join(dir, "encrypted.key")
	passwordFile := filepath.Join(dir, "password")
	if err := os.WriteFile(passwordFile, []byte("test passphrase with spaces\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return strings.TrimSpace(out.String())
	}
	call("init", "--db", db)
	a := call("wallet", "create", "--out", key, "--passphrase-file", passwordFile)
	if got := call("wallet", "address", "--key", key, "--passphrase-file", passwordFile); got != a {
		t.Fatal(got)
	}
	call("mine", "--db", db, "--reward-to", a)
	first := call("transfer", "--db", db, "--key", key, "--passphrase-file", passwordFile, "--to", a, "--amount", "10")
	second := call("transfer", "--db", db, "--key", key, "--passphrase-file", passwordFile, "--to", a, "--amount", "5")
	var lookup struct {
		Status      string
		Transaction transactionDisplay
	}
	if err := json.Unmarshal([]byte(call("lookup", "--db", db, "--id", first)), &lookup); err != nil || lookup.Status != "pending" || lookup.Transaction.ID != first {
		t.Fatal(lookup, err)
	}
	var result struct{ Removed []string }
	if err := json.Unmarshal([]byte(call("cancel", "--db", db, "--id", first, "--key", key, "--passphrase-file", passwordFile)), &result); err != nil || !reflect.DeepEqual(result.Removed, []string{first, second}) {
		t.Fatal(result, err)
	}
	if got := call("pending", "--db", db); got != "[]" {
		t.Fatal(got)
	}
	var summary ledger.BalanceSummary
	if err := json.Unmarshal([]byte(call("balance", "--db", db, "--address", a)), &summary); err != nil || summary.Available != 50 || summary.Reserved != 0 {
		t.Fatal(summary, err)
	}
	plain := filepath.Join(dir, "plain.key")
	encryptedCopy := filepath.Join(dir, "copy.key")
	seed := make([]byte, ed25519.SeedSize)
	if err := os.WriteFile(plain, []byte(hex.EncodeToString(seed)+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	address := hex.EncodeToString(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey))
	if got := call("wallet", "encrypt", "--key", plain, "--out", encryptedCopy, "--passphrase-file", passwordFile); got != address {
		t.Fatal(got)
	}
	if got := call("wallet", "address", "--key", encryptedCopy, "--passphrase-file", passwordFile); got != address {
		t.Fatal(got)
	}
	if data, err := os.ReadFile(plain); err != nil || string(data) != hex.EncodeToString(seed)+"\n" {
		t.Fatal("original key changed", err)
	}
}

func TestTransferCapacityFlagsAndInterrupt(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "ledger.db")
	key := filepath.Join(dir, "key")
	password := filepath.Join(dir, "password")
	if err := os.WriteFile(password, []byte("capacity test password"), 0600); err != nil {
		t.Fatal(err)
	}
	call := func(args ...string) string {
		t.Helper()
		var out bytes.Buffer
		if err := run(context.Background(), args, &out); err != nil {
			t.Fatal(args, err)
		}
		return strings.TrimSpace(out.String())
	}
	call("init", "--db", db)
	a := call("wallet", "create", "--out", key, "--passphrase-file", password)
	call("mine", "--db", db, "--reward-to", a)
	transfer := []string{"transfer", "--db", db, "--key", key, "--passphrase-file", password, "--to", a, "--amount", "1", "--max-pending", "1", "--max-pending-per-sender", "1"}
	call(transfer...)
	var out bytes.Buffer
	if err := run(context.Background(), transfer, &out); !errors.Is(err, ledger.ErrQueueFull) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := run(ctx, []string{"validate", "--db", db}, &out); !errors.Is(err, context.Canceled) || exitCode(err) != 130 {
		t.Fatal(err)
	}
	if exitCode(fmt.Errorf("wrapped: %w", context.Canceled)) != 130 || exitCode(context.DeadlineExceeded) != 1 || exitCode(errors.New("failure")) != 1 || exitCode(nil) != 0 {
		t.Fatal("exit status mapping")
	}
}

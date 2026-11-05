package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/mirstar13/go-blockchain/ledger"
	"github.com/mirstar13/go-blockchain/wallet"
)

const usage = `Usage: ledger <command> [flags]
  init     --db ledger.db
  wallet create  --out alice.key [--passphrase-file protected-file]
  wallet encrypt --key alice.key --out encrypted.key --passphrase-file protected-file
  wallet address --key alice.key [--passphrase-file protected-file]
  balance  --db ledger.db --address <hex-public-key>
  transfer --db ledger.db --key alice.key --to <address> --amount <integer> [--passphrase-file protected-file]
  lookup   --db ledger.db --id <transaction-id>
  cancel   --db ledger.db --id <transaction-id> --key alice.key [--passphrase-file protected-file]
  transfer admission options: --max-pending 1000 --max-pending-per-sender 100
  pending  --db ledger.db
  mine     --db ledger.db --reward-to <address>
  blocks   --db ledger.db
  validate --db ledger.db
  backup   --db ledger.db --out ledger.backup
  restore  --from ledger.backup --db restored.db
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if e := run(ctx, os.Args[1:], os.Stdout); e != nil {
		fmt.Fprintln(os.Stderr, "error:", e)
		os.Exit(exitCode(e))
	}
}
func run(ctx context.Context, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: ledger {init|wallet|balance|transfer|pending|mine|blocks|validate|backup|restore}")
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, usage)
		return err
	}
	cmd := args[0]
	args = args[1:]
	if cmd == "wallet" {
		if len(args) == 0 {
			return errors.New("usage: ledger wallet {create|address}")
		}
		cmd = "wallet " + args[0]
		args = args[1:]
	}
	switch cmd {
	case "init", "wallet create", "wallet address", "balance", "transfer", "pending", "mine", "blocks", "validate", "backup", "restore", "lookup", "cancel", "wallet encrypt":
	default:
		return fmt.Errorf("unknown command %q", cmd)
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var db, keyPath, output, address, to, source, transactionID, passphraseFile string
	var amount uint64
	options := ledger.Options{}
	switch cmd {
	case "wallet create":
		fs.StringVar(&output, "out", "", "new key file")
	case "wallet encrypt":
		fs.StringVar(&keyPath, "key", "", "plaintext input key")
		fs.StringVar(&output, "out", "", "new encrypted key file")
	case "wallet address":
		fs.StringVar(&keyPath, "key", "", "key file")
	default:
		fs.StringVar(&db, "db", "ledger.db", "database path")
	}
	switch cmd {
	case "lookup", "cancel":
		fs.StringVar(&transactionID, "id", "", "transaction ID")
		if cmd == "cancel" {
			fs.StringVar(&keyPath, "key", "", "sender key")
		}
	case "backup":
		fs.StringVar(&output, "out", "", "new backup file")
	case "restore":
		fs.StringVar(&source, "from", "", "backup file")
	case "balance":
		fs.StringVar(&address, "address", "", "account address")
	case "transfer":
		fs.IntVar(&options.MaxPending, "max-pending", ledger.DefaultMaxPending, "maximum pending transfers")
		fs.IntVar(&options.MaxPendingPerSender, "max-pending-per-sender", ledger.DefaultMaxPendingPerSender, "maximum pending transfers per sender")
		fs.StringVar(&keyPath, "key", "", "signing key")
		fs.StringVar(&to, "to", "", "recipient")
		fs.Uint64Var(&amount, "amount", 0, "integer amount")
	case "mine":
		fs.StringVar(&to, "reward-to", "", "reward recipient")
	}
	switch cmd {
	case "wallet create", "wallet address", "wallet encrypt", "transfer", "cancel":
		fs.StringVar(&passphraseFile, "passphrase-file", "", "owner-only passphrase file for encrypted key")
	}
	if e := fs.Parse(args); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			_, err := io.WriteString(out, usage)
			return err
		}
		return e
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	if wallet.RequiresEncryption && passphraseFile == "" {
		switch cmd {
		case "wallet create", "wallet address", "wallet encrypt", "transfer", "cancel":
			return errors.New("Windows requires --passphrase-file for encrypted wallets; use wallet encrypt to migrate a plaintext key")
		}
	}
	var password []byte
	if passphraseFile != "" {
		var err error
		password, err = wallet.ReadSecret(passphraseFile, 4098)
		if err != nil {
			return err
		}
		defer clear(password)
		password = bytes.TrimSuffix(password, []byte("\n"))
		password = bytes.TrimSuffix(password, []byte("\r"))
		if len(password) == 0 || len(password) > 4096 {
			return errors.New("passphrase must contain 1 to 4096 bytes")
		}
	}
	loadKey := func() (ed25519.PrivateKey, error) {
		if passphraseFile != "" {
			return wallet.LoadEncrypted(keyPath, password)
		}
		return wallet.Load(keyPath)
	}
	printJSON := func(v any) error { enc := json.NewEncoder(out); enc.SetIndent("", "  "); return enc.Encode(v) }
	switch cmd {
	case "wallet create":
		if output == "" {
			return errors.New("--out is required")
		}
		var a ledger.Address
		var e error
		if passphraseFile != "" {
			a, e = wallet.CreateEncrypted(output, password)
		} else {
			a, e = wallet.Create(output)
		}
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, a)
		return e
	case "wallet address":
		k, e := loadKey()
		if e != nil {
			return e
		}
		a, e := wallet.Address(k)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, a)
		return e
	case "wallet encrypt":
		if passphraseFile == "" || output == "" {
			return errors.New("--out and --passphrase-file are required")
		}
		a, err := wallet.EncryptFile(keyPath, output, password)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, a)
		return err
	case "restore":
		if source == "" {
			return errors.New("--from is required")
		}
		if err := ledger.Restore(ctx, source, db); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Restored", db)
		return err
	case "backup":
		if output == "" {
			return errors.New("--out is required")
		}
	case "init":
		if e := ledger.Init(db); e != nil {
			return e
		}
		_, e := fmt.Fprintln(out, "Initialized", db)
		return e
	}
	c, e := ledger.Open(ctx, db, options)
	if e != nil {
		return e
	}
	defer c.Close()
	switch cmd {
	case "lookup":
		id, err := ledger.ParseHash(transactionID)
		if err != nil {
			return err
		}
		info, err := c.Lookup(id)
		if err != nil {
			return err
		}
		view, err := displayTransactions([]ledger.Transaction{info.Transaction})
		if err != nil {
			return err
		}
		return printJSON(struct {
			Status      string
			Height      *uint64 `json:",omitempty"`
			Transaction transactionDisplay
		}{info.Status, info.Height, view[0]})
	case "cancel":
		id, err := ledger.ParseHash(transactionID)
		if err != nil {
			return err
		}
		request, err := c.PrepareCancellation(id)
		if err != nil {
			return err
		}
		k, err := loadKey()
		if err != nil {
			return err
		}
		defer clear(k)
		request, err = wallet.SignCancellation(k, request)
		if err != nil {
			return err
		}
		removed, err := c.Cancel(request)
		if err != nil {
			return err
		}
		ids := make([]string, len(removed))
		for i, id := range removed {
			ids[i] = id.String()
		}
		return printJSON(struct{ Removed []string }{ids})

	case "backup":
		if err := c.Backup(ctx, output); err != nil {
			return err
		}
		_, err := fmt.Fprintln(out, "Backup saved to", output)
		return err
	case "balance":
		a, e := ledger.ParseAddress(address)
		if e != nil {
			return e
		}
		s, e := c.BalanceSummary(a)
		if e != nil {
			return e
		}
		return printJSON(struct {
			Address string
			ledger.BalanceSummary
		}{a.String(), s})
	case "transfer":
		a, e := ledger.ParseAddress(to)
		if e != nil {
			return e
		}
		k, e := loadKey()
		if e != nil {
			return e
		}
		sender, e := wallet.Address(k)
		if e != nil {
			return e
		}
		nonce, e := c.NextNonce(sender)
		if e != nil {
			return e
		}
		t, e := wallet.Sign(k, ledger.Transaction{NetworkID: ledger.NetworkID, Recipient: a, Amount: amount, Nonce: nonce})
		if e != nil {
			return e
		}
		id, e := c.Submit(t)
		if e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, id)
		return e
	case "pending":
		p, e := c.Pending()
		if e != nil {
			return e
		}
		view, err := displayTransactions(p)
		if err != nil {
			return err
		}
		return printJSON(view)
	case "blocks":
		b, e := c.Blocks()
		if e != nil {
			return e
		}
		view, err := displayBlocks(b)
		if err != nil {
			return err
		}
		return printJSON(view)
	case "mine":
		a, e := ledger.ParseAddress(to)
		if e != nil {
			return e
		}
		b, e := c.Mine(ctx, a)
		if e != nil {
			return e
		}
		h, e := b.Hash()
		if e != nil {
			return e
		}
		_, e = fmt.Fprintf(out, "Mined block %d: %s (%d transactions)\n", b.Height, h, len(b.Transactions))
		return e
	case "validate":
		if e := c.Validate(ctx); e != nil {
			return e
		}
		_, e = fmt.Fprintln(out, "Ledger is valid")
		return e
	}
	return nil
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, context.Canceled) {
		return 130
	}
	return 1
}

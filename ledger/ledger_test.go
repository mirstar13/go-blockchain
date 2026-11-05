package ledger

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"math"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"
)

func identity(t *testing.T) (Address, ed25519.PrivateKey) {
	t.Helper()
	pub, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	var a Address
	copy(a[:], pub)
	return a, k
}
func signed(t *testing.T, k ed25519.PrivateKey, to Address, amount, nonce uint64) Transaction {
	t.Helper()
	var from Address
	copy(from[:], k.Public().(ed25519.PublicKey))
	tx := Transaction{NetworkID: NetworkID, Sender: from, Recipient: to, Amount: amount, Nonce: nonce}
	data, e := tx.SigningBytes()
	if e != nil {
		t.Fatal(e)
	}
	copy(tx.Signature[:], ed25519.Sign(k, data))
	return tx
}
func newChain(t *testing.T) (*Chain, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ledger.db")
	if e := Init(path); e != nil {
		t.Fatal(e)
	}
	c, e := Open(context.Background(), path, Options{})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, path
}
func mustMine(t *testing.T, c *Chain, a Address) Block {
	t.Helper()
	b, e := c.Mine(context.Background(), a)
	if e != nil {
		t.Fatal(e)
	}
	return b
}
func TestPersistentWorkflow(t *testing.T) {
	c, path := newChain(t)
	alice, k := identity(t)
	bob, _ := identity(t)
	mustMine(t, c, alice)
	tx := signed(t, k, bob, 10, 1)
	if _, e := c.Submit(tx); e != nil {
		t.Fatal(e)
	}
	if _, e := c.Submit(tx); !errors.Is(e, ErrDuplicate) {
		t.Fatalf("duplicate: %v", e)
	}
	if _, e := c.Submit(signed(t, k, bob, 41, 2)); !errors.Is(e, ErrFunds) {
		t.Fatalf("queued overspend: %v", e)
	}
	if _, e := c.Submit(signed(t, k, bob, 1, 3)); !errors.Is(e, ErrNonce) {
		t.Fatalf("nonce gap: %v", e)
	}
	if e := c.Close(); e != nil {
		t.Fatal(e)
	}
	var e error
	c, e = Open(context.Background(), path, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	p, e := c.Pending()
	if e != nil || len(p) != 1 || p[0] != tx {
		t.Fatalf("reopen pending: %v %v", p, e)
	}
	// Returned data must not alias internal data.
	p[0].Amount = 999
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = c.Mine(ctx, alice); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	p, _ = c.Pending()
	if len(p) != 1 || p[0] != tx {
		t.Fatal("cancel changed queue")
	}
	mustMine(t, c, alice)
	a, _ := c.Balance(alice)
	b, _ := c.Balance(bob)
	if a != (Account{90, 1}) || b != (Account{10, 0}) {
		t.Fatalf("balances: %v %v", a, b)
	}
	if _, e = c.Submit(tx); !errors.Is(e, ErrNonce) {
		t.Fatalf("replay: %v", e)
	}
	p, _ = c.Pending()
	if len(p) != 0 {
		t.Fatal("mined queue retained")
	}
	if e = c.Validate(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e = c.Close(); e != nil {
		t.Fatal(e)
	}
	c, e = Open(context.Background(), path, Options{})
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
}
func TestTransferRejectionsAndSelfTransfer(t *testing.T) {
	a, k := identity(t)
	b, _ := identity(t)
	cases := []struct {
		name  string
		tx    Transaction
		state map[Address]Account
		want  error
	}{
		{"funds", signed(t, k, b, 51, 1), map[Address]Account{a: {50, 0}}, ErrFunds},
		{"nonce", signed(t, k, b, 1, 2), map[Address]Account{a: {50, 0}}, ErrNonce},
		{"zero", signed(t, k, b, 0, 1), map[Address]Account{a: {50, 0}}, ErrInvalid},
		{"overflow", signed(t, k, b, 1, 1), map[Address]Account{a: {50, 0}, b: {math.MaxUint64, 0}}, ErrOverflow},
		{"nonce overflow", signed(t, k, b, 1, 0), map[Address]Account{a: {50, math.MaxUint64}}, ErrOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := map[Address]Account{}
			for a, s := range tc.state {
				before[a] = s
			}
			if e := apply(tc.state, tc.tx); !errors.Is(e, tc.want) {
				t.Fatal(e)
			}
			if !reflect.DeepEqual(before, tc.state) {
				t.Fatal("rejection changed state")
			}
		})
	}
	tx := signed(t, k, b, 1, 1)
	tx.Amount = 2
	if e := tx.Verify(); !errors.Is(e, ErrSignature) {
		t.Fatal(e)
	}
	tx.NetworkID = "other"
	if e := tx.Verify(); !errors.Is(e, ErrNetwork) {
		t.Fatal(e)
	}
	s := map[Address]Account{a: {50, 0}}
	if e := apply(s, signed(t, k, a, 20, 1)); e != nil {
		t.Fatal(e)
	}
	if s[a] != (Account{50, 1}) {
		t.Fatal(s)
	}
}
func TestPendingIncomingCannotFundSpend(t *testing.T) {
	a, k := identity(t)
	b, bk := identity(t)
	c, _ := identity(t)
	s := map[Address]Account{a: {50, 0}}
	if e := reserve(s, []Transaction{signed(t, k, b, 10, 1), signed(t, bk, c, 1, 1)}); !errors.Is(e, ErrFunds) {
		t.Fatal(e)
	}
}
func TestEncodingAndGenesis(t *testing.T) {
	h, e := Genesis().Hash()
	if e != nil {
		t.Fatal(e)
	}
	const expected = "332b60cd16be79304dff0a89ed751d9c50e7e96e8d82c39ec7a31c5a0d16c896"
	if h.String() != expected {
		t.Fatalf("genesis hash: %s", h)
	}
}
func TestBlockRules(t *testing.T) {
	a, k := identity(t)
	b, _ := identity(t)
	g := Genesis()
	candidate := Block{Version: Version, NetworkID: NetworkID, Height: 1, Previous: mustHash(t, g), RewardTo: a, Reward: Reward, Difficulty: Difficulty}
	good, e := mine(context.Background(), candidate)
	if e != nil {
		t.Fatal(e)
	}
	if e = applyBlock(map[Address]Account{}, g, good); e != nil {
		t.Fatal(e)
	}
	changes := []func(*Block){func(b *Block) { b.Height++ }, func(b *Block) { b.Previous[0]++ }, func(b *Block) { b.Reward++ }, func(b *Block) { b.Difficulty-- }, func(b *Block) { b.Version++ }, func(b *Block) { b.NetworkID = "other" }, func(b *Block) { b.Transactions = make([]Transaction, 101) }}
	for i, change := range changes {
		bad := good
		change(&bad)
		if e = applyBlock(map[Address]Account{}, g, bad); e == nil {
			t.Fatalf("accepted mutation %d", i)
		}
	}
	bad := good
	for {
		bad.Nonce++
		h, _ := bad.Hash()
		if !validWork(h) {
			break
		}
	}
	if e = applyBlock(map[Address]Account{}, g, bad); e == nil {
		t.Fatal("bad work accepted")
	}
	candidate.Transactions = []Transaction{signed(t, k, b, 1, 1)}
	candidate, e = mine(context.Background(), candidate)
	if e != nil {
		t.Fatal(e)
	}
	if e = applyBlock(map[Address]Account{}, g, candidate); !errors.Is(e, ErrFunds) {
		t.Fatalf("spent same-block reward: %v", e)
	}
	parent := good
	parent.Timestamp = 10
	candidate = good
	candidate.Height = 2
	candidate.Previous = mustHash(t, parent)
	candidate.Timestamp = 9
	if e = applyBlock(map[Address]Account{}, parent, candidate); e == nil {
		t.Fatal("backward timestamp")
	}
}
func mustHash(t *testing.T, b Block) Hash {
	t.Helper()
	h, e := b.Hash()
	if e != nil {
		t.Fatal(e)
	}
	return h
}
func TestQueueOrderAndBlockLimit(t *testing.T) {
	c, _ := newChain(t)
	c.options.MaxPendingPerSender = 200
	a, k := identity(t)
	mustMine(t, c, a)
	// Self-transfers preserve confirmed balance but reserve one unit each in queue.
	for i := 0; i < 3; i++ {
		mustMine(t, c, a)
	}
	for n := uint64(1); n <= 101; n++ {
		if _, e := c.Submit(signed(t, k, a, 1, n)); e != nil {
			t.Fatal(e)
		}
	}
	b := mustMine(t, c, a)
	if len(b.Transactions) != 100 {
		t.Fatal(len(b.Transactions))
	}
	for i, tx := range b.Transactions {
		if tx.Nonce != uint64(i+1) {
			t.Fatal("order")
		}
	}
	p, _ := c.Pending()
	if len(p) != 1 || p[0].Nonce != 101 {
		t.Fatal(p)
	}
	if e := c.Validate(context.Background()); e != nil {
		t.Fatal(e)
	}
	mustMine(t, c, a)
	if e := c.Validate(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestCommitFailurePreservesDatabase(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	if _, e := c.Submit(signed(t, k, b, 1, 1)); e != nil {
		t.Fatal(e)
	}
	// Force a reward overflow to fail after ordinary transfers have been applied
	// to the candidate state. None of those changes may reach storage.
	if e := c.db.Update(func(tx *bolt.Tx) error {
		return put(tx.Bucket([]byte("accounts")), b[:], Account{math.MaxUint64 - 1, 0})
	}); e != nil {
		t.Fatal(e)
	}
	before, _ := c.Blocks()
	p, _ := c.Pending()
	aBefore, _ := c.Balance(a)
	if _, e := c.Mine(context.Background(), b); !errors.Is(e, ErrOverflow) {
		t.Fatal(e)
	}
	after, _ := c.Blocks()
	pAfter, _ := c.Pending()
	aAfter, _ := c.Balance(a)
	if !reflect.DeepEqual(before, after) || !reflect.DeepEqual(p, pAfter) || aBefore != aAfter {
		t.Fatal("failed commit changed storage")
	}
}
func TestCorruptionAndVersionRejected(t *testing.T) {
	for _, kind := range []string{"version", "genesis", "account", "pending index", "missing bucket", "block"} {
		t.Run(kind, func(t *testing.T) {
			c, path := newChain(t)
			a, k := identity(t)
			mustMine(t, c, a)
			if _, e := c.Submit(signed(t, k, a, 1, 1)); e != nil {
				t.Fatal(e)
			}
			e := c.db.Update(func(tx *bolt.Tx) error {
				switch kind {
				case "version":
					return tx.Bucket([]byte("meta")).Put([]byte("version"), key(99))
				case "genesis":
					g := Genesis()
					g.Timestamp = 1
					return put(tx.Bucket([]byte("blocks")), key(0), g)
				case "account":
					return put(tx.Bucket([]byte("accounts")), a[:], Account{999, 0})
				case "pending index":
					return tx.Bucket([]byte("pending_ids")).ForEach(func(k, v []byte) error { return tx.Bucket([]byte("pending_ids")).Put(k, key(999)) })
				case "missing bucket":
					return tx.DeleteBucket([]byte("accounts"))
				default:
					return tx.Bucket([]byte("blocks")).Put(key(1), []byte("broken"))
				}
			})
			if e != nil {
				t.Fatal(e)
			}
			if e = c.Close(); e != nil {
				t.Fatal(e)
			}
			opened, e := Open(context.Background(), path, Options{})
			if e == nil {
				opened.Close()
				t.Fatal("corrupt database opened")
			}
		})
	}
}
func TestInitAndLock(t *testing.T) {
	c, path := newChain(t)
	if e := Init(path); e == nil {
		t.Fatal("overwrote database")
	}
	if second, e := Open(context.Background(), path, Options{}); e == nil {
		second.Close()
		t.Fatal("lock not enforced")
	}
	if e := c.Validate(context.Background()); e != nil {
		t.Fatal(e)
	}
	if _, e := Open(context.Background(), filepath.Join(t.TempDir(), "missing"), Options{}); e == nil {
		t.Fatal("created missing database")
	}
}
func TestEncodingBoundaries(t *testing.T) {
	var a, b bytes.Buffer
	field(&a, "a1")
	number(&a, 23)
	field(&b, "a")
	number(&b, 123)
	if bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("ambiguous encoding")
	}
}

// This context cancels at the final commit check, after all bucket writes.
type cancelAtCommit struct {
	context.Context
	calls int
}

func (c *cancelAtCommit) Err() error {
	c.calls++
	if c.calls >= 2 {
		return context.Canceled
	}
	return nil
}
func TestRollbackAfterWritesAndStaleSnapshot(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	parent := mustMine(t, c, a)
	tx := signed(t, k, b, 10, 1)
	if _, e := c.Submit(tx); e != nil {
		t.Fatal(e)
	}
	candidate, e := mine(context.Background(), Block{Version: Version, NetworkID: NetworkID, Height: 2, Previous: mustHash(t, parent), Timestamp: parent.Timestamp, Transactions: []Transaction{tx}, RewardTo: a, Reward: Reward, Difficulty: Difficulty})
	if e != nil {
		t.Fatal(e)
	}
	ctx := &cancelAtCommit{Context: context.Background()}
	if e = c.commit(ctx, parent, candidate, []Transaction{tx}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	blocks, _ := c.Blocks()
	pending, _ := c.Pending()
	account, _ := c.Balance(a)
	if len(blocks) != 2 || len(pending) != 1 || account != (Account{50, 0}) {
		t.Fatal("partial write escaped rollback")
	}
	if e = c.Validate(context.Background()); e != nil {
		t.Fatal(e)
	}
	mustMine(t, c, a)
	if e = c.commit(context.Background(), parent, candidate, []Transaction{tx}); !errors.Is(e, ErrConflict) {
		t.Fatalf("stale snapshot: %v", e)
	}
}

func TestConcurrentDuplicateSubmission(t *testing.T) {
	c, _ := newChain(t)
	a, key := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	tx := signed(t, key, b, 1, 1)
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := c.Submit(tx); results <- err }()
	}
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrDuplicate) {
			t.Fatal(err)
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d copies", accepted)
	}
	pending, err := c.Pending()
	if err != nil || len(pending) != 1 {
		t.Fatal(pending, err)
	}
	if err := c.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

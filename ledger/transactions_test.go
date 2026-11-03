package ledger

import (
	"context"
	"crypto/ed25519"
	"errors"
	bolt "go.etcd.io/bbolt"
	"reflect"
	"sync"
	"testing"
)

func authorize(key ed25519.PrivateKey, r Cancellation) Cancellation {
	copy(r.Signature[:], ed25519.Sign(key, r.SigningBytes()))
	return r
}
func TestCancellationCascadeAndReplay(t *testing.T) {
	c, path := newChain(t)
	a, k := identity(t)
	b, bk := identity(t)
	mustMine(t, c, a)
	mustMine(t, c, b)
	txs := []Transaction{signed(t, k, b, 5, 1), signed(t, bk, a, 7, 1), signed(t, k, b, 10, 2), signed(t, k, b, 15, 3)}
	ids := make([]Hash, len(txs))
	for i, tx := range txs {
		var err error
		ids[i], err = c.Submit(tx)
		if err != nil {
			t.Fatal(err)
		}
	}
	info, err := c.Lookup(ids[2])
	if err != nil || info.Status != "pending" || info.Height != nil || info.Transaction != txs[2] {
		t.Fatal(info, err)
	}
	r, err := c.PrepareCancellation(ids[2])
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []Cancellation{r, authorize(bk, r)} {
		if _, err = c.Cancel(bad); !errors.Is(err, ErrSignature) {
			t.Fatal(err)
		}
	}
	r = authorize(k, r)
	bad := r
	bad.ID = ids[0]
	if _, err = c.Cancel(bad); !errors.Is(err, ErrSignature) {
		t.Fatal(err)
	}
	removed, err := c.Cancel(r)
	if err != nil || !reflect.DeepEqual(removed, ids[2:]) {
		t.Fatal(removed, err)
	}
	p, err := c.Pending()
	if err != nil || !reflect.DeepEqual(p, txs[:2]) {
		t.Fatal(p, err)
	}
	summary, err := c.BalanceSummary(a)
	if err != nil || summary.Reserved != 5 || summary.Available != 45 {
		t.Fatal(summary, err)
	}
	if _, err = c.Lookup(ids[2]); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// Same transfer can be admitted again, but its earlier cancellation cannot replay.
	if _, err = c.Submit(txs[2]); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Cancel(r); !errors.Is(err, ErrConflict) {
		t.Fatal(err)
	}
	if err = c.Close(); err != nil {
		t.Fatal(err)
	}
	c, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	p, err = c.Pending()
	if err != nil || len(p) != 3 {
		t.Fatal(p, err)
	}
	mustMine(t, c, a)
	info, err = c.Lookup(ids[2])
	if err != nil || info.Status != "confirmed" || info.Height == nil || *info.Height != 3 {
		t.Fatal(info, err)
	}
	if _, err = c.PrepareCancellation(ids[2]); !errors.Is(err, ErrConfirmed) {
		t.Fatal(err)
	}
	if _, err = c.Cancel(r); !errors.Is(err, ErrConfirmed) {
		t.Fatal(err)
	}
	if err = c.Validate(); err != nil {
		t.Fatal(err)
	}
}
func TestCancelVersusMiningCommit(t *testing.T) {
	for i := 0; i < 5; i++ {
		c, _ := newChain(t)
		a, k := identity(t)
		b, _ := identity(t)
		parent := mustMine(t, c, a)
		tx := signed(t, k, b, 10, 1)
		id, err := c.Submit(tx)
		if err != nil {
			t.Fatal(err)
		}
		request, err := c.PrepareCancellation(id)
		if err != nil {
			t.Fatal(err)
		}
		request = authorize(k, request)
		candidate, err := mine(context.Background(), Block{Version: Version, NetworkID: NetworkID, Height: 2, Timestamp: parent.Timestamp, Previous: mustHash(t, parent), Transactions: []Transaction{tx}, RewardTo: a, Reward: Reward, Difficulty: Difficulty})
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var cancelErr, mineErr error
		wg.Add(2)
		go func() { defer wg.Done(); _, cancelErr = c.Cancel(request) }()
		go func() {
			defer wg.Done()
			mineErr = c.commit(context.Background(), parent, candidate, []Transaction{tx})
		}()
		wg.Wait()
		if cancelErr == nil {
			if !errors.Is(mineErr, ErrConflict) {
				t.Fatal(mineErr)
			}
		} else if mineErr != nil || !errors.Is(cancelErr, ErrConfirmed) {
			t.Fatal(cancelErr, mineErr)
		}
		if err = c.Validate(); err != nil {
			t.Fatal(err)
		}
	}
}
func TestCancellationDomainAndWrongOwner(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, bk := identity(t)
	mustMine(t, c, a)
	tx := signed(t, k, b, 1, 1)
	id, err := c.Submit(tx)
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.PrepareCancellation(id)
	if err != nil {
		t.Fatal(err)
	}
	r.Signature = tx.Signature
	if _, err = c.Cancel(r); !errors.Is(err, ErrSignature) {
		t.Fatal("transfer signature authorized cancellation", err)
	}
	r.Sender = b
	r = authorize(bk, r)
	if _, err = c.Cancel(r); !errors.Is(err, ErrSignature) {
		t.Fatal("wrong owner", err)
	}
	if _, err = ParseHash("bad"); err == nil {
		t.Fatal("bad ID accepted")
	}
	if got, err := ParseHash(id.String()); err != nil || got != id {
		t.Fatal(got, err)
	}
}

func TestCancellationRollback(t *testing.T) {
	c, _ := newChain(t)
	a, k := identity(t)
	b, _ := identity(t)
	mustMine(t, c, a)
	first := signed(t, k, b, 1, 1)
	second := signed(t, k, b, 1, 2)
	id, err := c.Submit(first)
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := c.Submit(second)
	if err != nil {
		t.Fatal(err)
	}
	request, err := c.PrepareCancellation(id)
	if err != nil {
		t.Fatal(err)
	}
	request = authorize(k, request)
	// Force a late deletion failure after the first pending entry was removed.
	if err = c.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte("pending_ids")).Delete(secondID[:]) }); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Cancel(request); !errors.Is(err, ErrCorrupt) {
		t.Fatal(err)
	}
	pending, err := c.Pending()
	if err != nil || !reflect.DeepEqual(pending, []Transaction{first, second}) {
		t.Fatal("partial cancellation escaped rollback", pending, err)
	}
	if _, err = c.Lookup(id); err != nil {
		t.Fatal("first index deletion escaped rollback", err)
	}
}

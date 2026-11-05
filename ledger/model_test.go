package ledger

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

type modelEntry struct {
	tx                Transaction
	id                Hash
	sender, recipient int
	sequence          uint64
}
type ledgerModel struct {
	balance          [4]uint64
	nonce            [4]uint64
	queue            []modelEntry
	sequence, height uint64
	options          Options
	known            map[Hash]modelEntry
	confirmed        map[Hash]uint64
}

// This model uses its own arithmetic and queue traversal, never production
// apply/reserve/validation routines. Signing and hashing are only test plumbing.
func (m *ledgerModel) admission(e modelEntry, tampered bool) error {
	for _, q := range m.queue {
		if q.id == e.id {
			return ErrDuplicate
		}
	}
	if e.tx.Amount == 0 {
		return ErrInvalid
	}
	if tampered {
		return ErrSignature
	}
	available := m.balance[e.sender]
	nonce := m.nonce[e.sender]
	count := 0
	for _, q := range m.queue {
		if q.sender == e.sender {
			available -= q.tx.Amount
			nonce++
			count++
		}
	}
	if e.tx.Nonce != nonce+1 {
		return ErrNonce
	}
	if e.tx.Amount > available {
		return ErrFunds
	}
	if len(m.queue) >= m.options.MaxPending || count >= m.options.MaxPendingPerSender {
		return ErrQueueFull
	}
	return nil
}
func (m *ledgerModel) mine(to int) {
	m.height++
	for _, e := range m.queue {
		m.balance[e.sender] -= e.tx.Amount
		m.balance[e.recipient] += e.tx.Amount
		m.nonce[e.sender]++
		m.confirmed[e.id] = m.height
	}
	m.queue = nil
	m.balance[to] += 50
}
func checkModel(t *testing.T, c *Chain, m *ledgerModel, addresses [4]Address) {
	t.Helper()
	pending, err := c.Pending()
	if err != nil || len(pending) != len(m.queue) {
		t.Fatal("queue length", len(pending), len(m.queue), err)
	}
	for i, e := range m.queue {
		if pending[i] != e.tx {
			t.Fatal("queue order/content", i)
		}
	}
	for i, a := range addresses {
		actual, err := c.BalanceSummary(a)
		if err != nil {
			t.Fatal(err)
		}
		reserved := uint64(0)
		for _, q := range m.queue {
			if q.sender == i {
				reserved += q.tx.Amount
			}
		}
		if actual != (BalanceSummary{m.balance[i], reserved, m.balance[i] - reserved, m.nonce[i]}) {
			t.Fatalf("account %d: %+v model balance=%d nonce=%d reserved=%d", i, actual, m.balance[i], m.nonce[i], reserved)
		}
	}
	for id, e := range m.known {
		info, err := c.Lookup(id)
		height, confirmed := m.confirmed[id]
		pending := false
		for _, q := range m.queue {
			if q.id == id {
				pending = true
			}
		}
		switch {
		case confirmed:
			if err != nil || info.Status != "confirmed" || info.Height == nil || *info.Height != height || info.Transaction != e.tx {
				t.Fatal("confirmed lookup", info, err)
			}
		case pending:
			if err != nil || info.Status != "pending" || info.Height != nil || info.Transaction != e.tx {
				t.Fatal("pending lookup", info, err)
			}
		default:
			if !errors.Is(err, ErrNotFound) {
				t.Fatal("cancelled lookup", err)
			}
		}
	}
	if err := c.Validate(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func runModel(t *testing.T, actions []byte) {
	t.Helper()
	if len(actions) > 32 {
		actions = actions[:32]
	}
	path := filepath.Join(t.TempDir(), "model.db")
	if err := Init(path); err != nil {
		t.Fatal(err)
	}
	m := ledgerModel{options: Options{4, 2}, known: map[Hash]modelEntry{}, confirmed: map[Hash]uint64{}}
	c, err := Open(context.Background(), path, m.options)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	var addresses [4]Address
	var keys [4]ed25519.PrivateKey
	for i := range keys {
		seed := make([]byte, 32)
		seed[0] = byte(i + 1)
		keys[i] = ed25519.NewKeyFromSeed(seed)
		copy(addresses[i][:], keys[i].Public().(ed25519.PublicKey))
	}
	if _, err = c.Mine(context.Background(), addresses[0]); err != nil {
		t.Fatal(err)
	}
	m.mine(0)
	mined := 1
	var submitted []modelEntry
	var cancellations []Cancellation
	for step, action := range actions {
		sender := int(action>>3) & 3
		recipient := int(action>>5) & 3
		switch action & 7 {
		case 0, 1, 2:
			nonce := m.nonce[sender] + 1
			for _, q := range m.queue {
				if q.sender == sender {
					nonce++
				}
			}
			amount := uint64(1 + action%3)
			if action&128 != 0 {
				amount = 0
			}
			if action&7 == 1 {
				nonce++
			}
			e := modelEntry{sender: sender, recipient: recipient, tx: signed(t, keys[sender], addresses[recipient], amount, nonce)}
			tampered := false
			if action&7 == 2 && len(submitted) > 0 {
				e = submitted[int(action)%len(submitted)]
			} else if action&7 == 2 {
				e.tx.Signature[0] ^= 1
				tampered = true
			}
			e.id, err = e.tx.ID()
			if err != nil {
				t.Fatal(err)
			}
			// Track all requests, including failed admissions, to verify not-found results.
			m.known[e.id] = e
			want := m.admission(e, tampered)
			_, got := c.Submit(e.tx)
			if (want == nil) != (got == nil) || (want != nil && !errors.Is(got, want)) {
				t.Fatalf("step %d submit: want %v got %v", step, want, got)
			}
			if got == nil {
				m.sequence++
				e.sequence = m.sequence
				m.queue = append(m.queue, e)
				submitted = append(submitted, e)
			}
		case 3, 4:
			var request Cancellation
			if action&7 == 4 && len(cancellations) > 0 {
				request = cancellations[int(action)%len(cancellations)]
			} else {
				if len(m.queue) == 0 {
					break
				}
				e := m.queue[int(action)%len(m.queue)]
				request, err = c.PrepareCancellation(e.id)
				if err != nil || request.Sequence != e.sequence || request.Sender != addresses[e.sender] {
					t.Fatal("prepare", request, err)
				}
				key := keys[e.sender]
				if action&128 != 0 {
					key = keys[(e.sender+1)%4]
				}
				request = authorize(key, request)
				if action&128 == 0 {
					cancellations = append(cancellations, request)
				}
			}
			var want error
			index := -1
			for i, e := range m.queue {
				if e.id == request.ID {
					index = i
				}
			}
			invalidSignature := !ed25519.Verify(request.Sender[:], request.SigningBytes(), request.Signature[:])
			switch {
			case invalidSignature:
				want = ErrSignature
			case index < 0:
				if _, ok := m.confirmed[request.ID]; ok {
					want = ErrConfirmed
				} else {
					want = ErrNotFound
				}
			case m.queue[index].sequence != request.Sequence:
				want = ErrConflict
			}
			removed, got := c.Cancel(request)
			if (want == nil) != (got == nil) || (want != nil && !errors.Is(got, want)) {
				t.Fatalf("step %d cancel: want %v got %v", step, want, got)
			}
			if got == nil {
				target := m.queue[index]
				kept := make([]modelEntry, 0)
				expected := []Hash{}
				for _, e := range m.queue {
					if e.sender == target.sender && e.tx.Nonce >= target.tx.Nonce {
						expected = append(expected, e.id)
					} else {
						kept = append(kept, e)
					}
				}
				if len(expected) != len(removed) {
					t.Fatal("cascade count")
				}
				for i, id := range expected {
					if removed[i] != id {
						t.Fatal("cascade order")
					}
				}
				m.queue = kept
			}
		case 5:
			if mined < 4 {
				if _, err = c.Mine(context.Background(), addresses[sender]); err != nil {
					t.Fatal(err)
				}
				m.mine(sender)
				mined++
			}
		case 6:
			if err = c.Close(); err != nil {
				t.Fatal(err)
			}
			m.options = Options{1, 1}
			if action&128 != 0 {
				m.options = Options{4, 2}
			}
			c, err = Open(context.Background(), path, m.options)
			if err != nil {
				t.Fatal(err)
			}
		case 7:
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, err = c.Mine(ctx, addresses[sender]); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		}
		checkModel(t, c, &m, addresses)
	}
}
func TestStateModel(t *testing.T) {
	scenarios := [][]byte{{0, 0, 0, 6, 3, 2, 4, 5, 2, 134, 0, 7}, {32, 5, 8, 8, 8, 3, 4, 5, 6}, {0, 3, 2, 4, 5, 0, 1, 128, 130, 7}}
	for i, data := range scenarios {
		t.Run(fmt.Sprint(i), func(t *testing.T) { runModel(t, data) })
	}
}
func FuzzStateModel(f *testing.F) {
	f.Add([]byte{0, 0, 6, 3, 2, 4, 5, 2, 134, 0, 7})
	f.Add([]byte{32, 5, 8, 0, 3, 4, 5, 6, 128, 130})
	f.Fuzz(func(t *testing.T, data []byte) { runModel(t, data) })
}

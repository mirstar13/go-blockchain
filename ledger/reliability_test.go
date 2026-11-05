package ledger

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestOpenDoesNotInitializeInvalidFile(t *testing.T) {
	for _, data := range [][]byte{nil, []byte("not a database")} {
		path := filepath.Join(t.TempDir(), "invalid.db")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if c, err := Open(context.Background(), path, Options{}); err == nil {
			c.Close()
			t.Fatal("invalid file accepted")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(data) {
			t.Fatal("Open modified rejected file")
		}
	}
	path := filepath.Join(t.TempDir(), "missing.db")
	if _, err := Open(context.Background(), path, Options{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Open created missing file")
	}
}
func TestRejectedBlockPreservesState(t *testing.T) {
	a, k := identity(t)
	b, _ := identity(t)
	state := map[Address]Account{a: {50, 0}}
	before := map[Address]Account{a: {50, 0}}
	parent := Genesis()
	block, err := mine(context.Background(), Block{Version: Version, NetworkID: NetworkID, Height: 1, Previous: mustHash(t, parent), Reward: Reward, RewardTo: a, Difficulty: Difficulty, Transactions: []Transaction{signed(t, k, b, 10, 1), signed(t, k, b, 41, 2)}})
	if err != nil {
		t.Fatal(err)
	}
	if err = applyBlock(state, parent, block); !errors.Is(err, ErrFunds) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("rejected block partially applied")
	}
}

// Exit at the final cancellation checkpoint, after all logical bucket writes
// but before the bbolt Update callback returns and commits. No defers run.
type crashContext struct {
	context.Context
	calls int
}

func (c *crashContext) Err() error {
	c.calls++
	if c.calls == 2 {
		os.Exit(23)
	}
	return nil
}
func TestCrashRecoveryHelper(t *testing.T) {
	stage := os.Getenv("LEDGER_CRASH_STAGE")
	if stage == "" {
		return
	}
	c, err := Open(context.Background(), os.Getenv("LEDGER_CRASH_DB"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := c.Blocks()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := c.Pending()
	if err != nil {
		t.Fatal(err)
	}
	parent := blocks[len(blocks)-1]
	to := pending[0].Sender
	if stage == "before" {
		block, err := mine(context.Background(), Block{Version: Version, NetworkID: NetworkID, Height: parent.Height + 1, Timestamp: parent.Timestamp, Previous: mustHash(t, parent), Transactions: pending, RewardTo: to, Reward: Reward, Difficulty: Difficulty})
		if err != nil {
			t.Fatal(err)
		}
		_ = c.commit(&crashContext{Context: context.Background()}, parent, block, pending)
		t.Fatal("crash checkpoint not reached")
	}
	if stage != "after" {
		t.Fatal(stage)
	}
	if _, err := c.Mine(context.Background(), to); err != nil {
		t.Fatal(err)
	}
	os.Exit(23) // committed, but deliberately never Close the database
}
func TestCrashRecovery(t *testing.T) {
	for _, stage := range []string{"before", "after"} {
		t.Run(stage, func(t *testing.T) {
			c, path := newChain(t)
			a, k := identity(t)
			b, _ := identity(t)
			mustMine(t, c, a)
			transfer := signed(t, k, b, 10, 1)
			if _, err := c.Submit(transfer); err != nil {
				t.Fatal(err)
			}
			if err := c.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestCrashRecoveryHelper$")
			cmd.Env = append(os.Environ(), "LEDGER_CRASH_STAGE="+stage, "LEDGER_CRASH_DB="+path)
			output, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("unexpected child result: %v %s", err, output)
			}
			restored, err := Open(context.Background(), path, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer restored.Close()
			blocks, err := restored.Blocks()
			if err != nil {
				t.Fatal(err)
			}
			pending, err := restored.Pending()
			if err != nil {
				t.Fatal(err)
			}
			alice, err := restored.Balance(a)
			if err != nil {
				t.Fatal(err)
			}
			bob, err := restored.Balance(b)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "before" {
				if len(blocks) != 2 || len(pending) != 1 || pending[0] != transfer || alice != (Account{50, 0}) || bob != (Account{}) {
					t.Fatal("uncommitted state survived crash")
				}
			} else if len(blocks) != 3 || len(pending) != 0 || alice != (Account{90, 1}) || bob != (Account{10, 0}) {
				t.Fatal("committed state lost after crash")
			}
			if err := restored.Validate(context.Background()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

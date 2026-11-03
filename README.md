# go-blockchain

A small single-node persistent account ledger in Go. It supports signed transfers,
a persistent pending queue, fixed-difficulty proof of work, and mining rewards.
It is an educational foundation, not a networked cryptocurrency or production wallet.

## Build and check

Requires Go 1.23 or newer. Direct dependencies are bbolt and `golang.org/x/crypto` (scrypt); `go.sum` pins
checksums. The module path is `github.com/mirstar13/go-blockchain`.

```sh
go build -o ledger-cli ./cmd/ledger
go test ./...
go vet ./...
go test -race ./...
```

If using the prepared cloud environment, activate the Go compiler first:

```sh
export PATH=/workspace/.go-toolchain/go/bin:$PATH
export GOCACHE=/workspace/.go-cache GOMODCACHE=/workspace/.go-modcache
```

## CLI walkthrough

Run commands in a directory where you want to keep the database and keys.
Addresses are 64 hex characters: the Ed25519 public key itself.

```sh
./ledger-cli init --db ledger.db
ALICE=$(./ledger-cli wallet create --out alice.key)
BOB=$(./ledger-cli wallet create --out bob.key)
./ledger-cli wallet address --key alice.key

# Empty blocks are allowed; this creates Alice's first 50 units.
./ledger-cli mine --db ledger.db --reward-to "$ALICE"
./ledger-cli transfer --db ledger.db --key alice.key --to "$BOB" --amount 10
./ledger-cli pending --db ledger.db
./ledger-cli mine --db ledger.db --reward-to "$ALICE"
./ledger-cli balance --db ledger.db --address "$ALICE" # balance 90, nonce 1
./ledger-cli balance --db ledger.db --address "$BOB"   # balance 10, nonce 0
./ledger-cli blocks --db ledger.db
./ledger-cli validate --db ledger.db
```

`transfer` signs and queues; it does not mine. It automatically selects the next
nonce after confirmed and pending outgoing transfers. `mine` creates one block,
with at most 100 transactions in persisted arrival order, then exits. Ctrl+C
cancels without committing a block or consuming queued transfers. An interrupt
arriving after a commit has completed cannot undo that commit. Commands report
errors to stderr with a nonzero exit status. Database paths default to `ledger.db`.
Block and pending JSON displays addresses, hashes, and signatures as hex strings.
Every transaction includes its computed `ID`; every block includes its `Hash`. Empty
transaction lists are `[]`. Balance output includes the address, `Confirmed`,
`Reserved`, `Available`, and the last confirmed sender `Nonce`. Reservations include
pending self-transfers; incoming pending transfers do not increase available funds.
These are CLI presentation fields; stored records and protocol encodings are unchanged.
Scripts consuming the former numeric-array or `Balance` output need updating.

Key files use plaintext seeds by default; optional encrypted files are described below.
Creation uses mode 0600 and
refuses existing paths. Loading rejects group/world-readable files. Keep keys
outside the database and back them up separately; possession authorizes spending.
Initialization refuses an existing database. Other commands require an existing
valid ledger and do not silently create one.

## Protocol version 1

- Network identifier: `go-blockchain-v1`. Copies of this network share transaction
  identity; a separate database is not a separate replay-protection domain.
- Balances, amounts, sender nonces, heights, timestamps, and mining nonces are
  unsigned 64-bit integers. Amounts must be positive. There are no fractional
  units, fees, halvings, genesis allocations, or transaction expiration.
- Accounts start at zero; the first outgoing transaction has nonce 1. Transfers
  require a valid Ed25519 signature, consecutive nonce, and sufficient balance.
  All arithmetic is checked. Self-transfers preserve balance and advance nonce.
- Each non-genesis block credits exactly 50 new units after its transfers. The
  current block's reward cannot fund its own transfers. Rewards have no additional
  maturity delay. The reward is a validated block field, never a queued transaction.
- Pending admission reserves outgoing amounts against confirmed balances. It does
  not count pending incoming payments, including self-transfers. This deliberately
  conservative rule can reject a transfer that would become affordable later.
- Genesis has timestamp/height/nonce zero, zero previous hash and reward address,
  no transfers, and no issued reward. It is deterministic and exempt from mining.
- Blocks require the correct parent hash, next height, nondecreasing Unix timestamp,
  fixed protocol parameters, and SHA-256 hash with 16 leading zero bits. There is
  no wall-clock freshness rule or difficulty adjustment.

### Canonical encoding

All integers are big-endian uint64. Strings are an 8-byte length followed by their
UTF-8 bytes. Addresses and hashes are raw 32 bytes; signatures are raw 64 bytes.
Concatenation below means these encodings in exactly the listed order:

- Signing bytes: string `ledger/transaction/sign/v1`, network string, sender,
  recipient, amount, sender nonce.
- Transaction ID: SHA-256 of string `ledger/transaction/id/v1`, signing bytes,
  signature. The signature signs only the signing bytes.
- Block hash: SHA-256 of string `ledger/block/v1`, version, fixed reward rate (50),
  fixed transaction limit (100), network string, height, timestamp, previous hash,
  transaction count, ordered transaction IDs, reward address, issued reward,
  difficulty (16), mining nonce.

This commits genesis to the issuance and transaction-limit rules even though
it issues no reward. Changing protocol rules requires an explicit version/network
change. Hashes are computed, not stored as mutable independent fields. Public API
results own their data; caller mutations do not modify database contents.

## Storage and validation

bbolt stores version metadata, blocks keyed by height, account state, pending
transactions keyed by increasing arrival sequence, and an ID-to-sequence index.
Records use JSON for storage; JSON is **not** the hashing or signing format.

Opening replays every block, validates genesis and proof of work, checks signatures
and state transitions, compares the account index, and verifies pending reservations
and queue indexes. Corrupt data and unsupported versions fail explicitly, without
silent repair. This full replay favors clarity over startup speed for small ledgers.
It cannot detect a complete internally consistent rewrite by the database owner.

Mining uses a snapshot outside a write transaction. Commit checks that the tip and
selected queue prefix are unchanged, then writes the block, accounts, and queue
removals in one atomic transaction. A stale snapshot returns a conflict rather than
silently mining on an old tip. bbolt locks the file; a second CLI process times out
after one second while it is held. This version deliberately has no background
server. Use the backup command below to create a consistent database snapshot.

## Layout and extension points

- `ledger`: protocol encoding, validation, account state, mining, bbolt storage.
- `wallet`: local key generation/loading and transaction signing.
- `cmd/ledger`: CLI and signal handling.

The old root-level `package main` demo and its API have been replaced. No legacy
stored format existed. Storage is version 1; automatic migrations are not provided.
Tests cover encoding, invalid transfers/blocks, queue reservations and ordering,
reopening/corruption, rollback, cancellation, wallet files, and the CLI workflow.
CI runs formatting, dependency verification, build, vet, tests, and the race detector.

Networking, fork handling, distributed consensus, fees, pruning,
and optimized indexes/startup are deferred. Hash links and proof of work alone do
not supply a multi-node protocol. Add those features only with their own explicit
rules and validation tests.

## Reliability checks

Normal tests include subprocess crash recovery: one process exits after logical
bucket writes but before the mining transaction commits; another exits after
commit without closing the database. Reopening must preserve the old state in the
first case and the complete new block, balances, and queue in the second case.
These test abrupt process exit, not power loss, disk failure, or every possible
interruption inside bbolt's page writes.

Two fuzz targets exercise signed transfer arithmetic/replay invariants and malformed
JSON application records inside structurally valid bbolt databases:

```sh
go test ./ledger -run='^$' -fuzz='^FuzzTransferState$' -fuzztime=30s -parallel=2
go test ./ledger -run='^$' -fuzz='^FuzzStoredRecords$' -fuzztime=30s -parallel=2
```

CI runs each for 10 seconds; seed cases also run with `go test ./...`. Record fuzzing
caps input size at 64 KiB. Arbitrary bbolt page corruption and resource exhaustion
are outside these tests; successful record fuzzing is not a guarantee that any
untrusted database file can be opened safely.

## Backup and restore

```sh
./ledger-cli backup --db ledger.db --out ledger.backup
./ledger-cli restore --from ledger.backup --db restored.db
./ledger-cli validate --db restored.db
```

Backup uses a bbolt read transaction to snapshot blocks, confirmed account state,
metadata, and the pending queue together. The library can take a backup while other
goroutines submit or mine on the same Chain. The CLI still uses exclusive file
locking: a separate running CLI process must finish before another can open it.

Restore copies the backup, validates its full history and indexes, then publishes
a new database. The original backup is never opened for writing. Both commands
require a new destination, refuse existing files and symlinks, and create files
with mode 0600. They write to a temporary sibling, sync and validate it, then
publish using an atomic hard link and sync the parent directory. The destination
filesystem must support hard links and directory syncing. An error after publication
explicitly reports that the file exists but directory syncing failed.

Failed copying or validation removes temporary output and leaves no destination.
Abrupt process termination can leave a `.ledger-snapshot-*` temporary file; it is
not a published backup. Restoration is intended for snapshots made by `backup`,
not an ordinary file copy of a database being written concurrently. Private key
files are separate and must be backed up separately. Backup/restore does not change
the network identity or prevent replay between copies of the same ledger.

## Transaction lookup and cancellation

```sh
./ledger-cli lookup --db ledger.db --id <transaction-id>
./ledger-cli cancel --db ledger.db --id <transaction-id> --key alice.key
```

Lookup returns `pending` or `confirmed`, the readable transaction, and its block
height when confirmed. Unknown or cancelled IDs return an error; there is no
cancellation history or tombstone index. Confirmed lookup scans stored block
records; pending lookup uses the existing ID index.

Cancellation requires the sender's Ed25519 signature. The CLI obtains an admission
sequence and signs a separate `ledger/cancel/v1` message containing the network,
transaction ID, sender, and sequence. Transfer signatures cannot authorize deletion.
The sequence prevents replay of a cancellation against a later admission of the
same signed transfer. This is local queue authorization, not an on-chain record.

Removing a transaction also removes every later pending nonce from that sender,
so the remaining queue has no nonce gaps. Other senders' entries remain in order.
The command reports every removed ID in `Removed`. Deletions, indexes, and released
reservations become visible atomically. Confirmed transactions cannot be cancelled.
If mining commits first cancellation fails; if cancellation changes the selected
queue first the mining commit fails with a conflict. Cancellation does not revoke
the original signed transfer: it can be submitted again while its nonce remains valid.
Restored copies of the same database share identity and may share old queue sequences.

## Encrypted key files

Use an owner-only passphrase file (mode 0600) containing a nonempty passphrase.
The passphrase is read from the file, never from a command-line value. A single
trailing newline (LF or CRLF) is removed; other spaces are significant. File input
is bounded and passphrases are limited to 4096 bytes.

```sh
./ledger-cli wallet create --out encrypted.key --passphrase-file passphrase.txt
./ledger-cli wallet address --key encrypted.key --passphrase-file passphrase.txt
./ledger-cli transfer --db ledger.db --key encrypted.key --passphrase-file passphrase.txt --to <address> --amount 10
./ledger-cli cancel --db ledger.db --id <transaction-id> --key encrypted.key --passphrase-file passphrase.txt

# Create an encrypted copy of an existing plaintext seed, preserving its address.
./ledger-cli wallet encrypt --key alice.key --out encrypted-copy.key --passphrase-file passphrase.txt
```

The versioned envelope uses scrypt (N=32768, r=8, p=1), a fresh 16-byte salt,
AES-256-GCM, and a fresh 12-byte nonce. The version is authenticated. Fixed KDF
parameters and envelope lengths prevent hostile files requesting arbitrary work.
Wrong passwords and authentication failures return an error without exposing keys.
Creation refuses existing files. Legacy plaintext keys remain supported when the
passphrase flag is omitted; there is no silent plaintext fallback when it is supplied.
`wallet encrypt` leaves the original plaintext key in place; protect or retire that
copy deliberately after verifying the encrypted copy. Protect passphrase files and
back them up separately from encrypted keys. There is no password recovery facility.

## Measured tip-read optimization

Mining previously decoded every block twice to retrieve the latest block. It now
uses bbolt's last-key cursor and decodes just the tip. Full replay on opening and
explicit validation remain unchanged; no storage or hash format changed.

A storage microbenchmark on this environment (two 200 ms runs, synthetic block
records, not a consensus-valid history) measured:

| Records | Before | After | Allocated bytes before / after |
| --- | --- | --- | --- |
| 10 | 22.0–22.4 µs/op | 2.49 µs/op | 7,124 / 592 |
| 1,000 | 2.03–2.08 ms/op | 2.58–2.60 µs/op | 507,170 / 641 |

```sh
go test ./ledger -run='^$' -bench=BenchmarkTipRead -benchmem
```

These numbers measure tip retrieval only, not overall mining speed or CLI startup.
Whole-history replay and confirmed-transaction lookup still scale with chain size.

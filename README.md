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

## CLI walkthrough (Linux/Unix; see Windows below)

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
errors to stderr with a nonzero exit status (130 for interruption, 1 for other errors). Database paths default to `ledger.db`.
Block and pending JSON displays addresses, hashes, and signatures as hex strings.
Every transaction includes its computed `ID`; every block includes its `Hash`. Empty
transaction lists are `[]`. Balance output includes the address, `Confirmed`,
`Reserved`, `Available`, and the last confirmed sender `Nonce`. Reservations include
pending self-transfers; incoming pending transfers do not increase available funds.
These are CLI presentation fields; stored records and protocol encodings are unchanged.
Scripts consuming the former numeric-array or `Balance` output need updating.

On Unix, key files use plaintext seeds by default; optional encrypted files are described below.
Windows requires encrypted keys, with plaintext accepted only for explicit migration.
Creation uses mode 0600 and
refuses existing paths. On Unix, loading rejects group/world-readable files. Windows ACL protection is the user’s responsibility. Keep keys
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
CI runs formatting, dependency verification, build, vet, and tests on Linux and
Windows Server 2022. Linux also runs race detection and bounded fuzz campaigns.

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

Fuzz targets exercise signed transfer arithmetic/replay invariants, independent
state-model operation sequences, and malformed
JSON application records inside structurally valid bbolt databases:

```sh
go test ./ledger -run='^$' -fuzz='^FuzzTransferState$' -fuzztime=30s -parallel=2
go test ./ledger -run='^$' -fuzz='^FuzzStoredRecords$' -fuzztime=30s -parallel=2
go test ./ledger -run='^$' -fuzz='^FuzzStateModel$' -fuzztime=30s -fuzzminimizetime=100ms -parallel=2
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
with mode 0600 on Unix (Windows access is controlled by ACLs). They write to a temporary sibling, sync and validate it, then
publish using an atomic hard link. Unix also syncs the parent directory. Windows
omits directory syncing because Go cannot provide it there; power-loss durability
of directory publication is not guaranteed. The destination filesystem must support
hard links, and Unix requires directory syncing. Windows supports local NTFS. An error after publication
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

Use a protected passphrase file (mode 0600 on Unix, restricted ACL on Windows)
containing a nonempty passphrase.
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
Creation refuses existing files. On Unix, legacy plaintext keys remain supported when the
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

## Windows 10 x64

The native target is **Windows 10 22H2 x64**, PowerShell 5.1 or newer, and local NTFS
storage, under a standard user account. Hosted CI runs Windows Server 2022, not
Windows 10; the manual checklist below is required before publishing a release.
ARM64, installers, executable signing, network shares, and non-NTFS filesystems are
not part of this target. The executable is unsigned. Moving current-format files between platforms needs no platform-specific migration.

Build from source with Go 1.23 or newer, or extract the Windows release ZIP:

```powershell
go build -trimpath -o .\ledger.exe ./cmd/ledger
.\ledger.exe --help
```

All Windows commands that create or use keys require `--passphrase-file`. Plaintext
wallet creation and normal plaintext loading fail. `wallet encrypt` is the only
plaintext import path and leaves the original file intact. Encryption protects key
contents; it does not restrict who can read the passphrase file, replace files, or
modify the ledger. Use Windows ACLs to protect the working directory and files.
The application does not set or validate ACLs and does not interpret Unix mode bits
on Windows. Administrator privileges are not required for normal CLI operation.

Create a protected UTF-8 passphrase file without a BOM. Avoid Windows PowerShell
5.1 `Out-File` defaults (UTF-16). This example restricts the file to the current
user before writing its contents and does not put the passphrase in shell history:

```powershell
$pass = Join-Path $PWD 'passphrase.txt'
New-Item -ItemType File -Path $pass -ErrorAction Stop | Out-Null
$identity = [System.Security.Principal.WindowsIdentity]::GetCurrent().Name
icacls $pass /inheritance:r /grant:r "${identity}:(F)"
if ($LASTEXITCODE -ne 0) { throw 'Could not restrict passphrase file access' }
$secret = Read-Host 'Wallet passphrase' -AsSecureString
$ptr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($secret)
try {
    [IO.File]::WriteAllText($pass, [Runtime.InteropServices.Marshal]::PtrToStringBSTR($ptr), [Text.UTF8Encoding]::new($false))
} finally {
    [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($ptr)
    $secret.Dispose()
}
```

A single trailing LF or CRLF is removed from the passphrase; other spaces remain
significant. Keep the passphrase separate from encrypted-key backups. There is no
password recovery. The following commands also demonstrate quoted paths:

```powershell
.\ledger.exe init --db '.\my ledger.db'
$alice = .\ledger.exe wallet create --out '.\alice encrypted.key' --passphrase-file $pass
$bob = .\ledger.exe wallet create --out '.\bob encrypted.key' --passphrase-file $pass
.\ledger.exe mine --db '.\my ledger.db' --reward-to $alice
$tx = .\ledger.exe transfer --db '.\my ledger.db' --key '.\alice encrypted.key' --passphrase-file $pass --to $bob --amount 10
.\ledger.exe lookup --db '.\my ledger.db' --id $tx
.\ledger.exe balance --db '.\my ledger.db' --address $alice
.\ledger.exe cancel --db '.\my ledger.db' --id $tx --key '.\alice encrypted.key' --passphrase-file $pass
.\ledger.exe backup --db '.\my ledger.db' --out '.\ledger snapshot.backup'
.\ledger.exe restore --from '.\ledger snapshot.backup' --db '.\restored ledger.db'
.\ledger.exe validate --db '.\restored ledger.db'

# Import an existing plaintext seed, preserving its address and original file:
.\ledger.exe wallet encrypt --key '.\legacy.key' --out '.\migrated encrypted.key' --passphrase-file $pass
.\ledger.exe wallet address --key '.\migrated encrypted.key' --passphrase-file $pass
```

Check `$LASTEXITCODE` after commands in automation. Backup and restore refuse
existing files, including symlinks, and do not fall back to non-atomic publication.
Symlink creation tests may be skipped when Windows denies the necessary privilege;
file overwrite and publication-race tests still run. Tests cover Unicode/spaced
paths, CRLF input, file locking, subprocess crash recovery, and encrypted workflows.

### Windows release checklist

Pushing `vMAJOR.MINOR.PATCH` runs both CI platforms. After both succeed, CI packages
`ledger.exe` and this README into `go-blockchain-vMAJOR.MINOR.PATCH-windows-amd64.zip`,
generates `SHA256SUMS`, and uploads a **draft** GitHub release. Other version-tag
shapes fail validation; existing published releases are never intentionally updated.
Rerunning the job may replace assets of the same draft. Publication remains manual.

Before publication, extract the ZIP on Windows 10 22H2 x64 as a standard user on
local NTFS. Record the OS build, account type, artifact checksum, date, and each
result in the draft release notes:

1. Verify the ZIP's SHA-256 using `Get-FileHash -Algorithm SHA256`; compare with
   `SHA256SUMS`. Run the extracted executable without installing Go.
2. Create encrypted keys, reload their addresses, reject missing/wrong passwords,
   and migrate a plaintext fixture while retaining its original address and file.
3. Mine funds, queue transfers, inspect IDs and reservations, cancel a transfer with
   later sender nonces, then mine and inspect a confirmed transfer.
4. Exercise Ctrl+C while mining is active (repeat if the fixed-difficulty block
   finishes first). Verify an interrupted uncommitted operation leaves the queue
   and balances unchanged and the ledger still validates.
5. Reopen, back up, restore to a new path, validate, and resume mining. Confirm that
   existing destination files are refused. Include paths with spaces and Unicode.
6. Record any skipped checks or failures; do not publish until required checks pass.

Hosted CI success and Linux cross-compilation alone are not Windows 10 verification.

## Resilience: admission limits and cancellation

Pending admission defaults to 1,000 transfers overall and 100 per sender. Limits
are local policy, not consensus rules, and are not stored in the database. Configure
both for a transfer command when needed:

```sh
./ledger-cli transfer --db ledger.db --key alice.key --to <address> --amount 1 --max-pending 200 --max-pending-per-sender 20
```

Windows additionally requires `--passphrase-file`. Zero selects that field's
default; negative values and per-sender limits exceeding the global limit are
rejected. A global override below 100 therefore needs an explicit compatible
per-sender override. Limits cannot be disabled. The existing signature, duplicate,
nonce, and funds checks precede capacity checks. Capacity is checked atomically
before allocating an arrival sequence or writing queue entries; nothing is evicted.

A valid older queue above the selected caps can still be opened, validated, backed
up, restored, mined, or cancelled. New submissions must satisfy both caps. Mining
and cancellation release capacity; choosing smaller limits never deletes entries.

The library API now requires explicit contexts for opening and maintenance:

```go
chain, err := ledger.Open(ctx, path, ledger.Options{
    MaxPending: 1000,
    MaxPendingPerSender: 100,
})
// Check err, then defer chain.Close().
err = chain.Validate(ctx)
err = chain.Backup(ctx, backupPath)
err = ledger.Restore(ctx, backupPath, restoredPath)
```

`Options{}` selects defaults. Use `context.Background()` for callers that do not
need cancellation. `Mine(ctx, address)` is unchanged. `ErrQueueFull` supports
`errors.Is`; `*QueueFullError` exposes `Scope` (global/sender), `Limit`, and the
current `Count`. Options are associated with an open handle; reopening accepts a
new policy. Protocol identity, hashing, stored-data format, and key envelopes remain unchanged.

Ctrl+C propagates through database opening, validation, mining, backup, and restore.
Validation streams block records and compares account and queue indexes, checking
cancellation between records and state transitions. It still retains account state;
this is not a hard memory bound for arbitrarily large databases. Cancelled opening
closes the database and releases its lock. Context cancellation and deadlines remain
identifiable through `errors.Is` and are not reported as corrupt ledger data.

Snapshot copies check cancellation in chunks of at most 32 KiB and throughout
validation. Before publication, cancellation closes handles, removes temporary
output, and leaves the destination absent. Immediately before linking the snapshot,
the context is checked one final time. After publication succeeds, directory-sync
bookkeeping completes regardless of cancellation; a late interrupt cannot undo a
published snapshot. Directory-sync failures still report that the snapshot exists.
Cancellation is cooperative: an active filesystem call or bbolt lock wait cannot
be forcibly interrupted. Database opening retains its one-second lock timeout.

The independent model tests use their own account and reservation arithmetic to
compare queue order, balances, nonces, and transaction status after submissions,
rejections, cascading cancellations, mining, and reopening with changed limits.
Fuzz inputs are bounded to four accounts, 32 actions, and four mined blocks. Neither
production state-transition helpers nor reservation helpers serve as the model's
oracle. Existing crash, concurrency, Windows, and backup tests remain enabled.

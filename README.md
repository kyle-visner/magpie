# InfoBase

InfoBase is a Phase 1 Go implementation of the PRD in `InfoBase_Requirements.md`.

It is built around one rule: agents and humans use the same CLI, and the CLI enforces RBAC, ledger invariants, encryption, immutable history, and auditability before data is written.

## Current Capabilities

- Canonical local CLI in `cmd/infobase`.
- Custom immutable Merkle-DAG-style storage with SHA-256-addressed JSON nodes.
- AES-256-GCM encryption for stored node payloads.
- Unified RBAC for ledger, notes, snapshots, and audit reads.
- Double-entry ledger validation before persistence.
- Markdown note create, update, list, and get operations.
- Source-tagged journal entries for agent-mapped exports from QuickBooks or other systems.
- Named snapshots for recoverable roots.
- JSON command output by default for agent consumption.
- Automated tests for business invariants and CLI behavior.

## Build And Verify

From the repository root:

```sh
go test ./...
go build -o ./infobase ./cmd/infobase
```

If your environment blocks the default Go build cache, use a writable cache:

```sh
GOCACHE=/private/tmp/infobase-gocache go test ./...
GOCACHE=/private/tmp/infobase-gocache go build -o ./infobase ./cmd/infobase
```

The generated `./infobase` binary is ignored by Git.

## Agent Integration Pattern

Give your agent a fixed command template and tell it to parse stdout as JSON:

```sh
/Users/kylevisner/dev/infobase/infobase \
  --store /Users/kylevisner/dev/infobase/.infobase \
  --actor AGENT_USER_ID \
  COMMAND...
```

For development without building first, use:

```sh
go run ./cmd/infobase \
  --store /Users/kylevisner/dev/infobase/.infobase \
  --actor AGENT_USER_ID \
  COMMAND...
```

Operational rules for agents:

- Treat stdout as the only success channel.
- Treat stderr as JSON error output.
- Never edit `.infobase/` files directly.
- Never invent raw storage mutations.
- Use `ledger account list` before creating journal entries so account IDs are exact.
- Use `note put --body-file FILE` for long note bodies to avoid shell quoting issues.
- Create a `snapshot create --name NAME` before large agent workflows.

Errors look like:

```json
{"code":"permission_denied","message":"role \"Operations\" lacks ledger:read"}
```

## First-Time Setup

Initialize a local store:

```sh
./infobase --store .infobase init
```

The default initialized actor is `owner` with the `Owner` role.

List supported permissions:

```sh
./infobase --store .infobase --actor owner rbac permissions
```

Create an agent user with a constrained role:

```sh
./infobase --store .infobase --actor owner rbac user set \
  --id ops-agent \
  --role Operations
```

Built-in roles:

- `Owner`: full Phase 1 access.
- `Admin`: broad operational access except recovery.
- `Accountant`: ledger, notes read, and audit read.
- `Operations`: notes read/write only.
- `Sales Rep`: notes read/write only.

To define a custom role:

```sh
./infobase --store .infobase --actor owner rbac role set \
  --name "Notes Agent" \
  --permissions notes:read,notes:write
```

Then assign it:

```sh
./infobase --store .infobase --actor owner rbac user set \
  --id notes-agent \
  --role "Notes Agent"
```

## Notes Workflow

Create or update a note:

```sh
./infobase --store .infobase --actor notes-agent note put \
  --title "Ops Handoff" \
  --body "Ship daily closeout."
```

For longer content:

```sh
./infobase --store .infobase --actor notes-agent note put \
  --title "Weekly Review" \
  --body-file ./weekly-review.md \
  --sensitivity internal
```

List notes:

```sh
./infobase --store .infobase --actor notes-agent note list
```

Read a specific note:

```sh
./infobase --store .infobase --actor notes-agent note get --id note:...
```

## Ledger Workflow

Create accounts as an actor with `ledger:write`:

```sh
./infobase --store .infobase --actor owner ledger account create \
  --name Checking \
  --type asset

./infobase --store .infobase --actor owner ledger account create \
  --name "Consulting Revenue" \
  --type revenue
```

List accounts and capture the generated account IDs:

```sh
./infobase --store .infobase --actor owner ledger account list
```

Create a balanced journal entry JSON file:

```json
{
  "date": "2026-06-29",
  "memo": "Invoice paid",
  "source": "quickbooks_export",
  "source_key": "qb-row-123",
  "postings": [
    {
      "account_id": "acct:CHECKING_ID",
      "debit_cents": 125000
    },
    {
      "account_id": "acct:REVENUE_ID",
      "credit_cents": 125000
    }
  ]
}
```

Submit it:

```sh
./infobase --store .infobase --actor owner ledger journal create \
  --file ./journal-entry.json
```

The write is rejected unless total debits exactly equal total credits.

If `source` and `source_key` are both present, InfoBase uses them as an idempotency key. Re-submitting the same source-tagged entry returns the existing entry instead of creating a duplicate.

List journal entries:

```sh
./infobase --store .infobase --actor owner ledger journal list
```

## Agent-Mapped External Exports

InfoBase does not include a QuickBooks-specific CSV/IIF/QBXML parser in the CLI. The agent is responsible for reading exports from QuickBooks or any other external system and mapping them into InfoBase's canonical journal-entry JSON.

The expected agent flow is:

1. Read the external export.
2. Use `ledger account list` to find exact InfoBase account IDs.
3. Build balanced journal-entry JSON.
4. Include `source` and `source_key` from the external row or transaction ID.
5. Submit each canonical entry with `ledger journal create --file FILE`.

Example agent-produced journal entry:

```json
{
  "date": "2026-06-01",
  "memo": "QuickBooks invoice payment INV-1001",
  "source": "quickbooks_export",
  "source_key": "INV-1001-payment",
  "postings": [
    {
      "account_id": "acct:CHECKING_ID",
      "debit_cents": 125000
    },
    {
      "account_id": "acct:CONSULTING_REVENUE_ID",
      "credit_cents": 125000
    }
  ]
}
```

Submit it:

```sh
./infobase --store .infobase --actor owner ledger journal create \
  --file ./agent-mapped-entry.json
```

This keeps InfoBase's CLI narrow and opinionated. The CLI validates permissions, account existence, double-entry balance, source-key idempotency, encryption, and immutable storage; the agent handles source-specific interpretation.

## Snapshots And Audit

Create a named recovery point before a risky workflow:

```sh
./infobase --store .infobase --actor owner snapshot create \
  --name before-agent-ledger-workflow-2026-06-29
```

Read reconstructed state:

```sh
./infobase --store .infobase --actor owner state
```

Read immutable audit nodes:

```sh
./infobase --store .infobase --actor owner audit
```

Both `state` and `audit` require `audit:read`.

## Command Reference

```text
init
state
audit
rbac permissions
rbac role set --name NAME --permissions p1,p2
rbac user set --id ID --role ROLE
ledger account create --name NAME --type TYPE
ledger account list
ledger journal create --file entry.json
ledger journal list
note put --title TITLE --body BODY
note put --title TITLE --body-file FILE
note get --id ID
note list
snapshot create --name NAME
```

Global flags:

```text
--store DIR        store directory, default .infobase
--actor USER_ID    caller identity, default owner
--role ROLE        optional role assertion; must match the actor's assigned role
```

## Storage Format

The default store directory is `.infobase/`.

- `.infobase/objects/nodes/`: immutable JSON node files.
- `.infobase/refs/root`: current live root hash.
- `.infobase/refs/named/`: named snapshot roots.
- `.infobase/keys/data.key`: local AES-256-GCM data key when `INFOBASE_DATA_KEY` is not supplied.

Business payloads are encrypted in node files as:

```json
{
  "sealed_payload": {
    "algorithm": "AES-256-GCM",
    "nonce": "...",
    "ciphertext": "..."
  }
}
```

## Security Notes

The CLI is the only supported interaction path. Business operations call the same storage and RBAC layer used by tests, so ledger invariants and permissions are enforced before persistence.

Phase 1 is local-only. There is no network listener and no transport surface.

Important current limitation: authentication is not implemented yet. The CLI accepts `--actor` as caller context and checks it against stored RBAC assignments, but it does not prove the operating-system user is that actor. For now, run the binary only in trusted local automation or behind a wrapper that authenticates the caller.

Production deployments should provide `INFOBASE_DATA_KEY` from a managed secret store or KMS and keep local key files out of backups.

# InfoBase

InfoBase is a Phase 1 Go implementation of the PRD in `InfoBase_Requirements.md`.

## What Is Implemented

- Canonical CLI in `cmd/infobase`.
- Custom Merkle-DAG-style storage with immutable SHA-256-addressed JSON nodes.
- AES-256-GCM encryption for stored node payloads.
- Unified RBAC for ledger, notes, imports, snapshots, and audit reads.
- Double-entry ledger with hard validation of balanced journal entries.
- Markdown note create, update, list, and get operations.
- QuickBooks CSV import with idempotency keys and balanced ledger mapping.
- Named snapshots for recoverable roots.
- Automated tests for business invariants and CLI behavior.

## Quick Start

```sh
go test ./...
go run ./cmd/infobase --store .infobase init
go run ./cmd/infobase --store .infobase ledger account create --name Checking --type asset
go run ./cmd/infobase --store .infobase note put --title "Ops Handoff" --body "Ship daily closeout."
```

All command output is JSON by default for agent consumption.

## Security Model

The CLI is the only supported interaction path. Business operations call the same storage and RBAC layer used by tests, so ledger invariants and permissions cannot be bypassed through alternate query surfaces.

Payloads are encrypted at rest with AES-256-GCM. By default, a local 32-byte data key is generated at `.infobase/keys/data.key` with restrictive file permissions. Production deployments should provide `INFOBASE_DATA_KEY` from a managed secret store or KMS and keep local key files out of backups.

The implementation is intentionally local-only in Phase 1, so there is no network listener and no transport surface. If an API server is added later, TLS, authn, request logging, and rate limiting should be required before handling sensitive data.

## QuickBooks CSV Format

```csv
date,memo,account,amount_cents,source_id
2026-06-01,Invoice paid,acct:...,125000,qb-1
2026-06-02,SaaS bill,acct:...,-1900,qb-2
```

Positive amounts debit the configured cash account and credit the mapped account. Negative amounts debit the mapped account and credit cash.

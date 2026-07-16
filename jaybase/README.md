# Jaybase

Jaybase is an AI-native information base for replayable, tamper-evident event storage.

It was extracted from InfoBase as a standalone storage project.

The module path is:

```sh
github.com/kyle-visner/jaybase
```

## Scope

- Append-only Merkle-DAG-style JSON nodes.
- SHA-256 content addressing.
- AES-256-GCM encrypted payloads at rest.
- Root refs and named refs.
- Audit traversal from any root.

Domain concepts such as accounting, invoices, RBAC, notes, and payouts live in the accounting CLI project that consumes this module.

## Verify

```sh
go test ./...
```

## License

AGPL-3.0-or-later. See `LICENSE`.

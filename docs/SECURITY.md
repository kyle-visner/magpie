# Security Controls

This document records the Phase 1 controls intended to support SOC 2, PCI DSS, and similar governance programs. It is not a certification claim.

## Implemented Controls

- Least privilege RBAC is enforced before ledger, note, snapshot, and audit operations.
- All business mutations are immutable events in content-addressed nodes.
- Node hashes are verified on read, and tampering fails state reconstruction.
- Node payloads are encrypted at rest with AES-256-GCM.
- Default store directories, node files, refs, and key files use restrictive filesystem permissions.
- Financial journal entries must balance before they can be persisted.
- External source references are structured and encrypted with the account payload.
- Agent-mapped external journal entries are idempotent by source key to reduce duplicate financial records.
- Audit history is reconstructed from the same immutable DAG used as source of truth.
- The Phase 1 CLI has no SQL, graph-query, vector-query, or raw mutation escape hatch.
- Hosted Jaybase access requires a bearer token and HTTPS outside loopback development.
- Hosted appends use optimistic root preconditions and stable idempotency keys, so concurrent changes and ambiguous retries cannot silently duplicate or overwrite events.
- Hosted replay follows the authenticated, payload-explicit, paginated `/v1/events` API rather than reading Jaybase data files directly.

## Production Requirements Before External Deployment

- Provide `JAYBASE_DATA_KEY` from a managed KMS or secret manager.
- Bind authenticated Jaybase principals to allowed Magpie actor identities instead of trusting unrestricted local CLI actor flags.
- Add signed command envelopes for non-interactive agents.
- Add centralized audit export with retention policies.
- Add backup and restore drills for store roots, objects, refs, and keys.
- Add vulnerability scanning and SBOM generation to CI.
- Add formal data classification rules for PCI/cardholder data and other regulated records.

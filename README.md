<p align="center">
  <img src="docs/assets/magpie-logo.png" alt="Magpie logo" width="900">
</p>

# Magpie

[![CI](https://github.com/kyle-visner/magpie/actions/workflows/ci.yml/badge.svg)](https://github.com/kyle-visner/magpie/actions/workflows/ci.yml)

## TL;DR

Magpie is an opinionated accounting CLI for humans and AI agents. It provides
one guarded path for chart-of-accounts management, double-entry journals,
customers, invoices, payouts, notes, snapshots, and audit reads. The CLI checks
RBAC and accounting invariants before appending an encrypted, immutable event to
[Jaybase](https://github.com/kyle-visner/jaybase).

Requires Go 1.26.5 or later. Earlier Go releases include known standard-library
vulnerabilities and must not be used to build release binaries:

```sh
git clone https://github.com/kyle-visner/magpie.git
cd magpie
go install ./cmd/magpie
magpie --store .magpie init
```

The initialized local book uses `cash` accounting and creates an `owner` actor
with the `Owner` role. Read [First-Time Setup](#first-time-setup) before adding
another actor or posting financial activity.

## Who Magpie is for

Magpie is for small teams that want agents to help with bookkeeping without
giving them raw database access or permission to invent accounting behavior. It
fits source-document imports, controlled migrations, invoice and payout
workflows, operational notes, and other jobs where writes must be attributable,
auditable, safe to retry, and rejected when they violate policy.

Magpie is a CLI and domain engine, not a hosted accounting SaaS or a substitute
for professional accounting judgment. Use the local backend for development or
a trusted single-user process. For shared or production use, run Magpie against
a separately deployed Jaybase service and bind authenticated callers to allowed
Magpie actor IDs.

## Status and scope

Magpie is pre-1.0. The implemented accounting surface is usable and tested.
This release includes the capabilities listed below. The following features are
outside its current scope:

- native QuickBooks CSV, IIF, or QBXML parsing; agents must normalize source
  data into Magpie's JSON contracts;
- bills, bank matching or statement-ingestion, tax, loan, transfer,
  fixed-asset, retention, garbage-collection, or point-in-time restore commands;
- note search, backlinks, typed cross-entity references, diff, or graph
  navigation;
- a human-oriented output mode, interactive UI, signed command envelopes, or an
  authentication layer that proves `--actor` identity.

Those are product-scope boundaries, not hidden installation steps. See
[`docs/SECURITY.md`](docs/SECURITY.md) for additional production controls that
remain the deployer's responsibility.

## Project Layout

- The repository root is the accounting CLI project.
- `cmd/magpie` contains the CLI and `internal/magpie` contains the accounting
  domain.
- Jaybase is maintained separately at
  [`github.com/kyle-visner/jaybase`](https://github.com/kyle-visner/jaybase).
  Magpie pins it as a Go module dependency.

## License

AGPL-3.0-or-later. See `LICENSE`.

## Current Capabilities

- Canonical CLI in `cmd/magpie` with local embedded and hosted Jaybase backends.
- Append-only, SHA-256-addressed event history through Jaybase.
- Bearer-authenticated hosted Jaybase access over HTTPS with paginated replay, optimistic concurrency, idempotent writes, and remote named refs.
- AES-256-GCM encryption for stored node payloads.
- Unified RBAC for ledger, notes, snapshots, and audit reads.
- Double-entry ledger validation before persistence.
- Book-level accounting basis support for cash, modified cash, and accrual accounting.
- Chart account roles for workflow-safe account selection.
- Privileged manual journal adjustments with required audit reasons.
- First-class customer, invoice, and payout workflows that generate basis-aware journals.
- Structured external source references on ledger accounts.
- Markdown note create, update, list, and get operations.
- Source-tagged journal entries for agent-mapped exports from QuickBooks or other systems.
- Named snapshots for recoverable roots.
- Append-only period close, privileged audited reopen, closed-period posting protection, and reproducible close packages.
- Deterministic trial balance, profit-and-loss, balance-sheet, and general-ledger reports in JSON and CSV.
- JSON command output by default for agent consumption.
- Automated tests for business invariants, CLI behavior, and BDD-style core scenarios.

## Build and verify

Use Go 1.26.5 or later. From the repository root:

```sh
go mod verify
go test -race ./...
go vet ./...
go build -o ./magpie ./cmd/magpie
go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
govulncheck ./...
```

If your environment blocks the default Go build cache, use a writable cache:

```sh
GOCACHE=/private/tmp/magpie-gocache go mod verify
GOCACHE=/private/tmp/magpie-gocache go test -race ./...
GOCACHE=/private/tmp/magpie-gocache go vet ./...
GOCACHE=/private/tmp/magpie-gocache go build -o ./magpie ./cmd/magpie
```

The generated `./magpie` binary is ignored by Git.

Tagged releases are built from clean `main` commits by
[`release.yml`](.github/workflows/release.yml) using the reproducible
[`scripts/build-release.sh`](scripts/build-release.sh) process. Each GitHub
Release includes macOS and Linux archives for amd64 and arm64 plus a
`SHA256SUMS` file.

## Performance Benchmarks

Run the accounting benchmarks from this repository. Run Jaybase benchmarks from the separate Jaybase repository.

```sh
GOCACHE=/private/tmp/magpie-gocache go test -run '^$' -bench . -benchmem ./...
```

For baseline comparisons, capture multiple runs and compare them with `benchstat`:

```sh
mkdir -p .benchmarks
GOCACHE=/private/tmp/magpie-gocache go test -run '^$' -bench . -benchmem -count 5 ./... > .benchmarks/magpie-main.txt
benchstat .benchmarks/magpie-main.txt .benchmarks/magpie-feature.txt
```

## Agent Integration Pattern

Agents should read [`llm.md`](llm.md) as their operating contract. The short
pattern below shows the required invocation shape.

Give your agent a fixed command template and tell it to parse stdout as JSON:

```sh
./magpie \
  --store .magpie \
  --actor AGENT_USER_ID \
  COMMAND...
```

For the hosted Jaybase service, provide the origin and bearer credential through
the environment. Do not put the token in a command-line flag, URL, payload, log,
or idempotency key:

```sh
export JAYBASE_URL=https://jaybase.example.com
export JAYBASE_TOKEN='writer-token-from-the-secret-manager'

./magpie \
  --actor AGENT_USER_ID \
  COMMAND...
```

`--jaybase-url` may override the origin, but the token is accepted only through
`JAYBASE_TOKEN`. Hosted requests fetch decrypted payloads only when replaying
state; audit output remains metadata-only. Writes use Jaybase's `expected_root`
and `Idempotency-Key` contract and return a conflict instead of overwriting a
newer root.

Magpie can share one linear Jaybase history with Martin. Replay applies the
legacy Magpie node types, skips `martin.*` nodes while still advancing to their
roots, and fails closed for other unknown node types or malformed Magpie
events. `magpie init` adds the Magpie bootstrap after a foreign-only history and
remains idempotent once that bootstrap exists.

For development without building first, use:

```sh
go run ./cmd/magpie \
  --store .magpie \
  --actor AGENT_USER_ID \
  COMMAND...
```

Operational rules for agents:

- Treat stdout as the only success channel.
- Treat stderr as JSON error output.
- Never edit `.magpie/` files directly.
- Never invent raw storage mutations.
- Read `book settings get` before posting financial activity.
- Use the active `accounting_basis` for the whole book; do not choose cash, modified cash, or accrual per transaction.
- Use account roles rather than account names or numbers when deciding what an account means.
- Use `invoice create-json`, `invoice post`, and `invoice mark-paid` for customer invoice activity.
- Do not use generic `ledger journal create` for ordinary operating activity. It is a privileged manual adjustment/import path.
- Use `note put --body-file FILE` for long note bodies to avoid shell quoting issues.
- Create a `snapshot create --name NAME` before large agent workflows.

Errors look like:

```json
{"code":"permission_denied","message":"role \"Operations\" lacks ledger:read"}
```

## First-Time Setup

Initialize a local store:

```sh
./magpie --store .magpie init
```

Or initialize an empty hosted store after setting `JAYBASE_URL` and a writer
`JAYBASE_TOKEN`:

```sh
./magpie --actor owner init
```

The default initialized actor is `owner` with the `Owner` role.

List supported permissions:

```sh
./magpie --store .magpie --actor owner rbac permissions
```

Create an agent user with a constrained role:

```sh
./magpie --store .magpie --actor owner rbac user set \
  --id ops-agent \
  --role Operations
```

Built-in roles:

- `Owner`: full Phase 1 access.
- `Admin`: broad operational access except recovery.
- `Accountant`: ledger, notes read, and audit read.
- `Operations`: notes read/write only.
- `Sales Rep`: notes read/write only.

Stores initialized before new built-in permissions were added can repair default roles without changing custom roles or users:

```sh
./magpie --store .magpie --actor owner rbac defaults repair
```

The repair command requires `rbac:manage`, adds missing current default permissions to built-in roles, and preserves any existing extra permissions on those roles.

To define a custom role:

```sh
./magpie --store .magpie --actor owner rbac role set \
  --name "Notes Agent" \
  --permissions notes:read,notes:write
```

Then assign it:

```sh
./magpie --store .magpie --actor owner rbac user set \
  --id notes-agent \
  --role "Notes Agent"
```

## Notes Workflow

Create or update a note:

```sh
./magpie --store .magpie --actor notes-agent note put \
  --title "Ops Handoff" \
  --body "Ship daily closeout."
```

For longer content:

```sh
./magpie --store .magpie --actor notes-agent note put \
  --title "Weekly Review" \
  --body-file ./weekly-review.md \
  --sensitivity internal
```

List notes:

```sh
./magpie --store .magpie --actor notes-agent note list
```

Read a specific note:

```sh
./magpie --store .magpie --actor notes-agent note get --id note:...
```

## Book Accounting Basis

Magpie is opinionated about accounting methods. The book has exactly one active accounting basis:

- `cash`: recognize income when cash is received and expenses when cash is paid.
- `modified_cash`: cash treatment for ordinary income and expenses, with explicit balance-sheet treatment for sales tax liabilities, payroll tax liabilities, loan principal, and capitalized fixed assets.
- `accrual`: recognize revenue when earned or invoiced and expenses when incurred or billed, using accounts receivable and accounts payable where appropriate.

New stores default to `cash`. Check the current setting before an agent posts entries:

```sh
./magpie --store .magpie --actor owner book settings get
```

Set the accounting basis before entering journal activity:

```sh
./magpie --store .magpie --actor owner book settings set \
  --accounting-basis accrual
```

Changing the accounting basis requires `settings:manage`, which the default `Owner` and `Admin` roles have. Magpie rejects basis changes after journal entries exist, because changing accounting method after postings would require a controlled migration or restatement workflow.

Every new journal entry is stamped with the active `accounting_basis`. If an agent submits a journal entry with an explicit `accounting_basis` that does not match the active book setting, the write is rejected.

Modified cash policy is deliberately narrow:

- Revenue is recognized when cash is received.
- Ordinary expenses are recognized when cash is paid.
- Sales tax and payroll tax are tracked as liabilities.
- Loan principal is tracked as a liability, separate from interest expense.
- Fixed assets are capitalized.
- Inventory, accounts receivable, and accounts payable are not used by default.

For normal service invoices, agents should post according to the active basis:

- Cash or modified cash, when paid: debit cash, credit revenue, credit sales tax payable when collected.
- Accrual, when issued: debit accounts receivable, credit revenue, credit sales tax payable.
- Accrual, when paid: debit cash, credit accounts receivable.

For vendor bills:

- Cash or modified cash: expense when paid, except for explicit modified-cash balance-sheet items such as fixed assets, loans, and taxes.
- Accrual: on bill, debit expense or asset and credit accounts payable; on payment, debit accounts payable and credit cash.

Magpie prevents ordinary agents from bypassing these rules with generic manual journals, and invoice workflows enforce the A/R versus cash-basis posting semantics directly.

## Ledger Workflow

Create accounts as an actor with `ledger:write`. Assigning a role at create time also requires `chart:manage`:

```sh
./magpie --store .magpie --actor owner ledger account create \
  --number 1000 \
  --name Checking \
  --type asset \
  --role bank_account

./magpie --store .magpie --actor owner ledger account create \
  --number 4000 \
  --name "Consulting Revenue" \
  --type revenue \
  --role default_service_revenue
```

Account numbers are optional but first-class. They are stored separately from account IDs, so journal entries continue to reference stable `acct:...` IDs even if an account is renumbered.

Renumber an existing account:

```sh
./magpie --store .magpie --actor owner ledger account number set \
  --account-id acct:CHECKING_ID \
  --number 1010
```

Account number rules:

- Numbers must be unique across accounts when present.
- Numbers may contain digits, hyphens, and dots, e.g. `1000`, `1010.01`, `2000-10`.
- Renumbering creates a versioned account update event.

List accounts and capture the generated account IDs:

```sh
./magpie --store .magpie --actor owner ledger account list
```

## Account Roles

Account roles tell Magpie what an account means inside accounting workflows. Type alone is not enough: an `asset` may be cash, accounts receivable, inventory, a fixed asset, or a contra-asset.

List supported roles:

```sh
./magpie --store .magpie --actor owner ledger account role list
```

Assign or update a role as an actor with `chart:manage`:

```sh
./magpie --store .magpie --actor owner ledger account role set \
  --account-id acct:CHECKING_ID \
  --role operating_cash
```

Role rules:

- Roles must match account type. For example, `accounts_receivable` requires an `asset` account and `sales_tax_payable` requires a `liability` account.
- Roles such as `operating_cash`, `accounts_receivable`, `accounts_payable`, `sales_tax_payable`, `retained_earnings`, and default revenue roles are unique.
- Roles such as `bank_account`, `fixed_asset`, `inventory`, and `default_expense` can be assigned to the accounts they represent when allowed by validation.
- Workflow commands should require roles, not hard-coded account names or chart numbers.

## External Source References

Ledger accounts can carry first-class external source references for bank sync, reconciliation, and migration traceability. This is intentionally stored on the account, not in sidecar notes or name-only conventions.

Example external bank account:

```sh
./magpie --store .magpie --actor owner ledger account create \
  --number 1010 \
  --name "Operating Checking ****1234" \
  --type asset \
  --role bank_account \
  --sensitivity confidential \
  --external-source bank_provider \
  --external-id bank-account-1 \
  --external-type bank_account \
  --external-display-name "Operating Checking" \
  --external-url https://bank.example.com/accounts/bank-account-1 \
  --external-meta account_kind=checking \
  --external-meta "nickname=Operating Checking" \
  --external-meta 'mask=****1234' \
  --external-meta last_four=1234
```

Stored account JSON includes:

```json
{
  "number": "1010",
  "role": "bank_account",
  "external_refs": [
    {
      "source_system": "bank_provider",
      "external_id": "bank-account-1",
      "external_type": "bank_account",
      "display_name": "Operating Checking",
      "url": "https://bank.example.com/accounts/bank-account-1",
      "metadata": {
        "account_kind": "checking",
        "nickname": "Operating Checking",
        "mask": "****1234",
        "last_four": "1234"
      }
    }
  ]
}
```

Example non-bank chart account mapping:

```json
{
  "number": "2300",
  "name": "Sales Tax Payable",
  "type": "liability",
  "sensitivity": "confidential",
  "external_refs": [
    {
      "source_system": "quickbooks",
      "external_id": "42",
      "external_type": "chart_account",
      "display_name": "Sales Tax Payable",
      "metadata": {
        "classification": "liability"
      }
    }
  ]
}
```

Submit JSON account definitions with:

```sh
./magpie --store .magpie --actor owner ledger account create-json \
  --file ./account.json
```

Add or update an external ref on an existing account:

```sh
./magpie --store .magpie --actor owner ledger account external-ref set \
  --account-id acct:OPERATING_BANK_ID \
  --external-source bank_provider \
  --external-id bank-account-1 \
  --external-type bank_account \
  --external-display-name "Operating Checking" \
  --role bank_account \
  --external-meta account_kind=checking \
  --external-meta "nickname=Operating Checking" \
  --external-meta 'mask=****1234' \
  --external-meta last_four=1234
```

`--role` is optional. When present, the actor must have `chart:manage`, and the role/type/uniqueness checks are applied in the same account update as the external reference.

If the account already has a ref with the same `source_system + external_id`, the command replaces that ref. If another account already has that ref, the command fails with a conflict.

Validation rules:

- `--external-source` and `--external-id` are required when any external metadata is provided.
- `--external-source` is normalized to lowercase.
- `--external-url` must be an absolute HTTPS URL.
- `metadata.last_four`, when present, must contain exactly four digits.
- The pair `source_system + external_id` must be unique across ledger accounts.

## Customer And Invoice Workflow

Invoices are first-class source documents. Agents should create customers and invoices, then let Magpie generate workflow-originated journal entries according to the active accounting basis. Magpie is bank and financial-institution agnostic: the AI bookkeeping agent interprets source-specific exports and submits normalized JSON with external references.

Minimum account roles for service invoices:

- `operating_cash` or `bank_account` for the payment/deposit account.
- `default_service_revenue`, `default_product_revenue`, or `other_income` on each invoice line.
- `sales_tax_payable` when the invoice includes sales tax.
- `accounts_receivable` when the book uses `accrual`.

For normalized external imports, line-level `revenue_account_id` may be omitted. Magpie resolves omitted revenue accounts to the configured `default_service_revenue` account and fails the import if that role is not configured. Line-level `tax_amount_cents` may also be supplied; Magpie sums line taxes into invoice-level `tax_amount_cents` when the invoice-level value is omitted, and rejects mismatches when both are present.

Create or update a customer:

```json
{
  "name": "Acme Co",
  "external_refs": [
    {
      "source_system": "billing_platform",
      "external_id": "customer-123",
      "external_type": "customer",
      "display_name": "Acme Co"
    }
  ]
}
```

```sh
./magpie --store .magpie --actor bookkeeping-agent customer create-json \
  --file ./customer.json
```

Create an invoice:

```json
{
  "invoice_number": "INV-1001",
  "customer_id": "cust:ACME_ID",
  "invoice_date": "2026-06-01",
  "due_date": "2026-06-30",
  "line_items": [
    {
      "description": "Services",
      "revenue_account_id": "acct:SERVICE_REVENUE_ID",
      "quantity": 1,
      "unit_amount_cents": 100000,
      "amount_cents": 100000
    }
  ],
  "subtotal_cents": 100000,
  "tax_amount_cents": 8500,
  "total_cents": 108500,
  "external_refs": [
    {
      "source_system": "billing_platform",
      "external_id": "invoice-1001",
      "external_type": "invoice"
    }
  ]
}
```

```sh
./magpie --store .magpie --actor bookkeeping-agent invoice create-json \
  --file ./invoice.json
```

For source imports, prefer one normalized external invoice payload. This keeps source-specific parsing in the agent while giving Magpie a first-class, idempotent workflow:

```json
{
  "post": true,
  "customer": {
    "name": "Acme Co",
    "external_refs": [
      {
        "source_system": "billing_platform",
        "external_id": "customer-123",
        "external_type": "customer",
        "display_name": "Acme Co"
      }
    ]
  },
  "invoice": {
    "invoice_number": "INV-1001",
    "invoice_date": "2026-06-01",
    "due_date": "2026-06-30",
    "status": "paid",
    "line_items": [
      {
        "description": "Services",
        "quantity": 1,
        "unit_amount_cents": 100000,
        "amount_cents": 100000,
        "tax_amount_cents": 8500
      }
    ],
    "subtotal_cents": 100000,
    "total_cents": 108500,
    "external_refs": [
      {
        "source_system": "billing_platform",
        "external_id": "invoice-1001",
        "external_type": "invoice"
      }
    ]
  },
  "payment": {
    "date": "2026-06-15",
    "amount_cents": 108500,
    "cash_account_id": "acct:OPERATING_CASH_ID",
    "external_source": "bank_feed",
    "external_id": "txn-123",
    "payment_evidence": "external_transaction_match"
  }
}
```

```sh
./magpie --store .magpie --actor bookkeeping-agent invoice import-json \
  --file ./external-invoice.json
```

`invoice import-json` upserts the customer by external reference, creates or reuses the invoice by external reference, posts it when `post` is true or the external status is `open`/`paid`, and marks it paid only when payment evidence and a cash/bank account are provided. A `paid` import without payment data is rejected instead of recording a paid status without accounting evidence.

Post the invoice:

```sh
./magpie --store .magpie --actor bookkeeping-agent invoice post \
  --invoice-id inv:...
```

Posting behavior:

- `cash` and `modified_cash`: opens the invoice but does not create A/R or revenue journals yet.
- `accrual`: creates a workflow journal for invoice issue: debit `accounts_receivable`, credit revenue, and credit `sales_tax_payable` when tax is present.

Mark the invoice paid:

```sh
./magpie --store .magpie --actor bookkeeping-agent invoice mark-paid \
  --invoice-id inv:... \
  --cash-account-id acct:OPERATING_CASH_ID \
  --paid-date 2026-06-15 \
  --amount-cents 108500 \
  --external-source bank_feed \
  --external-id txn-123 \
  --payment-evidence external_transaction_match
```

Payment behavior:

- `cash` and `modified_cash`: creates a workflow journal that debits cash, credits revenue, and credits `sales_tax_payable` when tax is present. A/R is not used.
- `accrual`: requires the invoice to be posted first, then creates a workflow journal that debits cash and credits `accounts_receivable`.

Reverse an incorrect payment:

```sh
./magpie --store .magpie --actor bookkeeping-agent invoice reverse-payment \
  --invoice-id inv:... \
  --payment-id pay:... \
  --reversal-date 2026-06-16 \
  --reason "invoice was incorrectly marked paid"
```

Payment reversals create a new workflow journal that exactly offsets the original payment journal. The original payment and journal remain in the audit trail; the payment is marked reversed and the invoice status is recomputed from non-reversed payments.

Workflow journals are stored with `origin: "workflow"`, `workflow`, `posting_semantics`, `source_document_type`, and `source_document_id`. Invoice workflow writes are idempotent by source key so agents can retry safely.

List source documents:

```sh
./magpie --store .magpie --actor bookkeeping-agent customer get --customer-id cust:...
./magpie --store .magpie --actor bookkeeping-agent customer list
./magpie --store .magpie --actor bookkeeping-agent invoice get --invoice-id inv:...
./magpie --store .magpie --actor bookkeeping-agent invoice list
```

## Payout And External Transfer Workflow

Payouts are first-class source documents for provider deposits and other external transfers into a bank account. Magpie does not parse provider exports directly. The AI bookkeeping agent interprets source-specific data, maps it to existing Magpie accounts, and submits normalized JSON with external references.

Minimum account roles:

- `operating_cash` or `bank_account` for the destination bank account.
- `merchant_fees_expense` when the payout includes processing fees.

The source account is an asset account representing the clearing, processor, or external-transfer balance being reduced. It does not need a provider-specific role.

Import a normalized payout:

```json
{
  "date": "2026-06-18",
  "description": "Processor batch 2026-06-18",
  "source_account_id": "acct:PROCESSOR_CLEARING_ID",
  "destination_account_id": "acct:OPERATING_BANK_ID",
  "net_amount_cents": 232518,
  "fee_amount_cents": 1000,
  "fee_expense_account_id": "acct:MERCHANT_FEES_ID",
  "external_refs": [
    {
      "source_system": "payment_processor",
      "external_id": "payout-1001",
      "external_type": "payout",
      "metadata": {
        "destination_account_id": "external-bank-1"
      }
    }
  ]
}
```

```sh
./magpie --store .magpie --actor bookkeeping-agent payout import-json \
  --file ./payout.json
```

`payout import-json` stores the payout source document idempotently by `external_refs`, then creates workflow journals:

- `payout.receive`: debit destination bank account, credit source/clearing account for `net_amount_cents`.
- `payout.fee`: when `fee_amount_cents` is present, debit merchant fee expense, credit source/clearing account.

Workflow journals are stamped with the active accounting basis, source document metadata, and source keys. Agents can retry the same payout import safely without creating duplicate payout documents or journals. The command requires `ledger:write`, not `journal:adjust`.

List payout source documents:

```sh
./magpie --store .magpie --actor bookkeeping-agent payout get --payout-id payout:...
./magpie --store .magpie --actor bookkeeping-agent payout list
```

## Manual Journal Adjustments

Generic journal creation is restricted. It requires both `ledger:write` and `journal:adjust`, and it must include a `manual_reason`. Default `Owner` and `Admin` roles have `journal:adjust`; ordinary bookkeeping agents should not.

Manual journals are for controlled adjustments, opening/import work, and emergency correction workflows until first-class domain workflows exist. Future bill, bank-match, tax, loan, transfer, and fixed-asset commands should generate workflow-originated journals instead of asking agents to hand-author postings.

Create a balanced manual journal JSON file:

```json
{
  "date": "2026-06-29",
  "memo": "Opening import adjustment",
  "manual_reason": "Owner-approved migration from legacy bookkeeping export",
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
./magpie --store .magpie --actor owner ledger journal create \
  --file ./journal-entry.json
```

The write is rejected unless total debits exactly equal total credits.

If `source` and `source_key` are both present, Magpie uses them as an idempotency key. Re-submitting the same source-tagged entry returns the existing entry instead of creating a duplicate.

Manual entries are stored with:

```json
{
  "origin": "manual_adjustment",
  "accounting_basis": "cash",
  "manual_reason": "Owner-approved migration from legacy bookkeeping export"
}
```

List journal entries:

```sh
./magpie --store .magpie --actor owner ledger journal list
```

## Agent-Mapped External Exports

Magpie does not include a QuickBooks-specific CSV/IIF/QBXML parser in the CLI. The agent is responsible for reading exports from QuickBooks or any other external system and mapping them into Magpie's canonical manual journal JSON when doing a controlled migration or adjustment.

The expected agent flow is:

1. Read the external export.
2. Use `ledger account list` to find exact Magpie account IDs.
3. Build balanced manual journal JSON with `manual_reason`.
4. Include `source` and `source_key` from the external row or transaction ID.
5. Submit each canonical entry with `ledger journal create --file FILE`.

Example agent-produced journal entry:

```json
{
  "date": "2026-06-01",
  "memo": "QuickBooks invoice payment INV-1001",
  "manual_reason": "Owner-approved migration from QuickBooks export",
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
./magpie --store .magpie --actor owner ledger journal create \
  --file ./agent-mapped-entry.json
```

This keeps Magpie's CLI narrow and opinionated. The CLI validates permissions, account existence, double-entry balance, source-key idempotency, manual-journal authorization, encryption, and immutable storage; the agent handles source-specific interpretation. Ordinary ongoing bookkeeping should move to domain workflows rather than generic manual journals.

## Snapshots And Audit

Create a named recovery point before a risky workflow:

```sh
./magpie --store .magpie --actor owner snapshot create \
  --name before-agent-ledger-workflow-2026-06-29
```

Read reconstructed state:

```sh
./magpie --store .magpie --actor owner state
```

Read immutable audit nodes:

```sh
./magpie --store .magpie --actor owner audit
```

Both `state` and `audit` require `audit:read`.

## Period close and reports

Preview every close before completing it:

```sh
./magpie --store .magpie --actor owner period close preview --through 2026-06-30
./magpie --store .magpie --actor owner period close complete --through 2026-06-30
./magpie --store .magpie --actor owner period close package \
  --through 2026-06-30 --output-dir ./close-2026-06
```

Preview fails closed on staged invoices, invalid or missing workflow account
roles, malformed journals, and unreconciled accounts carrying the
`operating_cash` or `bank_account` role. Because Magpie does not ingest bank
statements, reconciliation is an evidence marker on an account external ref:
`--external-meta reconciled_through=YYYY-MM-DD`. The caller is responsible for
setting that marker only after reconciling source evidence.

A completed close appends an immutable `period.close` event, creates a named
Jaybase ref, and prevents journals dated on or before the close date. Reopening
requires `period:reopen` and a non-empty reason:

```sh
./magpie --store .magpie --actor owner period reopen \
  --through 2026-06-30 --reason "late bank statement correction"
```

The original close remains in history. The next close for that date is a new
revision linked to the original and its reopen reason. A close package contains
canonical JSON and CSV reports plus `manifest.json`; the manifest records the
pre-close Jaybase root, basis, account set, parameters, artifact SHA-256 hashes,
snapshot name, actor, timestamp, and correction lineage.

Reports can also be produced independently:

```sh
./magpie --store .magpie --actor owner report trial-balance --as-of 2026-06-30 --format json
./magpie --store .magpie --actor owner report profit-loss --from 2026-06-01 --through 2026-06-30 --format csv
./magpie --store .magpie --actor owner report balance-sheet --as-of 2026-06-30 --format json
./magpie --store .magpie --actor owner report general-ledger --from 2026-06-01 --through 2026-06-30 --format csv
```

## Command Reference

```text
init
state
audit
book settings get
book settings set --accounting-basis cash|modified_cash|accrual
customer create-json --file customer.json
customer get --customer-id ID
customer list
invoice create-json --file invoice.json
invoice import-json --file external-invoice.json
invoice post --invoice-id ID
invoice mark-paid --invoice-id ID --cash-account-id ID --paid-date YYYY-MM-DD --amount-cents N
invoice reverse-payment --invoice-id ID --payment-id ID --reversal-date YYYY-MM-DD --reason REASON
invoice get --invoice-id ID
invoice list
rbac defaults repair
rbac permissions
rbac role set --name NAME --permissions p1,p2
rbac user set --id ID --role ROLE
ledger account create --name NAME --type TYPE [--number NUMBER] [--role ROLE]
ledger account create-json --file account.json
ledger account number set --account-id ID --number NUMBER
ledger account role list
ledger account role set --account-id ID --role ROLE
ledger account external-ref set --account-id ID --external-source SOURCE --external-id ID [--role ROLE]
ledger account list
ledger journal create --file entry.json
ledger journal list
note put --title TITLE --body BODY
note put --title TITLE --body-file FILE
note get --id ID
note list
snapshot create --name NAME
period close preview --through YYYY-MM-DD
period close complete --through YYYY-MM-DD
period reopen --through YYYY-MM-DD --reason REASON
period close package --through YYYY-MM-DD --output-dir DIR
report trial-balance --as-of YYYY-MM-DD --format json|csv
report profit-loss --from YYYY-MM-DD --through YYYY-MM-DD --format json|csv
report balance-sheet --as-of YYYY-MM-DD --format json|csv
report general-ledger --from YYYY-MM-DD --through YYYY-MM-DD --format json|csv
```

Global flags:

```text
--store DIR        store directory, default .magpie
--jaybase-url URL  hosted Jaybase HTTPS origin, default JAYBASE_URL
--actor USER_ID    caller identity, default owner
--role ROLE        optional role assertion; must match the actor's assigned role
```

`--store` and hosted mode are mutually exclusive. HTTP is accepted only for a
loopback development server; non-loopback Jaybase origins must use HTTPS.

## Storage Backends

In hosted mode, Magpie uses only Jaybase's authenticated `/v1` API. It does not
read or mount the server's data volume, refs, or encryption key. Jaybase owns
transport authentication, concurrent append serialization, encryption at rest,
and hosted snapshots/backups.

Each hosted command currently reconstructs state by fetching and decrypting the
complete event history. This keeps local and hosted behavior identical, but its
latency, bandwidth, and server work grow linearly with history size. Large or
high-frequency ledgers will need incremental state caching, compaction, or a
server-side materialized-state endpoint before this backend scales efficiently.
Hosted replay currently requests payloads for the complete event page, so
Jaybase decrypts foreign payloads before Magpie can classify them. A corrupt or
key-mismatched foreign payload therefore fails the entire hosted replay page
closed and can prevent Magpie state loading or initialization. Metadata-first
replay with selective payload fetch for Magpie-owned types remains required to
remove that availability coupling. Local replay classifies `martin.*` nodes
from metadata and does not decrypt their payloads.

In local mode, the default store directory is `.magpie/`:

- `.magpie/objects/nodes/`: immutable JSON node files.
- `.magpie/refs/root`: current live root hash.
- `.magpie/refs/named/`: named snapshot roots.
- `.magpie/keys/data.key`: local AES-256-GCM data key when `JAYBASE_DATA_KEY` is not supplied.

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

The CLI is the only supported Magpie interaction path. Business operations call
the same accounting and RBAC layer in local and hosted modes, so ledger
invariants and permissions are enforced before persistence.

Hosted Jaybase authenticates and authorizes storage access with bearer
credentials. It does not authenticate Magpie's domain-level `--actor`: the CLI
still accepts that caller context and checks it against Magpie RBAC assignments
without proving that the token principal is the same person. Run Magpie only in
trusted automation or behind a wrapper that binds the authenticated caller to
the permitted `--actor` value.

Keep `JAYBASE_TOKEN` in a secret manager, grant the lowest hosted role needed,
and never pass it on the command line. Local production deployments should
provide `JAYBASE_DATA_KEY` from a managed secret store or KMS and keep local key
files out of backups. Hosted deployments manage the data key and encrypted
backups on the Jaybase server instead.

## Contributing

Run the release checks in [Build and verify](#build-and-verify) before opening a
change. Bug reports and focused proposals are welcome in
[GitHub Issues](https://github.com/kyle-visner/magpie/issues). Review
[`docs/SECURITY.md`](docs/SECURITY.md) before production use, and do not put
credentials or confidential financial data in a public issue.

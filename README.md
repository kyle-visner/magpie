# InfoBase

InfoBase is a Phase 1 Go implementation of the PRD in `InfoBase_Requirements.md`.

It is built around one rule: agents and humans use the same CLI, and the CLI enforces RBAC, ledger invariants, encryption, immutable history, and auditability before data is written.

## Current Capabilities

- Canonical local CLI in `cmd/infobase`.
- Custom immutable Merkle-DAG-style storage with SHA-256-addressed JSON nodes.
- AES-256-GCM encryption for stored node payloads.
- Unified RBAC for ledger, notes, snapshots, and audit reads.
- Double-entry ledger validation before persistence.
- Book-level accounting basis support for cash, modified cash, and accrual accounting.
- Chart account roles for workflow-safe account selection.
- Privileged manual journal adjustments with required audit reasons.
- First-class customer and invoice workflows that generate basis-aware journals.
- Structured external source references on ledger accounts.
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

Stores initialized before new built-in permissions were added can repair default roles without changing custom roles or users:

```sh
./infobase --store .infobase --actor owner rbac defaults repair
```

The repair command requires `rbac:manage`, adds missing current default permissions to built-in roles, and preserves any existing extra permissions on those roles.

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

## Book Accounting Basis

InfoBase is opinionated about accounting methods. The book has exactly one active accounting basis:

- `cash`: recognize income when cash is received and expenses when cash is paid.
- `modified_cash`: cash treatment for ordinary income and expenses, with explicit balance-sheet treatment for sales tax liabilities, payroll tax liabilities, loan principal, and capitalized fixed assets.
- `accrual`: recognize revenue when earned or invoiced and expenses when incurred or billed, using accounts receivable and accounts payable where appropriate.

New stores default to `cash`. Check the current setting before an agent posts entries:

```sh
./infobase --store .infobase --actor owner book settings get
```

Set the accounting basis before entering journal activity:

```sh
./infobase --store .infobase --actor owner book settings set \
  --accounting-basis accrual
```

Changing the accounting basis requires `settings:manage`, which the default `Owner` and `Admin` roles have. InfoBase rejects basis changes after journal entries exist, because changing accounting method after postings would require a controlled migration or restatement workflow.

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

InfoBase prevents ordinary agents from bypassing these rules with generic manual journals, and invoice workflows enforce the A/R versus cash-basis posting semantics directly.

## Ledger Workflow

Create accounts as an actor with `ledger:write`. Assigning a role at create time also requires `chart:manage`:

```sh
./infobase --store .infobase --actor owner ledger account create \
  --number 1000 \
  --name Checking \
  --type asset \
  --role bank_account

./infobase --store .infobase --actor owner ledger account create \
  --number 4000 \
  --name "Consulting Revenue" \
  --type revenue \
  --role default_service_revenue
```

Account numbers are optional but first-class. They are stored separately from account IDs, so journal entries continue to reference stable `acct:...` IDs even if an account is renumbered.

Renumber an existing account:

```sh
./infobase --store .infobase --actor owner ledger account number set \
  --account-id acct:CHECKING_ID \
  --number 1010
```

Account number rules:

- Numbers must be unique across accounts when present.
- Numbers may contain digits, hyphens, and dots, e.g. `1000`, `1010.01`, `2000-10`.
- Renumbering creates a versioned account update event.

List accounts and capture the generated account IDs:

```sh
./infobase --store .infobase --actor owner ledger account list
```

## Account Roles

Account roles tell InfoBase what an account means inside accounting workflows. Type alone is not enough: an `asset` may be cash, accounts receivable, inventory, a fixed asset, or a contra-asset.

List supported roles:

```sh
./infobase --store .infobase --actor owner ledger account role list
```

Assign or update a role as an actor with `chart:manage`:

```sh
./infobase --store .infobase --actor owner ledger account role set \
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
./infobase --store .infobase --actor owner ledger account create \
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
./infobase --store .infobase --actor owner ledger account create-json \
  --file ./account.json
```

Add or update an external ref on an existing account:

```sh
./infobase --store .infobase --actor owner ledger account external-ref set \
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

Invoices are first-class source documents. Agents should create customers and invoices, then let InfoBase generate workflow-originated journal entries according to the active accounting basis. InfoBase is bank and financial-institution agnostic: the AI bookkeeping agent interprets source-specific exports and submits normalized JSON with external references.

Minimum account roles for service invoices:

- `operating_cash` or `bank_account` for the payment/deposit account.
- `default_service_revenue`, `default_product_revenue`, or `other_income` on each invoice line.
- `sales_tax_payable` when the invoice includes sales tax.
- `accounts_receivable` when the book uses `accrual`.

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
./infobase --store .infobase --actor bookkeeping-agent customer create-json \
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
./infobase --store .infobase --actor bookkeeping-agent invoice create-json \
  --file ./invoice.json
```

For source imports, prefer one normalized external invoice payload. This keeps source-specific parsing in the agent while giving InfoBase a first-class, idempotent workflow:

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
./infobase --store .infobase --actor bookkeeping-agent invoice import-json \
  --file ./external-invoice.json
```

`invoice import-json` upserts the customer by external reference, creates or reuses the invoice by external reference, posts it when `post` is true or the external status is `open`/`paid`, and marks it paid only when payment evidence and a cash/bank account are provided. A `paid` import without payment data is rejected instead of recording a paid status without accounting evidence.

Post the invoice:

```sh
./infobase --store .infobase --actor bookkeeping-agent invoice post \
  --invoice-id inv:...
```

Posting behavior:

- `cash` and `modified_cash`: opens the invoice but does not create A/R or revenue journals yet.
- `accrual`: creates a workflow journal for invoice issue: debit `accounts_receivable`, credit revenue, and credit `sales_tax_payable` when tax is present.

Mark the invoice paid:

```sh
./infobase --store .infobase --actor bookkeeping-agent invoice mark-paid \
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

Workflow journals are stored with `origin: "workflow"`, `workflow`, `posting_semantics`, `source_document_type`, and `source_document_id`. Invoice workflow writes are idempotent by source key so agents can retry safely.

List source documents:

```sh
./infobase --store .infobase --actor bookkeeping-agent customer get --customer-id cust:...
./infobase --store .infobase --actor bookkeeping-agent customer list
./infobase --store .infobase --actor bookkeeping-agent invoice get --invoice-id inv:...
./infobase --store .infobase --actor bookkeeping-agent invoice list
```

## Manual Journal Adjustments

Generic journal creation is restricted. It requires both `ledger:write` and `journal:adjust`, and it must include a `manual_reason`. Default `Owner` and `Admin` roles have `journal:adjust`; ordinary bookkeeping agents should not.

Manual journals are for controlled adjustments, opening/import work, and emergency correction workflows until first-class domain workflows exist. Future invoice, bill, bank-match, tax, loan, transfer, and fixed-asset commands should generate workflow-originated journals instead of asking agents to hand-author postings.

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
./infobase --store .infobase --actor owner ledger journal create \
  --file ./journal-entry.json
```

The write is rejected unless total debits exactly equal total credits.

If `source` and `source_key` are both present, InfoBase uses them as an idempotency key. Re-submitting the same source-tagged entry returns the existing entry instead of creating a duplicate.

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
./infobase --store .infobase --actor owner ledger journal list
```

## Agent-Mapped External Exports

InfoBase does not include a QuickBooks-specific CSV/IIF/QBXML parser in the CLI. The agent is responsible for reading exports from QuickBooks or any other external system and mapping them into InfoBase's canonical manual journal JSON when doing a controlled migration or adjustment.

The expected agent flow is:

1. Read the external export.
2. Use `ledger account list` to find exact InfoBase account IDs.
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
./infobase --store .infobase --actor owner ledger journal create \
  --file ./agent-mapped-entry.json
```

This keeps InfoBase's CLI narrow and opinionated. The CLI validates permissions, account existence, double-entry balance, source-key idempotency, manual-journal authorization, encryption, and immutable storage; the agent handles source-specific interpretation. Ordinary ongoing bookkeeping should move to domain workflows rather than generic manual journals.

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
book settings get
book settings set --accounting-basis cash|modified_cash|accrual
customer create-json --file customer.json
customer get --customer-id ID
customer list
invoice create-json --file invoice.json
invoice import-json --file external-invoice.json
invoice post --invoice-id ID
invoice mark-paid --invoice-id ID --cash-account-id ID --paid-date YYYY-MM-DD --amount-cents N
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

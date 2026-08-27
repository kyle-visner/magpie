# Magpie agent guide

Use this document as the operating contract for an AI agent that installs or
uses Magpie. For product context, human setup, examples, and deployment
boundaries, read [`README.md`](README.md) and
[`docs/SECURITY.md`](docs/SECURITY.md).

## What Magpie is

Magpie is an opinionated accounting CLI **and MCP server** backed by an append-only Jaybase event
history. Humans and agents use the same domain operations. The CLI and the MCP
server are two adapters over that API. They do not wrap each other. The domain layer enforces
role-based permissions, accounting-basis rules, account roles, balanced
double-entry journals, source-document workflow rules, and source-key
idempotency before a write is appended. It also enforces append-only period
closes and produces root-addressed standard financial reports.

Magpie does not authenticate a human, decide whether source evidence is true,
provide accounting or tax advice, or parse provider-specific exports. The agent
must validate source evidence, normalize it into Magpie's documented JSON
contracts, use existing account IDs and roles, and obtain human approval when
the accounting treatment is uncertain.

Never read or edit `.magpie/`, Jaybase data files, refs, or encryption keys
directly. The CLI and the MCP server are the supported interfaces.

## Two interfaces

Use the CLI when a human or a local script is driving Magpie. Use MCP when an
existing agent (ChatGPT, Claude, Grok, Cursor, Hermes, or any MCP client) should
keep the books.

```sh
# Local stdio MCP (Claude Desktop, Cursor, Hermes)
magpie --store /absolute/path/to/book.magpie --actor owner mcp

# Remote Streamable HTTP MCP (ChatGPT / Claude / Grok connectors)
export MAGPIE_MCP_TOKEN='connector-token-from-the-secret-manager'
magpie --actor owner mcp --http 127.0.0.1:8787
```

HTTP MCP requires `MAGPIE_MCP_TOKEN` or `MAGPIE_MCP_TOKEN_FILE`. That token authenticates
the connector. It is not `JAYBASE_TOKEN`. Hosted Jaybase accepts `JAYBASE_TOKEN` or
`JAYBASE_TOKEN_FILE`. The process `--actor` is bound at
server start; tools cannot change it. Hosted Jaybase credentials stay on the
host and are never accepted from an MCP client.

MCP tools are the Magpie command map (`book_settings_get`, `ledger_account_list`,
`invoice_import`, …). Arguments are JSON objects, not argv and not files.

## Install

Magpie requires Go 1.26.6 or later. Earlier Go releases must not be used to
build Magpie release binaries. Install it from a repository checkout:

```sh
git clone https://github.com/kyle-visner/magpie.git
cd magpie
go install ./cmd/magpie
magpie help
```

For repository-local development, replace `magpie` in the examples with:

```sh
go run ./cmd/magpie
```

Magpie is pre-1.0 and has no `version` command. Pin the repository revision used
by automation and review release notes before upgrading.

## Choose one storage mode

Local mode stores the book in a directory:

```sh
magpie --store /absolute/path/to/book.magpie --actor owner COMMAND...
```

The default local directory is `.magpie`. Use an absolute path in unattended
automation so the working directory cannot select the wrong book.

Hosted mode uses the authenticated Jaybase API:

```sh
export JAYBASE_URL=https://jaybase.example.com
export JAYBASE_TOKEN='writer-token-from-the-secret-manager'

magpie --actor owner COMMAND...
```

Rules for hosted mode:

- `JAYBASE_URL` must be an HTTPS origin with no credentials, path, query, or
  fragment. Plain HTTP is accepted only for a loopback development server.
- `JAYBASE_TOKEN` is required and is accepted only through the environment.
- Never put the token in a URL, argument, input file, payload, log, prompt
  transcript, or idempotency key.
- Do not combine an explicit `--store` with `JAYBASE_URL` or `--jaybase-url`.
- Use the lowest Jaybase credential role that can complete the task.
- Hosted commands scan the complete event metadata chain, bounded to one
  captured root, and retrieve decrypted payloads only for Magpie-owned events
  through Jaybase's selective payload endpoint. Accepted foreign namespaces
  advance the shared root without fetching their payloads. Metadata scanning
  and selected-payload retrieval still grow with history size.

Magpie's `--actor` is a domain identity, not proof of authentication. Jaybase
authenticates the bearer token but does not prove that it belongs to the actor
named on the command line. Production automation must run behind a trusted
wrapper that binds each authenticated caller to its allowed Magpie actor ID.

Global flags must appear before the command:

```text
--store DIR
--jaybase-url HTTPS_ORIGIN
--actor USER_ID
--role ROLE
```

Omit `--role` normally. If supplied, it must exactly match the role assigned to
the actor; it cannot elevate privileges.

## Initialize and inspect a book

Initialize once in the selected storage mode:

```sh
magpie --store /absolute/path/to/book.magpie --actor owner init
```

Initialization is idempotent. It creates the `owner` user with the `Owner` role
and sets the book to `cash` accounting. A shared Jaybase history may already
contain `martin.*` events; Magpie adds its own bootstrap and ignores those
foreign events while preserving the shared root.

Before any financial workflow, inspect:

```sh
magpie --store /absolute/path/to/book.magpie --actor owner book settings get
magpie --store /absolute/path/to/book.magpie --actor owner ledger account list
```

Do not infer the accounting basis from a business description. The
`accounting_basis` returned by `book settings get` governs the whole book.
Magpie rejects changing it after journal entries exist.

## Output and error contract

Successful commands write one JSON value to stdout. Parse stdout; do not scrape
human prose. Failures exit nonzero and write one JSON object to stderr:

```json
{"code":"permission_denied","message":"role \"Operations\" lacks ledger:read"}
```

Common codes include:

- `validation_error`: fix the input; do not retry unchanged.
- `permission_denied`: stop and use an authorized actor or credential.
- `not_found`: refresh the relevant ID, external reference, root, or snapshot
  assumption.
- `conflict`: reload state and re-evaluate the operation; never blindly loop.
- `integrity_error`: stop writes and alert an operator.
- `capacity_exceeded`: stop and have an operator restore storage capacity.
- `internal_error`: retry only when the operation has a stable domain identity,
  then alert an operator if it persists.

The CLI has no human-readable output mode. `magpie help` prints the top-level
command list; use this guide and the README for command-specific contracts.

## Non-negotiable accounting rules

- Represent money as integer cents. Never use floating-point dollars.
- Use `YYYY-MM-DD` for accounting dates.
- Use returned opaque IDs such as `acct:...`, `cust:...`, `inv:...`, and
  `payout:...`; never construct or guess them.
- Select accounts by their typed `role`, then use the returned account ID.
  Display names and chart numbers are not semantic contracts.
- Never change the book's accounting basis per transaction.
- Prefer first-class invoice, payout, and bank commands for ordinary activity.
- Generic `ledger journal create` is a privileged manual adjustment or import
  path. It requires `ledger:write`, `journal:adjust`, a balanced entry, and a
  nonempty `manual_reason`.
- Before a large or risky workflow, create a named snapshot. A Magpie snapshot
  is a root checkpoint, not an off-host backup.
- Corrections preserve history. Reverse an invoice payment with
  `invoice reverse-payment`; do not attempt to edit or delete the original
  payment or journal.
- Import institution-specific statement data only after normalizing it to the
  provider-neutral bank JSON contracts. Do not put PII in bank external-ref
  metadata; use opaque source-document IDs and SHA-256 content hashes.

Magpie supports `cash`, `modified_cash`, and `accrual` books. Invoice posting
differs by basis:

- `cash` and `modified_cash`: posting opens the invoice; payment recognizes
  revenue and any collected sales-tax liability.
- `accrual`: posting records accounts receivable and revenue; payment clears
  accounts receivable.

Use an account with `operating_cash` or `bank_account` for invoice payments.
Invoice lines require revenue accounts with an allowed revenue role. Accrual
invoices require `accounts_receivable`; taxable invoices require
`sales_tax_payable`.

## Safe source-document workflow

For each import or posting:

1. Read the active book settings.
2. Read the chart and resolve exact account IDs by role.
3. Validate the source amount, date, counterparty, destination, and durable
   source ID against authoritative evidence.
4. Normalize the source into an invoice, payout, customer, account, or manual
   journal JSON file.
5. Include structured `external_refs` whenever the source has a durable
   identifier.
6. For manual imports, include stable `source` and `source_key` values and a
   specific `manual_reason`.
7. Submit the narrow domain command.
8. Parse the returned JSON and retain returned IDs and root.
9. Read the resulting source document or journal list and reconcile amounts.
10. On an ambiguous transport result, re-read before retrying. Reuse the same
    external reference or source key; never invent a new identity to force a
    duplicate write.

For tenant-issued invoices prefer:

1. `invoice create-json` (status `draft` or omitted `imported`)
2. `invoice issue` (freezes the issued snapshot and posts accrual journals)
3. `invoice send` (records `sent`, returns the public link, attaches the PDF when mail is configured)
4. `rail collect --method checkout` on the **tenant** Stripe account

Never mark paid without `external_refs` or `manual_reason`. Never edit an issued
invoice in place. Void plus `credit-memo create-json` is the correction path.
Never edit `.magpie/` or raw Jaybase nodes. Snapshot before the first live
webhook test.

Tenant-facing copy: “Future Perfect issues and tracks your invoices. Your
customer pays you — card through your Stripe, or ACH/wire to your bank. We
never take the payment. Your books update when it clears.”

The platform is software + orchestration. The tenant is merchant of record.
Customers pay the tenant. Magpie never calls Stripe. `cmd/rail` creates Checkout
Sessions / Payment Links with `Stripe-Account: acct_…` (direct charges only).
Do not use the Stripe Invoicing product or destination charges. Do not hold or
forward customer funds. Stripe processing fees, when known from a balance
transaction, post through the existing `payout import-json` fee path
(`merchant_fees_expense` or `payment_processing_fees`). Do not invent a second
fee journal.

`rail collect` refuses unless the book has `operating_cash` or `bank_account`,
a fee role, accrual `accounts_receivable` when required, a default revenue
role, and a Stripe/Nango connection for that volume. Secrets stay in rail env /
Nango, never in invoice JSON beyond `external_refs`. Public tokens are
high-entropy and stored as `public_token_hash` only.

`invoice import-json` upserts the customer by external reference, creates or
reuses the invoice by external reference, optionally posts it, and records a
payment only when payment data and evidence are supplied. A source marked paid
without payment evidence is rejected.

`payout import-json` requires a destination bank/cash account and, when a fee is
present, an account with `merchant_fees_expense`. It creates the payout and its
workflow journals idempotently from external references.

`bank statement import-json` creates an open statement for an
`operating_cash`, `bank_account`, or liability `credit_card` account.
For the first statement on an account, Magpie compares the ledger balance
strictly before the period start and posts only the delta needed to reach the
statement opening balance against `opening_balance_equity`, dated one day
before the period. This uses `ledger:write`, not `journal:adjust`; later-period
activity does not change the delta. Recovery by source key must match the exact
date, accounts, postings, and amount.
`bank transaction import-json` stages a row by stable external reference.
`amount_cents` is the signed statement-balance change; positive increases the
balance and negative decreases it. A pending transaction cannot be posted or
paired.

Use `bank transaction post --transaction-id ID --account-id ID` to classify an
ordinary receipt, expense, refund, owner contribution, owner draw, or supported
balance-sheet movement. It creates a workflow journal with `ledger:write` and
does not require `journal:adjust`. Another bank/card account is not a valid
classification; pair the two staged source rows with `bank transfer pair`.
Pairing requires matching currency, equal amounts, opposite ledger effects,
and different accounts, with no income or expense. If the statement periods do
not share a safe posting date, a configured `transfer_clearing` asset account
supports two balanced leg journals on the source and destination dates.

Use `bank transaction reclassify` or `bank transaction reverse` for audited
corrections. Both append workflow journals and preserve earlier events.
Reversal offsets every journal contributing to the transaction's current
classification exactly and returns the row to `staged` for a corrected repost.
Its date defaults to and must equal the source transaction date. Repeating the
last exact correction is idempotent; an older historical target/reason is not a
no-op after later classification changes.

Use `bank transfer reverse` to offset an incorrect pair. Both legs return to
`staged`, while pair history retains the economic `from`/`to` direction and
audit reason. Each pair journal is offset on its original date. Omit an explicit
date for cross-period pairs; a supplied date must match every leg.

Run `bank reconciliation preview` after all source rows are imported. Completion
requires zero ledger difference, zero opening-plus-activity difference, and no
unmatched, duplicate, pending, or out-of-period blockers. A completed statement
rejects later transaction imports and corrections.

For provider-specific CSV, IIF, QBXML, API, PDF, or spreadsheet data, parsing
belongs outside Magpie. Preserve the original evidence and perform an explicit
mapping review before submitting normalized JSON.

## RBAC workflow

List permissions:

```sh
magpie --store /absolute/path/to/book.magpie --actor owner rbac permissions
```

Create a least-privilege role and actor:

```sh
magpie --store /absolute/path/to/book.magpie --actor owner \
  rbac role set --name "Invoice Agent" \
  --permissions ledger:read,ledger:write

magpie --store /absolute/path/to/book.magpie --actor owner \
  rbac user set --id invoice-agent --role "Invoice Agent"
```

Default roles are `Owner`, `Admin`, `Accountant`, `Operations`, `Sales Rep`,
and `Rail Webhook`. New books also create the `rail-webhook` actor. Read actual
permissions from reconstructed state rather than relying on the role name.
Finer invoice permissions are `invoice:issue`, `invoice:send`, `invoice:void`,
and `rail:collect`. They are separate from `ledger:write` mark-paid.
`rail-webhook` may only `invoice mark-paid` and `payout import-json`. It cannot
`ledger journal create` or change book settings. Stores created by older
versions may use:

```sh
magpie --store /absolute/path/to/book.magpie --actor owner rbac defaults repair
```

The repair adds missing current permissions to built-in roles without removing
custom roles or extra permissions.

## Command map

Use JSON files for structured input. Commands accepting `--file` also accept
`--file -` for stdin.

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
invoice update-json --file invoice.json
invoice import-json --file external-invoice.json
invoice post --invoice-id ID
invoice issue --invoice-id ID
invoice send --invoice-id ID [--to EMAIL]
invoice public-link --invoice-id ID [--tenant SLUG] [--rotate]
invoice public-serve --listen ADDR --tenant SLUG [--pay-base-url URL]
invoice void --invoice-id ID --reason TEXT
invoice remind --invoice-id ID
invoice mark-paid --invoice-id ID --cash-account-id ID --paid-date YYYY-MM-DD --amount-cents N
invoice reverse-payment --invoice-id ID --payment-id ID --reversal-date YYYY-MM-DD --reason REASON
invoice get --invoice-id ID
invoice list
credit-memo create-json --file credit-memo.json
payout import-json --file payout.json
payout get --payout-id ID
payout list
bank statement import-json --file statement.json
bank transaction import-json --file transaction.json
bank transaction post --transaction-id ID --account-id ID
bank transaction reverse --transaction-id ID --reason REASON [--date YYYY-MM-DD]
bank transaction reclassify --transaction-id ID --account-id ID --reason REASON
bank transaction list
bank transfer pair --from-transaction-id ID --to-transaction-id ID
bank transfer reverse --from-transaction-id ID --to-transaction-id ID --reason REASON [--date YYYY-MM-DD]
bank reconciliation preview --statement-id ID
bank reconciliation complete --statement-id ID
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
period close package (--through YYYY-MM-DD | --close-id ID) --output-dir DIR
report trial-balance --as-of YYYY-MM-DD --format json|csv
report profit-loss --from YYYY-MM-DD --through YYYY-MM-DD --format json|csv
report balance-sheet --as-of YYYY-MM-DD --format json|csv
report general-ledger --from YYYY-MM-DD --through YYYY-MM-DD --format json|csv
mcp [--http ADDR]
```

Always run close preview first and resolve every blocker. Native completed
bank/card statements provide reconciliation evidence through their period end.
Financial accounts with role `operating_cash`, `bank_account`, or `credit_card`
that are reconciled outside Magpie require an external-ref metadata marker
`reconciled_through=YYYY-MM-DD`; never advance it without source evidence. A
completed close rejects postings dated on or before its boundary.
Only an actor with `period:reopen` may reopen it, and the command requires an
audit reason. Reopening does not delete the close: after corrections, complete
the close again to create a linked revision.

Current staged-work coverage is exhaustive for the domain models in this
repository: invoices, payouts, and native bank statements and transactions.
Open statements, staged bank transactions, and missing active bank journal
links block close. A future bill or other staged financial model must add a
close-preview domain checker before it is close-safe.

Close packages regenerate reports from the manifest's exact pre-close Jaybase
root and verify every SHA-256 before writing files. Retain `manifest.json` with
all delivered JSON and CSV artifacts. Treat a hash mismatch as an integrity
failure. The first close report window begins at the book's first journal date;
later P&L and general-ledger windows begin the day after the previous active
close. A correction revision has a new `package_id` but preserves
`original_package_id`, `previous_package_id`, the original close record, and the
audit reason.

Close events and named refs are separate Jaybase operations. If a close reports
a ref-write failure, retry a close, reopen, or later close command. Magpie first
repairs all durable close refs deterministically and does not duplicate the
close event. Owner and Admin have `period:close` plus `period:reopen`; Accountant
has close only. Older stores need `rbac defaults repair` to receive these
defaults.

`state` and `audit` expose broad reconstructed or historical data and require
`audit:read`. Give that permission only to actors that need it.

## Security boundaries

- Treat local store contents, decrypted JSON output, source files, and hosted
  responses as sensitive financial data.
- Keep local stores and keys on encrypted disks with restrictive filesystem
  permissions. For production local storage, provide `JAYBASE_DATA_KEY` through
  managed secret delivery and separate keys from backups.
- Keep hosted bearer tokens and Jaybase data keys in a secret manager.
- Use opaque IDs in event metadata; payloads are encrypted, but event type,
  entity ID, actor, role, command, timestamp, and history shape are metadata.
- Do not store cardholder data, credentials, tax identifiers, or other secrets
  merely because payload encryption is available.
- Copy backups or snapshots off-host and test restoration. A successful command
  is not proof that disaster recovery works.
- Stop on integrity failures. Never bypass a failed read or weaken validation to
  make a posting succeed.

## Agent completion checklist

Before reporting success, verify:

- the command exited zero and stdout parsed as JSON;
- the returned actor, document, account, journal, and root match the intended
  book;
- debits, credits, taxes, fees, and totals reconcile to the source;
- the active accounting basis and account roles were used;
- durable external references or source keys were retained for replay safety;
- no secret appeared in arguments, files committed to source control, logs, or
  the report;
- any snapshot or backup claim distinguishes a local checkpoint from an
  independently stored and restore-tested backup.

# Magpie agent guide

Use this document as the operating contract for an AI agent that installs or
uses Magpie. For product context, human setup, examples, and deployment
boundaries, read [`README.md`](README.md) and
[`docs/SECURITY.md`](docs/SECURITY.md).

## What Magpie is

Magpie is an opinionated accounting CLI backed by an append-only Jaybase event
history. Humans and agents use the same commands. The domain layer enforces
role-based permissions, accounting-basis rules, account roles, balanced
double-entry journals, source-document workflow rules, and source-key
idempotency before a write is appended.

Magpie does not authenticate a human, decide whether source evidence is true,
provide accounting or tax advice, or parse provider-specific exports. The agent
must validate source evidence, normalize it into Magpie's documented JSON
contracts, use existing account IDs and roles, and obtain human approval when
the accounting treatment is uncertain.

Never read or edit `.magpie/`, Jaybase data files, refs, or encryption keys
directly. The CLI is the supported interface.

## Install

Magpie requires Go 1.26.5 or later. Earlier Go releases must not be used to
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
- Hosted commands currently reconstruct Magpie state by replaying the complete
  event history. Expect latency and bandwidth to grow with history size.

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
- Prefer first-class invoice and payout commands for ordinary activity.
- Use the first-class fixed-asset workflow for capitalized purchases and book
  depreciation. Do not create manual depreciation journals when the asset is in
  Magpie's fixed-asset register.
- Generic `ledger journal create` is a privileged manual adjustment or import
  path. It requires `ledger:write`, `journal:adjust`, a balanced entry, and a
  nonempty `manual_reason`.
- Before a large or risky workflow, create a named snapshot. A Magpie snapshot
  is a root checkpoint, not an off-host backup.
- Corrections preserve history. Reverse an invoice payment with
  `invoice reverse-payment`; do not attempt to edit or delete the original
  payment or journal.

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

Fixed-asset acquisition and depreciation are available only for
`modified_cash` and `accrual` books. Resolve accounts with `fixed_asset`,
`accumulated_depreciation`, and `depreciation_expense` roles before acquiring
an asset. Magpie supports straight-line book depreciation with a full-month
convention and posts only closed monthly periods. It allocates cents exactly
and uses stable source keys for idempotency.

Do not use this workflow to infer tax treatment. It does not calculate Section
179, bonus depreciation, MACRS, tax basis, impairment, or disposal. If the
method, useful life, salvage value, placed-in-service date, capitalization
decision, or book-versus-tax treatment is uncertain, stop for human review.

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

`invoice import-json` upserts the customer by external reference, creates or
reuses the invoice by external reference, optionally posts it, and records a
payment only when payment data and evidence are supplied. A source marked paid
without payment evidence is rejected.

`payout import-json` requires a destination bank/cash account and, when a fee is
present, an account with `merchant_fees_expense`. It creates the payout and its
workflow journals idempotently from external references.

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

Default roles are `Owner`, `Admin`, `Accountant`, `Operations`, and `Sales Rep`.
Read their actual permissions from reconstructed state rather than relying on
the role name. Stores created by older versions may use:

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
invoice import-json --file external-invoice.json
invoice post --invoice-id ID
invoice mark-paid --invoice-id ID --cash-account-id ID --paid-date YYYY-MM-DD --amount-cents N
invoice reverse-payment --invoice-id ID --payment-id ID --reversal-date YYYY-MM-DD --reason REASON
invoice get --invoice-id ID
invoice list
fixed-asset acquire-json --file asset.json
fixed-asset depreciate --asset-id ID --through-date YYYY-MM-DD
fixed-asset schedule --asset-id ID [--as-of-date YYYY-MM-DD]
fixed-asset get --asset-id ID
fixed-asset list
payout import-json --file payout.json
payout get --payout-id ID
payout list
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

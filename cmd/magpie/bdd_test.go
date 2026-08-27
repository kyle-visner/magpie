package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestBDDCLIProtectsNotesSnapshotsAndAudit(t *testing.T) {
	world := newCLIWorld(t)
	var noteID string
	var snapshotRoot string

	world.step("Given an initialized store and an Operations actor", func() {
		initResp := world.runOK("init")
		if stringField(t, initResp, "root") == "" {
			t.Fatalf("init response missing root: %#v", initResp)
		}
		world.runOK("rbac", "user", "set", "--id", "ops", "--role", "Operations")
	})

	world.step("When Operations writes an internal note", func() {
		resp := world.runOKAs("ops", "note", "put",
			"--title", "Ops Handoff",
			"--body", "Close out the support queue before EOD.",
			"--sensitivity", "internal",
		)
		noteID = nestedStringField(t, resp, "note", "id")
		if noteID == "" {
			t.Fatalf("note response missing id: %#v", resp)
		}
	})

	world.step("Then Operations can read notes but cannot inspect ledger data or create snapshots", func() {
		note := world.runOKAs("ops", "note", "get", "--id", noteID)
		if stringField(t, note, "title") != "Ops Handoff" {
			t.Fatalf("unexpected note response: %#v", note)
		}
		if err := world.runErrAs("ops", "ledger", "account", "list"); err == nil {
			t.Fatal("expected Operations actor to be denied ledger read")
		}
		if err := world.runErrAs("ops", "snapshot", "create", "--name", "ops-savepoint"); err == nil {
			t.Fatal("expected Operations actor to be denied snapshot creation")
		}
	})

	world.step("And Owner can create a named snapshot and audit the note write", func() {
		snapshot := world.runOK("snapshot", "create", "--name", "pre-close")
		snapshotRoot = stringField(t, snapshot, "root")
		if snapshotRoot == "" || stringField(t, snapshot, "name") != "pre-close" {
			t.Fatalf("unexpected snapshot response: %#v", snapshot)
		}

		var nodes []map[string]any
		world.unmarshal(world.runRawOK("audit"), &nodes)
		var sawNote bool
		for _, node := range nodes {
			if stringField(t, node, "type") == "note" && stringField(t, node, "command") == "note upsert" {
				sawNote = true
				break
			}
		}
		if !sawNote {
			t.Fatalf("audit log did not include note write: %#v", nodes)
		}
	})
}

func TestBDDCLIAccrualInvoiceFlowReplaysIntoLedger(t *testing.T) {
	world := newCLIWorld(t)
	var cashID string
	var revenueID string
	var customerID string
	var invoiceID string

	world.step("Given an initialized accrual-basis book with invoice account roles", func() {
		world.runOK("init")
		settings := nestedMap(t, world.runOK("book", "settings", "set", "--accounting-basis", "accrual"), "settings")
		if stringField(t, settings, "accounting_basis") != "accrual" {
			t.Fatalf("unexpected settings response: %#v", settings)
		}
		world.createAccount("1100", "Accounts Receivable", "asset", "accounts_receivable")
		cashID = world.createAccount("1010", "Operating Bank", "asset", "operating_cash")
		revenueID = world.createAccount("4000", "Service Revenue", "revenue", "default_service_revenue")
	})

	world.step("When a customer invoice is created, posted, and paid", func() {
		customerPath := world.writeJSON("customer.json", map[string]any{"name": "Acme Co"})
		customerID = nestedStringField(t, world.runOK("customer", "create-json", "--file", customerPath), "customer", "id")

		invoicePath := world.writeJSON("invoice.json", map[string]any{
			"invoice_number": "BDD-INV-1",
			"customer_id":    customerID,
			"invoice_date":   "2026-07-16",
			"line_items": []map[string]any{{
				"description":        "Implementation services",
				"revenue_account_id": revenueID,
				"quantity":           2,
				"unit_amount_cents":  50000,
				"amount_cents":       100000,
				"tax_amount_cents":   0,
			}},
			"subtotal_cents": 100000,
			"total_cents":    100000,
		})
		invoiceID = nestedStringField(t, world.runOK("invoice", "create-json", "--file", invoicePath), "invoice", "id")
		posted := nestedMap(t, world.runOK("invoice", "post", "--invoice-id", invoiceID), "invoice")
		if stringField(t, posted, "status") != "open" || stringField(t, posted, "issued_journal_entry_id") == "" {
			t.Fatalf("unexpected posted invoice: %#v", posted)
		}
		paid := nestedMap(t, world.runOK("invoice", "mark-paid",
			"--invoice-id", invoiceID,
			"--cash-account-id", cashID,
			"--paid-date", "2026-07-20",
			"--amount-cents", "100000",
			"--manual-reason", "BDD test payment",
		), "invoice")
		if stringField(t, paid, "status") != "paid" {
			t.Fatalf("unexpected paid invoice: %#v", paid)
		}
	})

	world.step("Then replayed ledger state contains the invoice issue and payment workflow journals", func() {
		var journals map[string]map[string]any
		world.unmarshal(world.runRawOK("ledger", "journal", "list"), &journals)
		if len(journals) != 2 {
			t.Fatalf("expected issue and payment journals, got %#v", journals)
		}
		workflows := map[string]bool{}
		for _, journal := range journals {
			if stringField(t, journal, "origin") != "workflow" || stringField(t, journal, "source_document_id") != invoiceID {
				t.Fatalf("unexpected invoice journal metadata: %#v", journal)
			}
			workflows[stringField(t, journal, "workflow")] = true
		}
		if !workflows["invoice.post"] || !workflows["invoice.mark_paid"] {
			t.Fatalf("missing expected invoice workflows: %#v", workflows)
		}
	})
}

func TestBDDCLIPayoutImportIsIdempotentAndPostsFees(t *testing.T) {
	world := newCLIWorld(t)
	var clearingID string
	var bankID string
	var feeID string
	var payoutPath string

	world.step("Given a chart with payout clearing, bank, and merchant fee accounts", func() {
		world.runOK("init")
		clearingID = world.createAccount("1020", "Processor Clearing", "asset", "")
		bankID = world.createAccount("1010", "Operating Bank", "asset", "operating_cash")
		feeID = world.createAccount("6100", "Merchant Fees", "expense", "merchant_fees_expense")
	})

	world.step("When an external payout with fees is imported twice", func() {
		payoutPath = world.writeJSON("payout.json", map[string]any{
			"date":                   "2026-07-21",
			"description":            "Processor batch 2026-07-21",
			"source_account_id":      clearingID,
			"destination_account_id": bankID,
			"net_amount_cents":       232518,
			"fee_amount_cents":       1000,
			"fee_expense_account_id": feeID,
			"external_refs": []map[string]any{{
				"source_system": "payment_processor",
				"external_id":   "po_bdd_1001",
				"external_type": "payout",
			}},
		})
		first := nestedMap(t, world.runOK("payout", "import-json", "--file", payoutPath), "payout")
		if len(arrayField(t, first, "journal_entry_ids")) != 2 {
			t.Fatalf("expected receive and fee journal links, got %#v", first)
		}
		second := nestedMap(t, world.runOK("payout", "import-json", "--file", payoutPath), "payout")
		if stringField(t, second, "id") != stringField(t, first, "id") {
			t.Fatalf("expected idempotent payout import, first=%#v second=%#v", first, second)
		}
	})

	world.step("Then replayed ledger state contains exactly the receive and fee workflow journals", func() {
		var journals map[string]map[string]any
		world.unmarshal(world.runRawOK("ledger", "journal", "list"), &journals)
		if len(journals) != 2 {
			t.Fatalf("expected idempotent payout import to leave two journals, got %#v", journals)
		}
		workflows := map[string]bool{}
		for _, journal := range journals {
			workflows[stringField(t, journal, "workflow")] = true
		}
		if !workflows["payout.receive"] || !workflows["payout.fee"] {
			t.Fatalf("missing expected payout workflows: %#v", workflows)
		}
	})
}

func TestBDDCLIBankImportPostAndReconciliation(t *testing.T) {
	world := newCLIWorld(t)
	world.runOK("init")
	bankID := world.createAccount("1010", "Operating Bank", "asset", "bank_account")
	expenseID := world.createAccount("6000", "Software Expense", "expense", "default_expense")
	statementPath := world.writeJSON("statement.json", map[string]any{
		"account_id": bankID, "period_start": "2026-06-01", "period_end": "2026-06-30",
		"opening_balance_cents": 0, "closing_balance_cents": -2500, "currency": "USD",
		"external_refs":   []map[string]any{{"source_system": "normalized_feed", "external_id": "stmt-1", "external_type": "statement"}},
		"source_document": map[string]any{"id": "document-1", "content_sha256": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
	})
	statementID := nestedStringField(t, world.runOK("bank", "statement", "import-json", "--file", statementPath), "statement", "id")
	transactionPath := world.writeJSON("transaction.json", map[string]any{
		"statement_id": statementID, "account_id": bankID, "date": "2026-06-10", "amount_cents": -2500, "currency": "USD",
		"external_refs": []map[string]any{{"source_system": "normalized_feed", "external_id": "txn-1", "external_type": "transaction"}},
	})
	transactionID := nestedStringField(t, world.runOK("bank", "transaction", "import-json", "--file", transactionPath), "transaction", "id")
	posted := nestedMap(t, world.runOK("bank", "transaction", "post", "--transaction-id", transactionID, "--account-id", expenseID), "transaction")
	if stringField(t, posted, "status") != "posted" {
		t.Fatalf("unexpected posted transaction: %#v", posted)
	}
	preview := world.runOK("bank", "reconciliation", "preview", "--statement-id", statementID)
	if canComplete, ok := preview["can_complete"].(bool); !ok || !canComplete {
		t.Fatalf("expected zero-difference preview: %#v", preview)
	}
	completed := nestedMap(t, world.runOK("bank", "reconciliation", "complete", "--statement-id", statementID), "reconciliation")
	if stringField(t, completed, "status") != "completed" {
		t.Fatalf("unexpected completed reconciliation: %#v", completed)
	}
}

type cliWorld struct {
	t   *testing.T
	dir string
	out bytes.Buffer
}

func newCLIWorld(t *testing.T) *cliWorld {
	t.Helper()
	return &cliWorld{t: t, dir: t.TempDir()}
}

func (w *cliWorld) step(text string, fn func()) {
	w.t.Helper()
	w.t.Logf("BDD: %s", text)
	fn()
}

func (w *cliWorld) runOK(args ...string) map[string]any {
	w.t.Helper()
	return w.runOKAs("owner", args...)
}

func (w *cliWorld) runOKAs(actor string, args ...string) map[string]any {
	w.t.Helper()
	var decoded map[string]any
	w.unmarshal(w.runRawOKAs(actor, args...), &decoded)
	return decoded
}

func (w *cliWorld) runRawOK(args ...string) []byte {
	w.t.Helper()
	return w.runRawOKAs("owner", args...)
}

func (w *cliWorld) runRawOKAs(actor string, args ...string) []byte {
	w.t.Helper()
	w.out.Reset()
	fullArgs := append([]string{"--store", w.dir, "--actor", actor}, args...)
	if err := run(fullArgs, &w.out); err != nil {
		w.t.Fatalf("command %v failed: %v", fullArgs, err)
	}
	return append([]byte(nil), w.out.Bytes()...)
}

func (w *cliWorld) runErrAs(actor string, args ...string) error {
	w.t.Helper()
	w.out.Reset()
	return run(append([]string{"--store", w.dir, "--actor", actor}, args...), &w.out)
}

func (w *cliWorld) writeJSON(name string, value any) string {
	w.t.Helper()
	path := filepath.Join(w.dir, name)
	raw, err := json.Marshal(value)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		w.t.Fatal(err)
	}
	return path
}

func (w *cliWorld) createAccount(number, name, typ, role string) string {
	w.t.Helper()
	args := []string{"ledger", "account", "create", "--name", name, "--type", typ}
	if number != "" {
		args = append(args, "--number", number)
	}
	if role != "" {
		args = append(args, "--role", role)
	}
	return nestedStringField(w.t, w.runOK(args...), "account", "id")
}

func (w *cliWorld) unmarshal(raw []byte, into any) {
	w.t.Helper()
	if err := json.Unmarshal(raw, into); err != nil {
		w.t.Fatalf("could not decode JSON %s: %v", string(raw), err)
	}
}

func nestedMap(t *testing.T, source map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := source[key].(map[string]any)
	if !ok {
		t.Fatalf("missing object %q in %#v", key, source)
	}
	return value
}

func nestedStringField(t *testing.T, source map[string]any, objectKey, fieldKey string) string {
	t.Helper()
	return stringField(t, nestedMap(t, source, objectKey), fieldKey)
}

func stringField(t *testing.T, source map[string]any, key string) string {
	t.Helper()
	value, ok := source[key].(string)
	if !ok {
		t.Fatalf("missing string field %q in %#v", key, source)
	}
	return value
}

func arrayField(t *testing.T, source map[string]any, key string) []any {
	t.Helper()
	value, ok := source[key].([]any)
	if !ok {
		t.Fatalf("missing array field %q in %#v", key, source)
	}
	return value
}

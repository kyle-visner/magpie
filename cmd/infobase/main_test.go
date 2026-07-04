package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCLIInitializesStoreAndDeniesUnauthorizedLedgerRead(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	var initResp map[string]any
	if err := json.Unmarshal(out.Bytes(), &initResp); err != nil {
		t.Fatal(err)
	}
	if initResp["root"] == "" {
		t.Fatalf("init response missing root: %s", out.String())
	}

	out.Reset()
	if err := run([]string{"--store", dir, "rbac", "user", "set", "--id", "ops", "--role", "Operations"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "--actor", "ops", "ledger", "account", "list"}, &out); err == nil {
		t.Fatal("expected Operations user to be denied ledger read")
	}
}

func TestCLICreatesAccountAndJournalFromJSON(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "account", "create", "--name", "Checking", "--type", "asset"}, &out); err != nil {
		t.Fatal(err)
	}
	cashID := extractNestedString(t, out.Bytes(), "account", "id")
	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "account", "create", "--name", "Revenue", "--type", "revenue"}, &out); err != nil {
		t.Fatal(err)
	}
	revenueID := extractNestedString(t, out.Bytes(), "account", "id")
	entryPath := filepath.Join(dir, "entry.json")
	entry := `{"date":"2026-06-28","memo":"Paid invoice","manual_reason":"manual test entry","postings":[{"account_id":"` + cashID + `","debit_cents":5000},{"account_id":"` + revenueID + `","credit_cents":5000}]}`
	if err := os.WriteFile(entryPath, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "journal", "create", "--file", entryPath}, &out); err != nil {
		t.Fatal(err)
	}
	if got := extractNestedString(t, out.Bytes(), "entry", "memo"); got != "Paid invoice" {
		t.Fatalf("unexpected journal memo %q", got)
	}
}

func TestCLICreatesAccountWithExternalRefMetadata(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--name", "Mercury Checking ****1234",
		"--type", "asset",
		"--external-source", "mercury",
		"--external-id", "mercury-account-1",
		"--external-type", "bank_account",
		"--external-display-name", "Mercury Operating Checking",
		"--external-url", "https://dashboard.mercury.com/accounts/mercury-account-1",
		"--external-meta", "account_kind=checking",
		"--external-meta", "nickname=Operating Checking",
		"--external-meta", "mask=****1234",
		"--external-meta", "last_four=1234",
	}, &out)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	account := decoded["account"].(map[string]any)
	externalRefs := account["external_refs"].([]any)
	external := externalRefs[0].(map[string]any)
	metadata := external["metadata"].(map[string]any)
	if external["source_system"] != "mercury" ||
		external["external_id"] != "mercury-account-1" ||
		external["external_type"] != "bank_account" ||
		external["display_name"] != "Mercury Operating Checking" ||
		metadata["account_kind"] != "checking" ||
		metadata["nickname"] != "Operating Checking" ||
		metadata["mask"] != "****1234" ||
		metadata["last_four"] != "1234" {
		t.Fatalf("unexpected external ref metadata: %#v", external)
	}
}

func TestCLICreatesAndUpdatesAccountNumber(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "1000",
		"--name", "Operating Bank",
		"--type", "asset",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	account := decoded["account"].(map[string]any)
	accountID := account["id"].(string)
	if account["number"] != "1000" {
		t.Fatalf("expected account number in create response: %#v", account)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "number", "set",
		"--account-id", accountID,
		"--number", "1010",
	}, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	account = decoded["account"].(map[string]any)
	if account["number"] != "1010" {
		t.Fatalf("expected updated account number: %#v", account)
	}
}

func TestCLICreatesAndUpdatesAccountRole(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "1010",
		"--name", "Operating Bank",
		"--type", "asset",
		"--role", "bank_account",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	account := decoded["account"].(map[string]any)
	accountID := account["id"].(string)
	if account["role"] != "bank_account" {
		t.Fatalf("expected account role in create response: %#v", account)
	}

	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "role", "set",
		"--account-id", accountID,
		"--role", "operating_cash",
	}, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	account = decoded["account"].(map[string]any)
	if account["role"] != "operating_cash" {
		t.Fatalf("expected updated account role: %#v", account)
	}

	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "account", "role", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	var roles []string
	if err := json.Unmarshal(out.Bytes(), &roles); err != nil {
		t.Fatal(err)
	}
	if len(roles) == 0 || roles[0] == "" {
		t.Fatalf("expected account roles list, got %#v", roles)
	}
}

func TestCLIConfiguresAccountingBasisAndStampsJournalEntries(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "book", "settings", "set", "--accounting-basis", "modified_cash"}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	settings := decoded["settings"].(map[string]any)
	if settings["accounting_basis"] != "modified_cash" {
		t.Fatalf("expected modified cash settings, got %#v", settings)
	}

	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "account", "create", "--name", "Checking", "--type", "asset"}, &out); err != nil {
		t.Fatal(err)
	}
	cashID := extractNestedString(t, out.Bytes(), "account", "id")
	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "account", "create", "--name", "Revenue", "--type", "revenue"}, &out); err != nil {
		t.Fatal(err)
	}
	revenueID := extractNestedString(t, out.Bytes(), "account", "id")
	entryPath := filepath.Join(dir, "modified-cash-entry.json")
	entry := `{"date":"2026-06-28","memo":"Paid invoice under modified cash","manual_reason":"manual modified cash test entry","postings":[{"account_id":"` + cashID + `","debit_cents":5000},{"account_id":"` + revenueID + `","credit_cents":5000}]}`
	if err := os.WriteFile(entryPath, []byte(entry), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "journal", "create", "--file", entryPath}, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	created := decoded["entry"].(map[string]any)
	if created["accounting_basis"] != "modified_cash" {
		t.Fatalf("expected stamped journal accounting basis, got %#v", created)
	}
	if created["origin"] != "manual_adjustment" {
		t.Fatalf("expected manual journal origin, got %#v", created)
	}

	out.Reset()
	if err := run([]string{"--store", dir, "--actor", "owner", "book", "settings", "get"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["accounting_basis"] != "modified_cash" {
		t.Fatalf("expected settings get to return modified cash, got %#v", settings)
	}
}

func TestCLIUpdatesExistingAccountExternalRefMetadata(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--name", "Operating Bank",
		"--type", "asset",
	}, &out); err != nil {
		t.Fatal(err)
	}
	accountID := extractNestedString(t, out.Bytes(), "account", "id")
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "external-ref", "set",
		"--account-id", accountID,
		"--external-source", "mercury",
		"--external-id", "mercury-account-1",
		"--external-type", "bank_account",
		"--external-display-name", "Mercury Operating Checking",
		"--role", "bank_account",
		"--external-meta", "last_four=1234",
		"--external-meta", "nickname=Operating Updated",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	account := decoded["account"].(map[string]any)
	if account["role"] != "bank_account" {
		t.Fatalf("expected role to be assigned with external ref, got %#v", account)
	}
	externalRefs := account["external_refs"].([]any)
	external := externalRefs[0].(map[string]any)
	metadata := external["metadata"].(map[string]any)
	if external["source_system"] != "mercury" ||
		external["external_id"] != "mercury-account-1" ||
		metadata["nickname"] != "Operating Updated" {
		t.Fatalf("unexpected updated external ref: %#v", external)
	}
}

func TestCLIRepairsLegacyDefaultRoles(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	staleOwnerPermissions := "admin:recover,audit:read,journal:adjust,ledger:read,ledger:write,notes:read,notes:write,rbac:manage,settings:manage,snapshot:create"
	if err := run([]string{
		"--store", dir,
		"rbac", "role", "set",
		"--name", "Owner",
		"--permissions", staleOwnerPermissions,
	}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--name", "Blocked Service Revenue",
		"--type", "revenue",
		"--role", "default_service_revenue",
	}, &out); err == nil {
		t.Fatal("expected stale Owner role without chart:manage to be denied role assignment")
	}

	out.Reset()
	if err := run([]string{"--store", dir, "rbac", "defaults", "repair"}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	repair := decoded["repair"].(map[string]any)
	roles := repair["roles"].(map[string]any)
	if _, ok := roles["Owner"]; !ok {
		t.Fatalf("expected Owner repair result, got %#v", repair)
	}

	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--name", "Service Revenue",
		"--type", "revenue",
		"--role", "default_service_revenue",
	}, &out); err != nil {
		t.Fatal(err)
	}
	if got := extractNestedString(t, out.Bytes(), "account", "role"); got != "default_service_revenue" {
		t.Fatalf("expected repaired Owner to assign account role, got %q", got)
	}
}

func TestCLIInvoiceWorkflowCreatesWorkflowJournal(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "1010",
		"--name", "Operating Bank",
		"--type", "asset",
		"--role", "operating_cash",
	}, &out); err != nil {
		t.Fatal(err)
	}
	cashID := extractNestedString(t, out.Bytes(), "account", "id")
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "4000",
		"--name", "Service Revenue",
		"--type", "revenue",
		"--role", "default_service_revenue",
	}, &out); err != nil {
		t.Fatal(err)
	}
	revenueID := extractNestedString(t, out.Bytes(), "account", "id")

	customerPath := filepath.Join(dir, "customer.json")
	if err := os.WriteFile(customerPath, []byte(`{"name":"Acme Co"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "customer", "create-json", "--file", customerPath}, &out); err != nil {
		t.Fatal(err)
	}
	customerID := extractNestedString(t, out.Bytes(), "customer", "id")

	invoicePath := filepath.Join(dir, "invoice.json")
	invoiceJSON := `{
		"invoice_number":"INV-CLI-1",
		"customer_id":"` + customerID + `",
		"invoice_date":"2026-06-01",
		"line_items":[{
			"description":"Services",
			"revenue_account_id":"` + revenueID + `",
			"quantity":1,
			"unit_amount_cents":125000,
			"amount_cents":125000
		}],
		"subtotal_cents":125000,
		"total_cents":125000
	}`
	if err := os.WriteFile(invoicePath, []byte(invoiceJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "invoice", "create-json", "--file", invoicePath}, &out); err != nil {
		t.Fatal(err)
	}
	invoiceID := extractNestedString(t, out.Bytes(), "invoice", "id")

	out.Reset()
	if err := run([]string{"--store", dir, "invoice", "post", "--invoice-id", invoiceID}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"invoice", "mark-paid",
		"--invoice-id", invoiceID,
		"--cash-account-id", cashID,
		"--paid-date", "2026-06-15",
		"--amount-cents", "125000",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	invoice := decoded["invoice"].(map[string]any)
	if invoice["status"] != "paid" {
		t.Fatalf("expected paid invoice, got %#v", invoice)
	}

	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "journal", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	var journals map[string]map[string]any
	if err := json.Unmarshal(out.Bytes(), &journals); err != nil {
		t.Fatal(err)
	}
	if len(journals) != 1 {
		t.Fatalf("expected one workflow journal, got %#v", journals)
	}
	for _, journal := range journals {
		if journal["origin"] != "workflow" || journal["workflow"] != "invoice.mark_paid" || journal["source_document_id"] != invoiceID {
			t.Fatalf("unexpected workflow journal: %#v", journal)
		}
	}
}

func TestCLIImportsNormalizedExternalInvoice(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{"--store", dir, "init"}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "1010",
		"--name", "Operating Bank",
		"--type", "asset",
		"--role", "operating_cash",
	}, &out); err != nil {
		t.Fatal(err)
	}
	cashID := extractNestedString(t, out.Bytes(), "account", "id")
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "4000",
		"--name", "Service Revenue",
		"--type", "revenue",
		"--role", "default_service_revenue",
	}, &out); err != nil {
		t.Fatal(err)
	}
	revenueID := extractNestedString(t, out.Bytes(), "account", "id")
	out.Reset()
	if err := run([]string{
		"--store", dir,
		"ledger", "account", "create",
		"--number", "2100",
		"--name", "Sales Tax Payable",
		"--type", "liability",
		"--role", "sales_tax_payable",
	}, &out); err != nil {
		t.Fatal(err)
	}

	importPath := filepath.Join(dir, "external-invoice.json")
	importJSON := `{
		"post": true,
		"customer": {
			"name": "Acme Co",
			"external_refs": [{
				"source_system": "billing_platform",
				"external_id": "customer-1",
				"external_type": "customer"
			}]
		},
		"invoice": {
			"invoice_number": "EXT-CLI-1",
			"invoice_date": "2026-06-01",
			"status": "paid",
			"line_items": [{
				"description": "Services",
				"quantity": 1,
				"unit_amount_cents": 125000,
				"tax_amount_cents": 6875
			}],
			"total_cents": 131875,
			"external_refs": [{
				"source_system": "billing_platform",
				"external_id": "invoice-1",
				"external_type": "invoice"
			}]
		},
		"payment": {
			"date": "2026-06-15",
			"amount_cents": 131875,
			"cash_account_id": "` + cashID + `",
			"external_source": "bank_feed",
			"external_id": "txn-1",
			"payment_evidence": "external_transaction_match"
		}
	}`
	if err := os.WriteFile(importPath, []byte(importJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "invoice", "import-json", "--file", importPath}, &out); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	importResult := decoded["import"].(map[string]any)
	invoice := importResult["invoice"].(map[string]any)
	invoiceID := invoice["id"].(string)
	if invoice["status"] != "paid" {
		t.Fatalf("expected paid invoice import, got %#v", invoice)
	}
	payments := invoice["payments"].([]any)
	payment := payments[0].(map[string]any)
	paymentID := payment["id"].(string)
	lines := invoice["line_items"].([]any)
	line := lines[0].(map[string]any)
	if line["revenue_account_id"] != revenueID {
		t.Fatalf("expected default revenue fallback %s, got %#v", revenueID, line)
	}
	if invoice["tax_amount_cents"] != float64(6875) || invoice["total_cents"] != float64(131875) {
		t.Fatalf("expected invoice tax inferred from line tax, got %#v", invoice)
	}

	out.Reset()
	if err := run([]string{"--store", dir, "invoice", "import-json", "--file", importPath}, &out); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run([]string{"--store", dir, "invoice", "get", "--invoice-id", invoiceID}, &out); err != nil {
		t.Fatal(err)
	}
	var gotInvoice map[string]any
	if err := json.Unmarshal(out.Bytes(), &gotInvoice); err != nil {
		t.Fatal(err)
	}
	if gotInvoice["id"] != invoiceID || gotInvoice["status"] != "paid" {
		t.Fatalf("unexpected invoice get response: %#v", gotInvoice)
	}

	out.Reset()
	if err := run([]string{
		"--store", dir,
		"invoice", "reverse-payment",
		"--invoice-id", invoiceID,
		"--payment-id", paymentID,
		"--reversal-date", "2026-06-16",
		"--reason", "invoice was incorrectly marked paid",
	}, &out); err != nil {
		t.Fatal(err)
	}
	var reversedResp map[string]any
	if err := json.Unmarshal(out.Bytes(), &reversedResp); err != nil {
		t.Fatal(err)
	}
	reversed := reversedResp["invoice"].(map[string]any)
	if reversed["status"] != "open" {
		t.Fatalf("expected reversed invoice to reopen, got %#v", reversed)
	}
	reversedPayments := reversed["payments"].([]any)
	reversedPayment := reversedPayments[0].(map[string]any)
	if reversedPayment["reversed"] != true || reversedPayment["reversal_journal_entry_id"] == "" {
		t.Fatalf("expected reversed payment metadata, got %#v", reversedPayment)
	}

	out.Reset()
	if err := run([]string{"--store", dir, "ledger", "journal", "list"}, &out); err != nil {
		t.Fatal(err)
	}
	var journals map[string]map[string]any
	if err := json.Unmarshal(out.Bytes(), &journals); err != nil {
		t.Fatal(err)
	}
	if len(journals) != 2 {
		t.Fatalf("expected payment and reversal workflow journals after idempotent import retry, got %#v", journals)
	}
}

func extractNestedString(t *testing.T, raw []byte, objectKey, fieldKey string) string {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	nested, ok := decoded[objectKey].(map[string]any)
	if !ok {
		t.Fatalf("missing object %q in %s", objectKey, string(raw))
	}
	value, ok := nested[fieldKey].(string)
	if !ok {
		t.Fatalf("missing string field %q in %s", fieldKey, string(raw))
	}
	return value
}

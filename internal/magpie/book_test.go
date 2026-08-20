package magpie

import (
	"encoding/json"
	"testing"
)

func TestBookIsFirstClassPathForInitChartAndJournal(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	book := NewBook(store, Context{Actor: "owner"})

	if _, err := book.Invoke("init", nil); err != nil {
		t.Fatal(err)
	}
	settings, err := book.Invoke("book_settings_get", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := settings.(BookSettings)
	if got.AccountingBasis != AccountingBasisCash {
		t.Fatalf("expected cash basis, got %#v", got)
	}

	cashRaw, err := book.Invoke("ledger_account_create", json.RawMessage(`{"name":"Checking","type":"asset","role":"operating_cash"}`))
	if err != nil {
		t.Fatal(err)
	}
	revenueRaw, err := book.Invoke("ledger_account_create", json.RawMessage(`{"name":"Revenue","type":"revenue","role":"default_service_revenue"}`))
	if err != nil {
		t.Fatal(err)
	}
	cashID := nestedID(t, cashRaw, "account")
	revenueID := nestedID(t, revenueRaw, "account")

	entry := `{
		"date":"2026-08-19",
		"memo":"Paid invoice",
		"manual_reason":"MCP first-class path test",
		"source":"test",
		"source_key":"mcp-journal-1",
		"postings":[
			{"account_id":"` + cashID + `","debit_cents":5000},
			{"account_id":"` + revenueID + `","credit_cents":5000}
		]
	}`
	created, err := book.Invoke("ledger_journal_create", json.RawMessage(entry))
	if err != nil {
		t.Fatal(err)
	}
	if nestedID(t, created, "entry") == "" {
		t.Fatalf("journal create returned no entry id: %#v", created)
	}

	listed, err := book.Invoke("ledger_journal_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	journals, ok := listed.(map[string]JournalEntry)
	if !ok || len(journals) != 1 {
		t.Fatalf("expected one journal, got %#v", listed)
	}

	if _, err := book.Invoke("note_put", json.RawMessage(`{"title":"Policy","body":"Cash basis confirmed."}`)); err != nil {
		t.Fatal(err)
	}
	notes, err := book.Invoke("note_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	if noteMap, ok := notes.(map[string]Note); !ok || len(noteMap) != 1 {
		t.Fatalf("expected one note, got %#v", notes)
	}
}

func TestBookInvokeListCatalogsReturnObjects(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	book := NewBook(store, Context{Actor: "owner"})
	if _, err := book.Init(); err != nil {
		t.Fatal(err)
	}

	roles, err := book.Invoke("ledger_account_role_list", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogField(t, "roles", roles, "operating_cash")

	perms, err := book.Invoke("rbac_permissions", nil)
	if err != nil {
		t.Fatal(err)
	}
	assertCatalogField(t, "permissions", perms, "ledger:read")

	audit, err := book.Invoke("audit", nil)
	if err != nil {
		t.Fatal(err)
	}
	auditObj, ok := audit.(map[string]any)
	if !ok {
		t.Fatalf("audit must be an object, got %#v", audit)
	}
	if _, ok := auditObj["events"]; !ok {
		t.Fatalf("audit object must include events, got %#v", auditObj)
	}

	for _, name := range []string{"ledger_account_list", "ledger_journal_list", "customer_list", "invoice_list", "payout_list", "note_list"} {
		listed, err := book.Invoke(name, nil)
		if err != nil {
			t.Fatal(err)
		}
		buf, err := json.Marshal(listed)
		if err != nil {
			t.Fatal(err)
		}
		if len(buf) == 0 || buf[0] != '{' {
			t.Fatalf("%s must return a JSON object, got %s", name, buf)
		}
	}
}

func TestBookRejectsUnknownToolAndUnknownField(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	book := NewBook(store, Context{Actor: "owner"})
	if _, err := book.Invoke("not_a_tool", nil); err == nil {
		t.Fatal("expected unknown tool to fail")
	}
	if _, err := book.Invoke("book_settings_set", json.RawMessage(`{"accounting_basis":"cash","nope":true}`)); err == nil {
		t.Fatal("expected unknown field to fail")
	}
}

func TestBookBindsActorFromContextNotFromParams(t *testing.T) {
	store, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	owner := NewBook(store, Context{Actor: "owner"})
	if _, err := owner.Init(); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.SetUser("ops", "Operations"); err != nil {
		t.Fatal(err)
	}
	ops := NewBook(store, Context{Actor: "ops"})
	if _, err := ops.Invoke("ledger_account_list", json.RawMessage(`{"actor":"owner"}`)); err == nil {
		t.Fatal("expected Operations actor to be denied even if params mention owner")
	}
}

func assertCatalogField(t *testing.T, field string, value any, required string) {
	t.Helper()
	obj, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s must be an object, got %#v", field, value)
	}
	raw, ok := obj[field]
	if !ok {
		t.Fatalf("missing %s field: %#v", field, obj)
	}
	names, ok := raw.([]string)
	if !ok {
		t.Fatalf("expected %s []string, got %#v", field, raw)
	}
	for _, name := range names {
		if name == required {
			return
		}
	}
	t.Fatalf("expected %s to include %q, got %#v", field, required, names)
}

func nestedID(t *testing.T, value any, key string) string {
	t.Helper()
	wrapper, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %#v", value)
	}
	inner, ok := wrapper[key].(Account)
	if ok {
		return inner.ID
	}
	entry, ok := wrapper[key].(JournalEntry)
	if ok {
		return entry.ID
	}
	encoded, err := json.Marshal(wrapper[key])
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(encoded, &generic); err != nil {
		t.Fatal(err)
	}
	id, _ := generic["id"].(string)
	return id
}

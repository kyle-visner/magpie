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
	entry := `{"date":"2026-06-28","memo":"Paid invoice","postings":[{"account_id":"` + cashID + `","debit_cents":5000},{"account_id":"` + revenueID + `","credit_cents":5000}]}`
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

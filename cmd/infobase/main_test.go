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
	externalRefs := account["external_refs"].([]any)
	external := externalRefs[0].(map[string]any)
	metadata := external["metadata"].(map[string]any)
	if external["source_system"] != "mercury" ||
		external["external_id"] != "mercury-account-1" ||
		metadata["nickname"] != "Operating Updated" {
		t.Fatalf("unexpected updated external ref: %#v", external)
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

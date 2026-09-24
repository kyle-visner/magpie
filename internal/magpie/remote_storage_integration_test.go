package magpie

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kyle-visner/jaybase"
	jaybaseserver "github.com/kyle-visner/jaybase/server"
)

type jaybaseIntegrationHarness struct {
	store        *Store
	jaybaseStore *jaybase.Store
}

func newJaybaseIntegrationHarness(t *testing.T) jaybaseIntegrationHarness {
	t.Helper()
	token := strings.Repeat("w", 64)
	digest := sha256.Sum256([]byte(token))
	authJSON, err := json.Marshal(map[string]any{
		"tokens": []map[string]string{{
			"id": "magpie-integration", "role": "writer", "sha256": hex.EncodeToString(digest[:]),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, authJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := jaybaseserver.LoadAuthenticator(authPath)
	if err != nil {
		t.Fatal(err)
	}

	jaybaseStore, err := jaybase.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := jaybaseStore.Close(); err != nil {
			t.Errorf("close Jaybase store: %v", err)
		}
	})
	api, err := jaybaseserver.New(jaybaseserver.Options{
		Store:  jaybaseStore,
		Auth:   auth,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(httpServer.Close)

	store, err := openRemoteStore(httpServer.URL, token, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Magpie remote store: %v", err)
		}
	})
	return jaybaseIntegrationHarness{store: store, jaybaseStore: jaybaseStore}
}

func TestRemoteStoreAgainstMergedJaybaseServer(t *testing.T) {
	harness := newJaybaseIntegrationHarness(t)
	store := harness.store
	if _, err := store.WriteInitialRoot(Context{Actor: "owner"}); err != nil {
		t.Fatal(err)
	}
	note, firstRoot, err := store.UpsertNote(Context{Actor: "owner"}, "", "Hosted contract", "first", "internal")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateSnapshot(Context{Actor: "owner"}, "integration-checkpoint"); err != nil {
		t.Fatal(err)
	}
	foreignRoot, err := harness.jaybaseStore.AppendAt(jaybase.Context{Actor: "martin-integration", Role: "writer"}, jaybase.AppendOptions{
		Type: "martin.contact", EntityID: "contact:foreign", Command: "foreign shared-history append",
		Payload: map[string]string{"value": "must not be decrypted by Magpie"},
	}, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	foreignPath := harness.jaybaseStore.NodePath(foreignRoot)
	foreignBytes, err := os.ReadFile(foreignPath)
	if err != nil {
		t.Fatal(err)
	}
	corruptForeign := strings.Replace(string(foreignBytes), "ciphertext", "ciphertexu", 1)
	if corruptForeign == string(foreignBytes) {
		t.Fatal("foreign Jaybase fixture did not contain an encrypted payload")
	}
	if err := os.WriteFile(foreignPath, []byte(corruptForeign), 0o600); err != nil {
		t.Fatal(err)
	}
	_, secondRoot, err := store.UpsertNote(Context{Actor: "owner"}, note.ID, note.Title, "second", note.Sensitivity)
	if err != nil {
		t.Fatal(err)
	}
	if firstRoot == secondRoot {
		t.Fatal("expected note update to advance the hosted root")
	}
	if _, err := store.CreateSnapshot(Context{Actor: "owner"}, "integration-checkpoint"); err != nil {
		t.Fatal(err)
	}

	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Root != secondRoot || state.Notes[note.ID].Body != "second" {
		t.Fatalf("merged Jaybase replay mismatch: %#v", state)
	}
	ref, err := harness.jaybaseStore.NamedRef("magpie-integration-checkpoint")
	if err != nil {
		t.Fatal(err)
	}
	if ref != secondRoot {
		t.Fatalf("conditional named ref = %q, want %q", ref, secondRoot)
	}
	audit, err := store.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	if len(audit) != 4 || audit[0].Actor != "magpie-integration" || len(audit[0].Payload) != 0 {
		t.Fatalf("unexpected hosted audit response: %#v", audit)
	}
}

func TestRemoteStoreBankReconciliationClosesAndReproducesPackage(t *testing.T) {
	harness := newJaybaseIntegrationHarness(t)
	store := harness.store
	owner := Context{Actor: "owner"}
	if _, err := store.WriteInitialRoot(owner); err != nil {
		t.Fatal(err)
	}
	bank, _, err := store.CreateAccountWithDetails(owner, Account{
		Number: "1010", Name: "Hosted Operating Bank", Type: AccountAsset, Role: AccountRoleBankAccount,
	})
	if err != nil {
		t.Fatal(err)
	}
	revenue, _, err := store.CreateAccountWithDetails(owner, Account{
		Number: "4000", Name: "Hosted Service Revenue", Type: AccountRevenue, Role: AccountRoleDefaultServiceRevenue,
	})
	if err != nil {
		t.Fatal(err)
	}
	statement, _, err := store.ImportBankStatement(owner, BankStatement{
		AccountID: bank.ID, PeriodStart: "2026-06-01", PeriodEnd: "2026-06-30",
		OpeningBalanceCents: 0, ClosingBalanceCents: 10000, Currency: "USD",
		ExternalRefs:   []ExternalSourceRef{{SourceSystem: "integration", ExternalID: "statement-june", ExternalType: "statement"}},
		SourceDocument: &SourceDocumentReference{ID: "doc-statement-june", ContentSHA256: strings.Repeat("a", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	transaction, _, err := store.ImportBankTransaction(owner, BankTransaction{
		StatementID: statement.ID, AccountID: bank.ID, Date: "2026-06-15", AmountCents: 10000, Currency: "USD",
		ExternalRefs:   []ExternalSourceRef{{SourceSystem: "integration", ExternalID: "transaction-income", ExternalType: "transaction"}},
		SourceDocument: &SourceDocumentReference{ID: "doc-transaction-income", ContentSHA256: strings.Repeat("b", 64)},
	})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := store.PreviewPeriodClose(owner, "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlockerForEntity(preview, "unposted_source_document", transaction.ID) ||
		!hasBlockerForEntity(preview, "unreconciled_bank_statement", statement.ID) {
		t.Fatalf("hosted close preview did not fail closed before posting and reconciliation: %#v", preview.Blockers)
	}
	if _, _, err := store.PostBankTransaction(owner, transaction.ID, revenue.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CompleteBankReconciliation(owner, statement.ID); err != nil {
		t.Fatal(err)
	}
	preview, err = store.PreviewPeriodClose(owner, "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if preview.Blocked {
		t.Fatalf("hosted close remained blocked after completed reconciliation: %#v", preview.Blockers)
	}
	profitLoss, err := store.ProfitLoss(owner, "2026-06-01", "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if profitLoss.TotalRevenueCents != 10000 || profitLoss.TotalExpenseCents != 0 || profitLoss.NetIncomeCents != 10000 {
		t.Fatalf("hosted profit and loss did not reflect reconciled bank activity: %#v", profitLoss)
	}
	close, err := store.CompletePeriodClose(owner, "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if close.Manifest.SourceRoot == "" || close.Root == "" || len(close.Manifest.ReportSHA256) != 8 {
		t.Fatalf("hosted close provenance is incomplete: %#v", close)
	}
	if ref, err := harness.jaybaseStore.NamedRef(close.Manifest.SnapshotName); err != nil || ref != close.Root {
		t.Fatalf("hosted close named ref=%q want=%q err=%v", ref, close.Root, err)
	}
	first, err := store.BuildClosePackageByID(owner, close.ID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.BuildClosePackageByID(owner, close.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Files) != 9 || len(second.Files) != len(first.Files) {
		t.Fatalf("unexpected hosted close package file counts: first=%d second=%d", len(first.Files), len(second.Files))
	}
	for name, firstData := range first.Files {
		if !bytes.Equal(firstData, second.Files[name]) {
			t.Fatalf("hosted close package artifact %s was not reproducible", name)
		}
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Root != close.Root || state.BankStatements[statement.ID].Status != ReconciliationCompleted ||
		state.BankTransactions[transaction.ID].Status != BankTransactionPosted || state.PeriodCloses[close.ID].ID != close.ID {
		t.Fatalf("hosted Jaybase replay lost bank or close state: %#v", state)
	}
	if _, _, err := store.CreateJournalEntry(owner, JournalEntry{
		Date: "2026-06-30", Memo: "Hosted backdate must fail", ManualReason: "integration check",
		Postings: []Posting{{AccountID: bank.ID, Debit: 1}, {AccountID: revenue.ID, Credit: 1}},
	}); err == nil || !strings.Contains(err.Error(), "closed period") {
		t.Fatalf("hosted closed-period guard did not reject a backdated journal: %v", err)
	}
	if !strings.HasPrefix(close.Manifest.SnapshotName, RefPrefix) {
		t.Fatalf("hosted close ref %q is outside %s", close.Manifest.SnapshotName, RefPrefix)
	}
}

// The catalog file exists only in this test's temp directory. Magpie does not
// read JAYBASE_CATALOG_FILE and this test does not set it.
func TestScopedWriterAcceptsCatalogTypesAndRejectsLegacyUndottedNames(t *testing.T) {
	token := strings.Repeat("s", 64)
	digest := sha256.Sum256([]byte(token))
	authJSON, err := json.Marshal(map[string]any{
		"tokens": []map[string]any{{
			"id": "magpie-scoped", "role": "writer", "sha256": hex.EncodeToString(digest[:]),
			"allow": map[string]any{
				"types": []string{"magpie.*"},
				"refs":  []string{RefPattern},
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, authJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	auth, err := jaybaseserver.LoadAuthenticator(authPath)
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := jaybaseserver.LoadCatalog(filepath.Join(t.TempDir(), "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range WriterCatalog() {
		if _, _, err := catalog.Install(entry.Type, entry.Commands); err != nil {
			t.Fatalf("install %s: %v", entry.Type, err)
		}
	}
	jaybaseStore, err := jaybase.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = jaybaseStore.Close() })
	api, err := jaybaseserver.New(jaybaseserver.Options{
		Store: jaybaseStore, Auth: auth, Catalog: catalog,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(api.Handler())
	t.Cleanup(httpServer.Close)
	store, err := openRemoteStore(httpServer.URL, token, httpServer.Client())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := Context{Actor: "owner"}
	if _, err := store.WriteInitialRoot(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.UpsertNote(ctx, "", "Scoped", "catalog write", "internal"); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.CreateSnapshot(ctx, "scoped-save")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Name != "magpie-scoped-save" {
		t.Fatalf("snapshot name = %q", snapshot.Name)
	}
	state, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Notes == nil || len(state.Notes) != 1 {
		t.Fatalf("scoped replay lost the note: %#v", state.Notes)
	}

	legacy := postJaybase(t, httpServer.Client(), httpServer.URL, token, "/v1/events", map[string]any{
		"type": "note", "command": "note upsert", "payload": map[string]any{"kind": "probe"},
		"expected_root": state.Root,
	}, "legacy-note-probe")
	if legacy.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy undotted type status = %d body %s", legacy.StatusCode, legacy.Body)
	}
	oldRef := postJaybase(t, httpServer.Client(), httpServer.URL, token, "/v1/refs/period-close-2026-01-31-r1", map[string]any{
		"root": state.Root, "expected_root": "",
	}, "")
	if oldRef.StatusCode != http.StatusForbidden {
		t.Fatalf("legacy period-close ref status = %d body %s", oldRef.StatusCode, oldRef.Body)
	}
}

type jaybaseHTTPResult struct {
	StatusCode int
	Body       string
}

func postJaybase(t *testing.T, client *http.Client, baseURL, token, path string, payload any, idempotencyKey string) jaybaseHTTPResult {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	method := http.MethodPost
	if strings.Contains(path, "/v1/refs/") {
		method = http.MethodPut
	}
	request, err := http.NewRequest(method, baseURL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	return jaybaseHTTPResult{StatusCode: response.StatusCode, Body: string(raw)}
}

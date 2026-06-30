package infobase

import (
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) (*Store, Context) {
	t.Helper()
	s, err := OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC) }
	ctx := Context{Actor: "owner"}
	if _, err := s.WriteInitialRoot(ctx); err != nil {
		t.Fatal(err)
	}
	return s, ctx
}

func mustAccount(t *testing.T, s *Store, ctx Context, name string, typ AccountType) Account {
	t.Helper()
	acct, _, err := s.CreateAccount(ctx, name, typ, "confidential")
	if err != nil {
		t.Fatal(err)
	}
	return acct
}

func TestLedgerRequiresBalancedJournalEntries(t *testing.T) {
	s, ctx := newTestStore(t)
	cash := mustAccount(t, s, ctx, "Checking", AccountAsset)
	revenue := mustAccount(t, s, ctx, "Sales Revenue", AccountRevenue)

	_, _, err := s.CreateJournalEntry(ctx, JournalEntry{
		Date: "2026-06-28",
		Memo: "Unbalanced entry",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 10000},
			{AccountID: revenue.ID, Credit: 9000},
		},
	})
	if err == nil {
		t.Fatal("expected unbalanced entry to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected validation error, got %#v", err)
	}

	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 0 {
		t.Fatalf("unbalanced entry was persisted: %#v", st.JournalEntries)
	}
}

func TestRBACDeniesLedgerWritesButAllowsConfiguredNoteWrites(t *testing.T) {
	s, owner := newTestStore(t)
	if _, err := s.UpsertUser(owner, User{ID: "ops", Role: "Operations"}); err != nil {
		t.Fatal(err)
	}
	ops := Context{Actor: "ops"}

	if _, _, err := s.CreateAccount(ops, "Unauthorized Cash", AccountAsset, "internal"); err == nil {
		t.Fatal("expected Operations user to be denied ledger writes")
	} else {
		var app *AppError
		if !errors.As(err, &app) || app.Code != ErrPermission {
			t.Fatalf("expected permission error, got %#v", err)
		}
	}

	note, _, err := s.UpsertNote(ops, "", "Ops handoff", "Close tickets before EOD.", "internal")
	if err != nil {
		t.Fatalf("expected Operations user to write notes: %v", err)
	}
	if note.CreatedBy != "ops" || !strings.HasPrefix(note.ID, "note:") {
		t.Fatalf("unexpected note metadata: %#v", note)
	}
}

func TestAccountExternalRefsAreStructuredAndUnique(t *testing.T) {
	s, ctx := newTestStore(t)
	acct, _, err := s.CreateAccountWithExternalRefs(ctx, "Mercury Checking ****1234", AccountAsset, "confidential", []ExternalSourceRef{{
		SourceSystem: " Mercury ",
		ExternalID:   "mercury-account-1",
		ExternalType: "bank_account",
		DisplayName:  "Mercury Operating Checking",
		URL:          "https://dashboard.mercury.com/accounts/mercury-account-1",
		Metadata: map[string]string{
			"account_kind": "checking",
			"nickname":     "Operating Checking",
			"mask":         "****1234",
			"last_four":    "1234",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(acct.ExternalRefs) != 1 {
		t.Fatalf("expected one external ref, got %#v", acct.ExternalRefs)
	}
	ref := acct.ExternalRefs[0]
	if ref.SourceSystem != "mercury" {
		t.Fatalf("expected normalized source system, got %#v", ref)
	}
	if ref.ExternalID != "mercury-account-1" ||
		ref.ExternalType != "bank_account" ||
		ref.DisplayName != "Mercury Operating Checking" ||
		ref.URL != "https://dashboard.mercury.com/accounts/mercury-account-1" ||
		ref.Metadata["account_kind"] != "checking" ||
		ref.Metadata["nickname"] != "Operating Checking" ||
		ref.Metadata["mask"] != "****1234" ||
		ref.Metadata["last_four"] != "1234" {
		t.Fatalf("unexpected external ref: %#v", ref)
	}

	qbAcct, _, err := s.CreateAccountWithExternalRefs(ctx, "Sales Tax Payable", AccountLiability, "confidential", []ExternalSourceRef{{
		SourceSystem: "quickbooks",
		ExternalID:   "42",
		ExternalType: "chart_account",
		DisplayName:  "Sales Tax Payable",
		Metadata: map[string]string{
			"classification": "liability",
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := qbAcct.ExternalRefs[0]; got.SourceSystem != "quickbooks" || got.ExternalType != "chart_account" || got.Metadata["classification"] != "liability" {
		t.Fatalf("unexpected non-bank external ref: %#v", got)
	}

	_, _, err = s.CreateAccountWithExternalRefs(ctx, "Duplicate Mercury Checking", AccountAsset, "confidential", []ExternalSourceRef{{
		SourceSystem: "mercury",
		ExternalID:   "mercury-account-1",
	}})
	if err == nil {
		t.Fatal("expected duplicate external ref to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected duplicate external ref conflict, got %#v", err)
	}
}

func TestAccountExternalRefValidation(t *testing.T) {
	s, ctx := newTestStore(t)
	_, _, err := s.CreateAccountWithExternalRefs(ctx, "Invalid External Ref", AccountAsset, "confidential", []ExternalSourceRef{{
		ExternalID: "missing-source",
	}})
	if err == nil {
		t.Fatal("expected missing source system to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected validation error, got %#v", err)
	}

	_, _, err = s.CreateAccountWithExternalRefs(ctx, "Invalid Last Four", AccountAsset, "confidential", []ExternalSourceRef{{
		SourceSystem: "mercury",
		ExternalID:   "bad-last-four",
		Metadata: map[string]string{
			"last_four": "12x4",
		},
	}})
	if err == nil {
		t.Fatal("expected invalid last_four to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected validation error, got %#v", err)
	}

	_, _, err = s.CreateAccountWithExternalRefs(ctx, "Invalid URL", AccountAsset, "confidential", []ExternalSourceRef{{
		SourceSystem: "mercury",
		ExternalID:   "bad-url",
		URL:          "http://dashboard.mercury.com/accounts/bad-url",
	}})
	if err == nil {
		t.Fatal("expected non-https URL to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected validation error, got %#v", err)
	}
}

func TestSourceTaggedJournalEntriesAreBalancedAndIdempotent(t *testing.T) {
	s, ctx := newTestStore(t)
	cash := mustAccount(t, s, ctx, "Operating Bank", AccountAsset)
	revenue := mustAccount(t, s, ctx, "Consulting Revenue", AccountRevenue)

	entry := JournalEntry{
		Date:      "2026-06-01",
		Memo:      "Agent-mapped external export row",
		Source:    "quickbooks_export",
		SourceKey: "qb-1",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 125000},
			{AccountID: revenue.ID, Credit: 125000},
		},
	}
	created, root, err := s.CreateJournalEntry(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	if created.Source != "quickbooks_export" || created.SourceKey != "qb-1" {
		t.Fatalf("source metadata was not preserved: %#v", created)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 1 {
		t.Fatalf("expected 1 journal entry, got %d", len(st.JournalEntries))
	}
	for _, entry := range st.JournalEntries {
		var debit, credit int64
		for _, p := range entry.Postings {
			debit += p.Debit
			credit += p.Credit
		}
		if debit != credit {
			t.Fatalf("source-tagged workflow created unbalanced entry %#v", entry)
		}
	}

	createdAgain, rootAgain, err := s.CreateJournalEntry(ctx, entry)
	if err != nil {
		t.Fatal(err)
	}
	if createdAgain.ID != created.ID || rootAgain != root {
		t.Fatalf("expected source-key idempotency, got entry=%s root=%s", createdAgain.ID, rootAgain)
	}
	st, err = s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 1 {
		t.Fatalf("duplicate source-tagged entry persisted entries: %d", len(st.JournalEntries))
	}

	conflicting := entry
	conflicting.Memo = "Changed external row with reused source key"
	_, _, err = s.CreateJournalEntry(ctx, conflicting)
	if err == nil {
		t.Fatal("expected reused source key with different content to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected source-key conflict, got %#v", err)
	}
}

func TestMerkleNodeTamperingIsDetected(t *testing.T) {
	s, ctx := newTestStore(t)
	_, root, err := s.UpsertNote(ctx, "", "Security memo", "Original", "confidential")
	if err != nil {
		t.Fatal(err)
	}
	path := s.nodePath(root)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.Replace(string(b), "ciphertext", "ciphertexu", 1))
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadState(); err == nil {
		t.Fatal("expected tampered node to fail integrity verification")
	} else {
		var app *AppError
		if !errors.As(err, &app) || app.Code != ErrValidation {
			t.Fatalf("expected integrity validation error, got %#v", err)
		}
	}
}

func TestNodePayloadsAreEncryptedAtRest(t *testing.T) {
	s, ctx := newTestStore(t)
	secret := "Cardholder data must not appear in plaintext."
	_, root, err := s.UpsertNote(ctx, "", "PCI memo", secret, "restricted")
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(s.nodePath(root))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("node file contains plaintext sensitive content: %s", s.nodePath(root))
	}
	if !strings.Contains(string(b), "AES-256-GCM") {
		t.Fatalf("node file does not advertise payload encryption: %s", string(b))
	}
}

func TestSnapshotsArePermissionedNamedRoots(t *testing.T) {
	s, owner := newTestStore(t)
	if _, err := s.UpsertUser(owner, User{ID: "sales", Role: "Sales Rep"}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.CreateSnapshot(owner, "fy2026-close")
	if err != nil {
		t.Fatal(err)
	}
	if snap.Root == "" || snap.Name != "fy2026-close" {
		t.Fatalf("unexpected snapshot: %#v", snap)
	}
	if _, err := s.CreateSnapshot(Context{Actor: "sales"}, "sales-savepoint"); err == nil {
		t.Fatal("expected Sales Rep snapshot creation to be denied")
	}
}

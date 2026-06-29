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

func TestQuickBooksImportIsBalancedAndIdempotent(t *testing.T) {
	s, ctx := newTestStore(t)
	cash := mustAccount(t, s, ctx, "Operating Bank", AccountAsset)
	revenue := mustAccount(t, s, ctx, "Consulting Revenue", AccountRevenue)
	expense := mustAccount(t, s, ctx, "Software Expense", AccountExpense)
	csvPath := s.Dir() + "/quickbooks.csv"
	data := "date,memo,account,amount_cents,source_id\n" +
		"2026-06-01,Invoice paid," + revenue.ID + ",125000,qb-1\n" +
		"2026-06-02,SaaS bill," + expense.ID + ",-1900,qb-2\n"
	if err := os.WriteFile(csvPath, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	report, err := s.ImportQuickBooksCSV(ctx, csvPath, cash.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 2 || report.Skipped != 0 {
		t.Fatalf("unexpected first import report: %#v", report)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 2 {
		t.Fatalf("expected 2 journal entries, got %d", len(st.JournalEntries))
	}
	for _, entry := range st.JournalEntries {
		var debit, credit int64
		for _, p := range entry.Postings {
			debit += p.Debit
			credit += p.Credit
		}
		if debit != credit {
			t.Fatalf("import created unbalanced entry %#v", entry)
		}
	}

	report, err = s.ImportQuickBooksCSV(ctx, csvPath, cash.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Imported != 0 || report.Skipped != 2 {
		t.Fatalf("expected idempotent second import, got %#v", report)
	}
	st, err = s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 2 {
		t.Fatalf("duplicate import persisted entries: %d", len(st.JournalEntries))
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

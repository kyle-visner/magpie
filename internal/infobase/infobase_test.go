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
		Date:         "2026-06-28",
		Memo:         "Unbalanced entry",
		ManualReason: "test unbalanced manual journal",
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

func TestBookAccountingBasisIsExplicitVersionedAndEnforced(t *testing.T) {
	s, ctx := newTestStore(t)
	settings, err := s.GetBookSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if settings.AccountingBasis != AccountingBasisCash {
		t.Fatalf("expected default cash basis, got %#v", settings)
	}
	if !settings.ModifiedCashPolicy.TrackSalesTaxLiability ||
		!settings.ModifiedCashPolicy.TrackLoanPrincipalLiability ||
		settings.ModifiedCashPolicy.UseAccountsReceivable ||
		settings.ModifiedCashPolicy.UseAccountsPayable {
		t.Fatalf("unexpected modified cash policy defaults: %#v", settings.ModifiedCashPolicy)
	}

	settings, _, err = s.SetAccountingBasis(ctx, AccountingBasisAccrual)
	if err != nil {
		t.Fatal(err)
	}
	if settings.AccountingBasis != AccountingBasisAccrual || settings.UpdatedBy != "owner" {
		t.Fatalf("unexpected updated settings: %#v", settings)
	}

	cash := mustAccount(t, s, ctx, "Operating Bank", AccountAsset)
	revenue := mustAccount(t, s, ctx, "Consulting Revenue", AccountRevenue)
	entry, _, err := s.CreateJournalEntry(ctx, JournalEntry{
		Date:         "2026-06-28",
		Memo:         "Accrual-basis posting",
		ManualReason: "test accrual posting",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 10000},
			{AccountID: revenue.ID, Credit: 10000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.AccountingBasis != AccountingBasisAccrual {
		t.Fatalf("expected journal entry to inherit active basis, got %#v", entry)
	}

	_, _, err = s.CreateJournalEntry(ctx, JournalEntry{
		Date:            "2026-06-28",
		Memo:            "Stale cash-basis posting",
		AccountingBasis: AccountingBasisCash,
		ManualReason:    "test stale posting",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 5000},
			{AccountID: revenue.ID, Credit: 5000},
		},
	})
	if err == nil {
		t.Fatal("expected mismatched journal accounting basis to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected basis mismatch validation error, got %#v", err)
	}

	_, _, err = s.SetAccountingBasis(ctx, AccountingBasisModifiedCash)
	if err == nil {
		t.Fatal("expected accounting basis change after journals to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected basis change validation error, got %#v", err)
	}

	nodes, err := s.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	var sawSettingsUpdate bool
	for _, node := range nodes {
		if node.Type == "book.settings" && node.Command == "book settings set" {
			sawSettingsUpdate = true
			break
		}
	}
	if !sawSettingsUpdate {
		t.Fatal("expected accounting basis update to be versioned in audit log")
	}
}

func TestOnlySettingsManagersCanChangeAccountingBasis(t *testing.T) {
	s, owner := newTestStore(t)
	if _, err := s.UpsertUser(owner, User{ID: "accountant", Role: "Accountant"}); err != nil {
		t.Fatal(err)
	}
	accountant := Context{Actor: "accountant"}

	if _, _, err := s.SetAccountingBasis(accountant, AccountingBasisAccrual); err == nil {
		t.Fatal("expected Accountant to be denied settings changes")
	} else {
		var app *AppError
		if !errors.As(err, &app) || app.Code != ErrPermission {
			t.Fatalf("expected permission error, got %#v", err)
		}
	}

	if _, err := s.GetBookSettings(accountant); err != nil {
		t.Fatalf("expected Accountant to read book settings: %v", err)
	}
}

func TestAccountRolesAreTypedUniqueAndUpdatable(t *testing.T) {
	s, ctx := newTestStore(t)
	cash, _, err := s.CreateAccountWithDetails(ctx, Account{
		Number:      "1000",
		Name:        "Operating Bank",
		Type:        AccountAsset,
		Role:        AccountRoleBankAccount,
		Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if cash.Role != AccountRoleBankAccount {
		t.Fatalf("expected role to be stored, got %#v", cash)
	}

	ar, _, err := s.CreateAccountWithDetails(ctx, Account{
		Number:      "1100",
		Name:        "Accounts Receivable",
		Type:        AccountAsset,
		Role:        AccountRoleAccountsReceivable,
		Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ar.Role != AccountRoleAccountsReceivable {
		t.Fatalf("expected A/R role, got %#v", ar)
	}

	updated, _, err := s.SetAccountRole(ctx, cash.ID, AccountRoleOperatingCash)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Role != AccountRoleOperatingCash {
		t.Fatalf("expected updated role, got %#v", updated)
	}

	_, _, err = s.CreateAccountWithDetails(ctx, Account{
		Name:        "Duplicate Accounts Receivable",
		Type:        AccountAsset,
		Role:        AccountRoleAccountsReceivable,
		Sensitivity: "confidential",
	})
	if err == nil {
		t.Fatal("expected duplicate unique account role to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected duplicate role conflict, got %#v", err)
	}

	_, _, err = s.CreateAccountWithDetails(ctx, Account{
		Name:        "Duplicate Operating Cash",
		Type:        AccountAsset,
		Role:        AccountRoleOperatingCash,
		Sensitivity: "confidential",
	})
	if err == nil {
		t.Fatal("expected duplicate operating_cash role to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected duplicate operating_cash conflict, got %#v", err)
	}

	_, _, err = s.CreateAccountWithDetails(ctx, Account{
		Name:        "Bad Sales Tax Role",
		Type:        AccountAsset,
		Role:        AccountRoleSalesTaxPayable,
		Sensitivity: "confidential",
	})
	if err == nil {
		t.Fatal("expected role/type mismatch to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected role/type validation error, got %#v", err)
	}

	nodes, err := s.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	var sawRoleUpdate bool
	for _, node := range nodes {
		if node.Type == "ledger.account" && node.Command == "ledger account role set" {
			sawRoleUpdate = true
			break
		}
	}
	if !sawRoleUpdate {
		t.Fatal("expected account role update to be versioned in audit log")
	}
}

func TestAccountRoleMutationRequiresChartManage(t *testing.T) {
	s, owner := newTestStore(t)
	if _, err := s.UpsertRole(owner, Role{
		Name: "Bookkeeping Agent",
		Permissions: []Permission{
			PermissionLedgerRead,
			PermissionLedgerWrite,
			PermissionAuditRead,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertUser(owner, User{ID: "bookkeeper", Role: "Bookkeeping Agent"}); err != nil {
		t.Fatal(err)
	}
	bookkeeper := Context{Actor: "bookkeeper"}
	acct, _, err := s.CreateAccountWithDetails(bookkeeper, Account{
		Name:        "Unclassified Agent Account",
		Type:        AccountAsset,
		Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatalf("expected ledger writer to create unclassified account: %v", err)
	}

	_, _, err = s.SetAccountRole(bookkeeper, acct.ID, AccountRoleBankAccount)
	if err == nil {
		t.Fatal("expected ledger writer without chart:manage to be denied account role changes")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrPermission {
		t.Fatalf("expected permission error, got %#v", err)
	}

	_, _, err = s.CreateAccountWithDetails(bookkeeper, Account{
		Name:        "Agent Classified Bank",
		Type:        AccountAsset,
		Role:        AccountRoleBankAccount,
		Sensitivity: "confidential",
	})
	if err == nil {
		t.Fatal("expected ledger writer without chart:manage to be denied role assignment at create time")
	}
	if !errors.As(err, &app) || app.Code != ErrPermission {
		t.Fatalf("expected permission error, got %#v", err)
	}
}

func TestManualJournalRequiresJournalAdjustAndReason(t *testing.T) {
	s, owner := newTestStore(t)
	if _, err := s.UpsertRole(owner, Role{
		Name: "Bookkeeping Agent",
		Permissions: []Permission{
			PermissionLedgerRead,
			PermissionLedgerWrite,
			PermissionAuditRead,
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertUser(owner, User{ID: "bookkeeper", Role: "Bookkeeping Agent"}); err != nil {
		t.Fatal(err)
	}
	cash := mustAccount(t, s, owner, "Operating Bank", AccountAsset)
	revenue := mustAccount(t, s, owner, "Consulting Revenue", AccountRevenue)
	bookkeeper := Context{Actor: "bookkeeper"}

	_, _, err := s.CreateJournalEntry(bookkeeper, JournalEntry{
		Date:         "2026-06-28",
		Memo:         "Agent arbitrary posting",
		ManualReason: "agent guessed at a journal",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 10000},
			{AccountID: revenue.ID, Credit: 10000},
		},
	})
	if err == nil {
		t.Fatal("expected bookkeeping agent without journal:adjust to be denied")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrPermission {
		t.Fatalf("expected permission error, got %#v", err)
	}

	_, _, err = s.CreateJournalEntry(owner, JournalEntry{
		Date: "2026-06-28",
		Memo: "Owner adjustment without reason",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 10000},
			{AccountID: revenue.ID, Credit: 10000},
		},
	})
	if err == nil {
		t.Fatal("expected manual journal without reason to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected validation error, got %#v", err)
	}

	created, _, err := s.CreateJournalEntry(owner, JournalEntry{
		Date:         "2026-06-28",
		Memo:         "Owner adjustment with reason",
		ManualReason: "opening review adjustment approved by owner",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 10000},
			{AccountID: revenue.ID, Credit: 10000},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Origin != JournalOriginManualAdjustment ||
		created.ManualReason != "opening review adjustment approved by owner" ||
		created.GeneratedBy != "owner" {
		t.Fatalf("unexpected manual journal metadata: %#v", created)
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

func TestAccountNumbersAreStructuredUniqueAndUpdatable(t *testing.T) {
	s, ctx := newTestStore(t)
	acct, _, err := s.CreateAccountWithDetails(ctx, Account{
		Number:      "1000",
		Name:        "Operating Bank",
		Type:        AccountAsset,
		Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if acct.Number != "1000" {
		t.Fatalf("expected account number to be stored, got %#v", acct)
	}

	updated, _, err := s.SetAccountNumber(ctx, acct.ID, "1010.01")
	if err != nil {
		t.Fatal(err)
	}
	if updated.Number != "1010.01" {
		t.Fatalf("expected updated account number, got %#v", updated)
	}

	_, _, err = s.CreateAccountWithDetails(ctx, Account{
		Number:      "1010.01",
		Name:        "Duplicate Number",
		Type:        AccountAsset,
		Sensitivity: "confidential",
	})
	if err == nil {
		t.Fatal("expected duplicate account number to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected duplicate number conflict, got %#v", err)
	}

	_, _, err = s.CreateAccountWithDetails(ctx, Account{
		Number:      "10 A",
		Name:        "Invalid Number",
		Type:        AccountAsset,
		Sensitivity: "confidential",
	})
	if err == nil {
		t.Fatal("expected invalid account number to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected invalid number validation error, got %#v", err)
	}

	nodes, err := s.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	var sawUpdate bool
	for _, node := range nodes {
		if node.Type == "ledger.account" && node.Command == "ledger account number set" {
			sawUpdate = true
			break
		}
	}
	if !sawUpdate {
		t.Fatal("expected account number update to be versioned in audit log")
	}
}

func TestAccountExternalRefCanBeAddedAndUpdatedAfterCreation(t *testing.T) {
	s, ctx := newTestStore(t)
	account := mustAccount(t, s, ctx, "Operating Bank", AccountAsset)

	updated, _, err := s.SetAccountExternalRef(ctx, account.ID, ExternalSourceRef{
		SourceSystem: "mercury",
		ExternalID:   "mercury-account-1",
		ExternalType: "bank_account",
		DisplayName:  "Mercury Operating Checking",
		Metadata: map[string]string{
			"last_four": "1234",
			"nickname":  "Operating",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.ExternalRefs) != 1 {
		t.Fatalf("expected external ref to be added, got %#v", updated.ExternalRefs)
	}
	if updated.ExternalRefs[0].Metadata["nickname"] != "Operating" {
		t.Fatalf("unexpected initial metadata: %#v", updated.ExternalRefs[0])
	}

	updated, _, err = s.SetAccountExternalRef(ctx, account.ID, ExternalSourceRef{
		SourceSystem: "mercury",
		ExternalID:   "mercury-account-1",
		ExternalType: "bank_account",
		DisplayName:  "Mercury Operating Checking",
		Metadata: map[string]string{
			"last_four": "1234",
			"nickname":  "Operating Updated",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.ExternalRefs) != 1 {
		t.Fatalf("expected matching external ref to be replaced, got %#v", updated.ExternalRefs)
	}
	if updated.ExternalRefs[0].Metadata["nickname"] != "Operating Updated" {
		t.Fatalf("expected updated metadata, got %#v", updated.ExternalRefs[0])
	}

	other := mustAccount(t, s, ctx, "Backup Bank", AccountAsset)
	_, _, err = s.SetAccountExternalRef(ctx, other.ID, ExternalSourceRef{
		SourceSystem: "mercury",
		ExternalID:   "mercury-account-1",
	})
	if err == nil {
		t.Fatal("expected duplicate external ref on another account to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected duplicate external ref conflict, got %#v", err)
	}

	nodes, err := s.AuditLog()
	if err != nil {
		t.Fatal(err)
	}
	var sawUpdate bool
	for _, node := range nodes {
		if node.Type == "ledger.account" && node.Command == "ledger account external-ref set" {
			sawUpdate = true
			break
		}
	}
	if !sawUpdate {
		t.Fatal("expected account external-ref update to be versioned in audit log")
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
		Date:         "2026-06-01",
		Memo:         "Agent-mapped external export row",
		ManualReason: "migration from agent-mapped external export",
		Source:       "quickbooks_export",
		SourceKey:    "qb-1",
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

	missingReason := entry
	missingReason.ManualReason = ""
	_, _, err = s.CreateJournalEntry(ctx, missingReason)
	if err == nil {
		t.Fatal("expected idempotent manual journal resubmission without reason to fail")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation {
		t.Fatalf("expected missing manual_reason validation error, got %#v", err)
	}

	conflicting := entry
	conflicting.Memo = "Changed external row with reused source key"
	_, _, err = s.CreateJournalEntry(ctx, conflicting)
	if err == nil {
		t.Fatal("expected reused source key with different content to fail")
	}
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected source-key conflict, got %#v", err)
	}
}

func TestLegacySourceTaggedJournalWithoutBasisReplaysWithEffectiveBasis(t *testing.T) {
	s, ctx := newTestStore(t)
	cash := mustAccount(t, s, ctx, "Operating Bank", AccountAsset)
	revenue := mustAccount(t, s, ctx, "Consulting Revenue", AccountRevenue)
	legacy := JournalEntry{
		ID:           "jrnl:legacy-source-entry",
		Date:         "2026-06-01",
		Memo:         "Legacy source-tagged entry",
		ManualReason: "replay legacy source-tagged entry",
		Source:       "legacy_export",
		SourceKey:    "legacy-1",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 125000},
			{AccountID: revenue.ID, Credit: 125000},
		},
		CreatedAt: time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		CreatedBy: "owner",
	}
	root, err := s.appendEvent(ctx, "ledger.journal", legacy.ID, "ledger journal create", wrapEvent("journal.create", journalCreatePayload{
		Entry:     legacy,
		SourceKey: "legacy_export:legacy-1",
	}), true)
	if err != nil {
		t.Fatal(err)
	}

	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	replayed := st.JournalEntries[legacy.ID]
	if replayed.AccountingBasis != AccountingBasisCash {
		t.Fatalf("expected legacy journal to replay with cash basis, got %#v", replayed)
	}

	createdAgain, rootAgain, err := s.CreateJournalEntry(ctx, legacy)
	if err != nil {
		t.Fatal(err)
	}
	if createdAgain.ID != legacy.ID || rootAgain != root {
		t.Fatalf("expected legacy source-key idempotency, got entry=%s root=%s", createdAgain.ID, rootAgain)
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

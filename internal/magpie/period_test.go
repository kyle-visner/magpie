package magpie

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestClosePreviewFindsReconciliationStagingAndRoleBlockers(t *testing.T) {
	s, owner := newTestStore(t)
	if _, _, err := s.SetAccountingBasis(owner, AccountingBasisAccrual); err != nil {
		t.Fatal(err)
	}
	bank := mustRoleAccount(t, s, owner, "1010", "Operating Bank", AccountAsset, AccountRoleOperatingCash)
	revenue := mustRoleAccount(t, s, owner, "4000", "Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	customer, _, err := s.UpsertCustomer(owner, Customer{Name: "Close Preview Customer"})
	if err != nil {
		t.Fatal(err)
	}
	invoice, _, err := s.CreateInvoice(owner, Invoice{
		InvoiceNumber: "INV-CLOSE-1", CustomerID: customer.ID, InvoiceDate: "2026-06-15",
		LineItems:     []InvoiceLineItem{{Description: "Service", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 10000, AmountCents: 10000}},
		SubtotalCents: 10000, TotalCents: 10000,
	})
	if err != nil {
		t.Fatal(err)
	}

	preview, err := s.PreviewPeriodClose(owner, "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	for _, code := range []string{"unreconciled_financial_account", "unposted_source_document", "missing_account_role"} {
		if !hasBlocker(preview, code) {
			t.Fatalf("expected blocker %s in %#v", code, preview.Blockers)
		}
	}
	if _, err := s.CompletePeriodClose(owner, "2026-06-30"); err == nil {
		t.Fatal("expected blocked close to fail")
	}
	if _, _, err := s.SetAccountExternalRef(owner, bank.ID, ExternalSourceRef{SourceSystem: "bank", ExternalID: "checking-1", Metadata: map[string]string{"reconciled_through": "2026-06-30"}}); err != nil {
		t.Fatal(err)
	}
	_ = invoice
}

func TestReportsAreDeterministicAndBalancedAcrossAccountingBases(t *testing.T) {
	for _, basis := range []AccountingBasis{AccountingBasisCash, AccountingBasisModifiedCash, AccountingBasisAccrual} {
		t.Run(string(basis), func(t *testing.T) {
			s, owner := newTestStore(t)
			if basis != AccountingBasisCash {
				if _, _, err := s.SetAccountingBasis(owner, basis); err != nil {
					t.Fatal(err)
				}
			}
			cash := mustAccount(t, s, owner, "Cash", AccountAsset)
			revenue := mustAccount(t, s, owner, "Revenue", AccountRevenue)
			expense := mustAccount(t, s, owner, "Expense", AccountExpense)
			postManual(t, s, owner, "2026-06-05", "Sale", cash, revenue, 10000)
			postManual(t, s, owner, "2026-06-06", "Expense", expense, cash, 2000)
			postManual(t, s, owner, "2026-06-07", "Expense reversal", cash, expense, 500)

			tb1, err := s.TrialBalance(owner, "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			tb2, err := s.TrialBalance(owner, "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			assertSameReport(t, tb1, tb2)
			if tb1.TotalDebitCents != tb1.TotalCreditCents {
				t.Fatalf("trial balance mismatch: %#v", tb1)
			}
			pl1, err := s.ProfitLoss(owner, "2026-06-01", "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			pl2, err := s.ProfitLoss(owner, "2026-06-01", "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			assertSameReport(t, pl1, pl2)
			if pl1.TotalRevenueCents != 10000 || pl1.TotalExpenseCents != 1500 || pl1.NetIncomeCents != 8500 {
				t.Fatalf("unexpected P&L: %#v", pl1)
			}
			bs, err := s.BalanceSheet(owner, "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			if bs.TotalAssetsCents != bs.LiabilitiesEquityCents {
				t.Fatalf("balance sheet mismatch: %#v", bs)
			}
			gl1, err := s.GeneralLedger(owner, "2026-06-01", "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			gl2, err := s.GeneralLedger(owner, "2026-06-01", "2026-06-30")
			if err != nil {
				t.Fatal(err)
			}
			assertSameReport(t, gl1, gl2)
			csv1, err := ReportCSV(gl1)
			if err != nil {
				t.Fatal(err)
			}
			csv2, err := ReportCSV(gl2)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(csv1, csv2) {
				t.Fatal("general ledger CSV is not deterministic")
			}
		})
	}
}

func TestEmptyPeriodClosePackageBackdateReopenAndRevision(t *testing.T) {
	s, owner := newTestStore(t)
	asset := mustAccount(t, s, owner, "Cash", AccountAsset)
	equity := mustAccount(t, s, owner, "Opening Equity", AccountEquity)
	revenue := mustRoleAccount(t, s, owner, "4000", "Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	customer, _, err := s.UpsertCustomer(owner, Customer{Name: "Closed Period Customer"})
	if err != nil {
		t.Fatal(err)
	}

	close, err := s.CompletePeriodClose(owner, "2026-05-31")
	if err != nil {
		t.Fatal(err)
	}
	if close.Manifest.SourceRoot == "" || close.Root == "" || close.Manifest.SnapshotName == "" || len(close.Manifest.ReportSHA256) != 8 {
		t.Fatalf("incomplete close provenance: %#v", close)
	}
	if ref, err := s.db.NamedRef(close.Manifest.SnapshotName); err != nil || ref != close.Root {
		t.Fatalf("close snapshot ref=%q err=%v", ref, err)
	}

	_, _, err = s.CreateJournalEntry(owner, JournalEntry{Date: "2026-05-15", Memo: "Backdated", ManualReason: "must fail", Postings: []Posting{{AccountID: asset.ID, Debit: 100}, {AccountID: equity.ID, Credit: 100}}})
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation || !strings.Contains(app.Message, "closed period") {
		t.Fatalf("expected closed-period validation, got %v", err)
	}
	_, _, err = s.CreateInvoice(owner, Invoice{
		InvoiceNumber: "INV-BACKDATED", CustomerID: customer.ID, InvoiceDate: "2026-05-20",
		LineItems:     []InvoiceLineItem{{Description: "Service", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 100, AmountCents: 100}},
		SubtotalCents: 100, TotalCents: 100,
	})
	if !errors.As(err, &app) || !strings.Contains(app.Message, "closed period") {
		t.Fatalf("expected backdated source document to fail, got %v", err)
	}

	if _, err := s.ReopenPeriod(owner, "2026-05-31", ""); err == nil {
		t.Fatal("expected blank reopen reason to fail")
	}
	if _, err := s.UpsertUser(owner, User{ID: "accountant", Role: "Accountant"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReopenPeriod(Context{Actor: "accountant"}, "2026-05-31", "not privileged"); err == nil {
		t.Fatal("expected accountant reopen to be denied")
	}
	reopen, err := s.ReopenPeriod(owner, "2026-05-31", "late bank statement correction")
	if err != nil {
		t.Fatal(err)
	}
	if reopen.CloseID != close.ID || reopen.Reason == "" {
		t.Fatalf("unexpected reopen: %#v", reopen)
	}
	postManual(t, s, owner, "2026-05-15", "Correction", asset, equity, 100)
	revised, err := s.CompletePeriodClose(owner, "2026-05-31")
	if err != nil {
		t.Fatal(err)
	}
	if revised.Revision != 2 || revised.Manifest.PreviousCloseID != close.ID || revised.Manifest.CorrectionReason != reopen.Reason {
		t.Fatalf("revision did not preserve correction chain: %#v", revised)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.PeriodCloses) != 2 || st.PeriodCloses[close.ID].Root != close.Root {
		t.Fatalf("original close was not preserved: %#v", st.PeriodCloses)
	}

	pkg, err := s.BuildClosePackage(owner, "2026-05-31")
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Close.ID != revised.ID || len(pkg.Files) != 9 {
		t.Fatalf("unexpected close package: close=%s files=%d", pkg.Close.ID, len(pkg.Files))
	}
	var packaged PeriodClose
	if err := json.Unmarshal(pkg.Files["manifest.json"], &packaged); err != nil {
		t.Fatal(err)
	}
	if packaged.Manifest.SourceRoot != revised.Manifest.SourceRoot || packaged.Manifest.PreviousCloseID != close.ID {
		t.Fatalf("package provenance mismatch: %#v", packaged)
	}
}

func hasBlocker(preview ClosePreview, code string) bool {
	for _, blocker := range preview.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func postManual(t *testing.T, s *Store, ctx Context, date, memo string, debit, credit Account, amount int64) {
	t.Helper()
	if _, _, err := s.CreateJournalEntry(ctx, JournalEntry{Date: date, Memo: memo, ManualReason: "period report test", Postings: []Posting{{AccountID: debit.ID, Debit: amount}, {AccountID: credit.ID, Credit: amount}}}); err != nil {
		t.Fatal(err)
	}
}

func assertSameReport(t *testing.T, a, b any) {
	t.Helper()
	left, err := CanonicalJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) {
		t.Fatalf("reports differ:\n%s\n%s", left, right)
	}
}

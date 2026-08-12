package magpie

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type failNamedRefBackend struct {
	storageBackend
	failures int
}

func (b *failNamedRefBackend) WriteNamedRefAt(name, root, expectedRoot string) error {
	if b.failures > 0 {
		b.failures--
		return errors.New("injected named-ref failure")
	}
	return b.storageBackend.WriteNamedRefAt(name, root, expectedRoot)
}

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

func TestClosePreviewCoversEveryCurrentStagedDomainType(t *testing.T) {
	s, owner := newTestStore(t)
	customer, _, err := s.UpsertCustomer(owner, Customer{Name: "Staged Work Customer"})
	if err != nil {
		t.Fatal(err)
	}
	revenue := mustRoleAccount(t, s, owner, "4000", "Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	invoice, _, err := s.CreateInvoice(owner, Invoice{
		InvoiceNumber: "INV-STAGED", CustomerID: customer.ID, InvoiceDate: "2026-06-15",
		LineItems:     []InvoiceLineItem{{Description: "Service", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 100, AmountCents: 100}},
		SubtotalCents: 100, TotalCents: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	payout := Payout{ID: "payout:staged", Date: "2026-06-20", NetAmountCents: 100, JournalEntryIDs: []string{}}
	if _, err := s.appendEventAt(owner, "payout", payout.ID, "test staged payout", wrapEvent("payout.create", payoutCreatePayload{Payout: payout}), st.Root); err != nil {
		t.Fatal(err)
	}

	preview, err := s.PreviewPeriodClose(owner, "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if !hasBlockerForEntity(preview, "unposted_source_document", invoice.ID) {
		t.Fatalf("staged invoice was not blocked: %#v", preview.Blockers)
	}
	if !hasBlockerForEntity(preview, "unposted_source_document", payout.ID) {
		t.Fatalf("staged payout was not blocked: %#v", preview.Blockers)
	}
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
	if close.Manifest.PackageID == "" || close.Manifest.OriginalPackageID != close.Manifest.PackageID {
		t.Fatalf("original package identity is incomplete: %#v", close.Manifest)
	}
	if revised.Manifest.PackageID == close.Manifest.PackageID || revised.Manifest.OriginalPackageID != close.Manifest.PackageID || revised.Manifest.PreviousPackageID != close.Manifest.PackageID {
		t.Fatalf("revised package did not preserve original identity and append lineage: %#v", revised.Manifest)
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
	originalPackage, err := s.BuildClosePackageByID(owner, close.ID)
	if err != nil {
		t.Fatal(err)
	}
	if originalPackage.Close.ID != close.ID || originalPackage.Close.Manifest.PackageID != close.Manifest.PackageID {
		t.Fatalf("original package revision is not reproducible by stable identity: %#v", originalPackage.Close)
	}
}

func TestClosedPeriodJournalChokePointCoversEveryPostingWorkflow(t *testing.T) {
	s, owner := newTestStore(t)
	debit := mustAccount(t, s, owner, "Debit", AccountAsset)
	credit := mustAccount(t, s, owner, "Credit", AccountEquity)
	if _, err := s.CompletePeriodClose(owner, "2026-05-31"); err != nil {
		t.Fatal(err)
	}
	for _, workflow := range []string{"invoice.post", "invoice.mark_paid", "invoice.reverse_payment", "payout.receive", "payout.fee"} {
		t.Run(workflow, func(t *testing.T) {
			_, _, err := s.createWorkflowJournalEntry(owner, workflowJournalRequest{
				Date: "2026-05-15", Memo: workflow, Workflow: workflow, PostingSemantics: "test",
				SourceDocumentType: strings.Split(workflow, ".")[0], SourceDocumentID: workflow,
				Source: "period-test", SourceKey: workflow,
				Postings: []Posting{{AccountID: debit.ID, Debit: 100}, {AccountID: credit.ID, Credit: 100}},
			})
			assertClosedPeriodError(t, err)
		})
	}
	_, _, err := s.CreateJournalEntry(owner, JournalEntry{Date: "2026-05-15", Memo: "manual", ManualReason: "test", Postings: []Posting{{AccountID: debit.ID, Debit: 100}, {AccountID: credit.ID, Credit: 100}}})
	assertClosedPeriodError(t, err)
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 0 {
		t.Fatalf("closed-period choke point appended journals: %#v", st.JournalEntries)
	}
}

func TestClosedPeriodPublicPaymentReversalAndPayoutPaths(t *testing.T) {
	s, owner := newTestStore(t)
	bank := mustRoleAccount(t, s, owner, "1010", "Bank", AccountAsset, AccountRoleOperatingCash)
	if _, _, err := s.SetAccountExternalRef(owner, bank.ID, ExternalSourceRef{SourceSystem: "bank", ExternalID: "bank-1", Metadata: map[string]string{"reconciled_through": "2026-05-31"}}); err != nil {
		t.Fatal(err)
	}
	revenue := mustRoleAccount(t, s, owner, "4000", "Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	clearing := mustAccount(t, s, owner, "Clearing", AccountAsset)
	if _, err := s.CompletePeriodClose(owner, "2026-05-31"); err != nil {
		t.Fatal(err)
	}
	customer, _, err := s.UpsertCustomer(owner, Customer{Name: "Open Period Customer"})
	if err != nil {
		t.Fatal(err)
	}
	st := mustState(t, s)
	legacyClosedInvoice, err := normalizeInvoice(st, Invoice{
		InvoiceNumber: "INV-LEGACY-CLOSED", CustomerID: customer.ID, InvoiceDate: "2026-05-30",
		LineItems:     []InvoiceLineItem{{Description: "Legacy service", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 100, AmountCents: 100}},
		SubtotalCents: 100, TotalCents: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.appendEventAt(owner, "invoice", legacyClosedInvoice.ID, "test legacy staged invoice", wrapEvent("invoice.create", invoiceCreatePayload{Invoice: legacyClosedInvoice}), st.Root); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.PostInvoice(owner, legacyClosedInvoice.ID)
	assertClosedPeriodError(t, err)
	invoice, _, err := s.CreateInvoice(owner, Invoice{
		InvoiceNumber: "INV-JUNE", CustomerID: customer.ID, InvoiceDate: "2026-06-01",
		LineItems:     []InvoiceLineItem{{Description: "Service", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 100, AmountCents: 100}},
		SubtotalCents: 100, TotalCents: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	invoice, _, err = s.PostInvoice(owner, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.MarkInvoicePaid(owner, invoice.ID, InvoicePaymentRequest{Date: "2026-05-31", AmountCents: 100, CashAccountID: bank.ID})
	assertClosedPeriodError(t, err)
	invoice, _, err = s.MarkInvoicePaid(owner, invoice.ID, InvoicePaymentRequest{Date: "2026-06-02", AmountCents: 100, CashAccountID: bank.ID})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ReverseInvoicePayment(owner, invoice.ID, InvoicePaymentReversalRequest{PaymentID: invoice.Payments[0].ID, Date: "2026-05-31", Reason: "test closed reversal"})
	assertClosedPeriodError(t, err)
	_, _, err = s.ImportPayout(owner, Payout{Date: "2026-05-31", SourceAccountID: clearing.ID, DestinationAccountID: bank.ID, NetAmountCents: 100, ExternalRefs: []ExternalSourceRef{{SourceSystem: "processor", ExternalID: "payout-closed"}}})
	assertClosedPeriodError(t, err)
}

func TestCloseReportWindowUsesBookStartThenDayAfterPreviousActiveClose(t *testing.T) {
	s, owner := newTestStore(t)
	asset := mustAccount(t, s, owner, "Cash", AccountAsset)
	equity := mustAccount(t, s, owner, "Equity", AccountEquity)
	postManual(t, s, owner, "2026-03-15", "Book start", asset, equity, 100)
	parameters, err := closeReportParameters(mustState(t, s), "2026-04-30")
	if err != nil {
		t.Fatal(err)
	}
	if parameters.ProfitLossFrom != "2026-03-15" || parameters.GeneralLedgerFrom != "2026-03-15" {
		t.Fatalf("first close did not use book start: %#v", parameters)
	}
	if _, err := s.CompletePeriodClose(owner, "2026-04-30"); err != nil {
		t.Fatal(err)
	}
	postManual(t, s, owner, "2026-06-10", "Next period", asset, equity, 50)
	parameters, err = closeReportParameters(mustState(t, s), "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	if parameters.ProfitLossFrom != "2026-05-01" || parameters.GeneralLedgerFrom != "2026-05-01" {
		t.Fatalf("later close did not start after previous active close: %#v", parameters)
	}
	if _, err := closeReportParameters(mustState(t, s), "not-a-date"); err == nil {
		t.Fatal("expected malformed close through date to fail")
	}
}

func TestCloseNamedRefFailureRepairsBeforeLaterCloseAndReopen(t *testing.T) {
	for _, next := range []string{"later-close", "reopen"} {
		t.Run(next, func(t *testing.T) {
			s, owner := newTestStore(t)
			failing := &failNamedRefBackend{storageBackend: s.db, failures: 1}
			s.db = failing
			if _, err := s.CompletePeriodClose(owner, "2026-05-31"); err == nil || !strings.Contains(err.Error(), "injected named-ref failure") {
				t.Fatalf("expected injected ref failure, got %v", err)
			}
			st := mustState(t, s)
			failedClose, ok := latestCloseForThrough(st, "2026-05-31")
			if !ok {
				t.Fatal("close event was not durable before ref failure")
			}
			switch next {
			case "later-close":
				if _, err := s.CompletePeriodClose(owner, "2026-06-30"); err != nil {
					t.Fatal(err)
				}
			case "reopen":
				if _, err := s.ReopenPeriod(owner, "2026-05-31", "repair test"); err != nil {
					t.Fatal(err)
				}
			}
			ref, err := s.db.NamedRef(failedClose.Manifest.SnapshotName)
			if err != nil || ref != failedClose.Root {
				t.Fatalf("failed close ref was not repaired: ref=%q want=%q err=%v", ref, failedClose.Root, err)
			}
		})
	}
}

func TestCloseRefRepairNeverOverwritesConflictingDurableRef(t *testing.T) {
	s, owner := newTestStore(t)
	close, err := s.CompletePeriodClose(owner, "2026-05-31")
	if err != nil {
		t.Fatal(err)
	}
	conflictingRoot := close.Manifest.SourceRoot
	if err := s.db.WriteNamedRefAt(close.Manifest.SnapshotName, conflictingRoot, close.Root); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CompletePeriodClose(owner, "2026-05-31"); err == nil {
		t.Fatal("expected conflicting close ref to fail integrity validation")
	} else {
		var app *AppError
		if !errors.As(err, &app) || app.Code != ErrIntegrity {
			t.Fatalf("expected integrity error, got %v", err)
		}
	}
	ref, err := s.db.NamedRef(close.Manifest.SnapshotName)
	if err != nil || ref != conflictingRoot {
		t.Fatalf("repair overwrote conflicting ref: ref=%q want=%q err=%v", ref, conflictingRoot, err)
	}
}

func TestActiveCloseTieBreakIsDeterministic(t *testing.T) {
	st := emptyState()
	st.PeriodCloses["close:a"] = PeriodClose{ID: "close:a", Through: "2026-05-31", Revision: 1}
	st.PeriodCloses["close:b"] = PeriodClose{ID: "close:b", Through: "2026-05-31", Revision: 1}
	for range 100 {
		close, ok := latestActiveClose(st)
		if !ok || close.ID != "close:b" {
			t.Fatalf("nondeterministic tied close selection: %#v", close)
		}
	}
}

func TestCanonicalEmptyReportsUseStableCollections(t *testing.T) {
	s, owner := newTestStore(t)
	tb, err := s.TrialBalance(owner, "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	pl, err := s.ProfitLoss(owner, "2026-06-01", "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	gl, err := s.GeneralLedger(owner, "2026-06-01", "2026-06-30")
	if err != nil {
		t.Fatal(err)
	}
	for name, report := range map[string]any{"trial-balance": tb, "profit-loss": pl, "general-ledger": gl} {
		data, err := CanonicalJSON(report)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte(":null")) {
			t.Fatalf("%s contains unstable null collection: %s", name, data)
		}
		again, err := CanonicalJSON(report)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatalf("%s canonical JSON changed: %v", name, err)
		}
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

func hasBlockerForEntity(preview ClosePreview, code, entityID string) bool {
	for _, blocker := range preview.Blockers {
		if blocker.Code == code && blocker.EntityID == entityID {
			return true
		}
	}
	return false
}

func assertClosedPeriodError(t *testing.T, err error) {
	t.Helper()
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrValidation || !strings.Contains(app.Message, "closed period") {
		t.Fatalf("expected closed-period validation error, got %v", err)
	}
}

func mustState(t *testing.T, s *Store) State {
	t.Helper()
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	return st
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

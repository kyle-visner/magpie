package magpie

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedInvoiceBook(t *testing.T, s *Store, ctx Context) (cash, revenue Account, customer Customer) {
	t.Helper()
	cash = mustRoleAccount(t, s, ctx, "1010", "Operating Bank", AccountAsset, AccountRoleOperatingCash)
	revenue = mustRoleAccount(t, s, ctx, "4000", "Service Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	var err error
	customer, _, err = s.UpsertCustomer(ctx, Customer{Name: "Acme Roofing", Email: "ap@acme.test", Address: "1 Main St"})
	if err != nil {
		t.Fatal(err)
	}
	return cash, revenue, customer
}

func createDraftInvoice(t *testing.T, s *Store, ctx Context, customer Customer, revenue Account, number string, amount int64) Invoice {
	t.Helper()
	invoice, _, err := s.CreateInvoice(ctx, Invoice{
		InvoiceNumber: number,
		CustomerID:    customer.ID,
		InvoiceDate:   "2026-08-27",
		DueDate:       "2026-09-26",
		Terms:         "net_30",
		Currency:      "USD",
		Status:        InvoiceStatusDraft,
		LineItems: []InvoiceLineItem{{
			Description:      "Roof tear-off — 12 sq",
			RevenueAccountID: revenue.ID,
			Quantity:         1,
			UnitAmountCents:  amount,
			AmountCents:      amount,
			JobRef:           "job:roof-12",
		}},
		SubtotalCents: amount,
		TotalCents:    amount,
		Branding: InvoiceBranding{
			LegalName: "Tenant LLC",
			Email:     "billing@tenant.test",
		},
		PaymentInstructions: InvoicePaymentInstructions{
			ACH: &PaymentInstruction{BankName: "Local Bank", AccountLast4: "4321", RoutingLast4: "6789"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return invoice
}

func TestIssueSnapshotPublicURLShowsImmutableTotals(t *testing.T) {
	s, ctx := newTestStore(t)
	_, revenue, customer := seedInvoiceBook(t, s, ctx)
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-2026-0041", 540000)

	issued, _, err := s.IssueInvoice(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issued.Status != InvoiceStatusIssued || issued.IssuedSnapshot == nil {
		t.Fatalf("expected issued snapshot, got %#v", issued)
	}
	if issued.IssuedSnapshot.TotalCents != 540000 {
		t.Fatalf("snapshot totals: %#v", issued.IssuedSnapshot)
	}

	link, raw, err := s.PublicLink(ctx, issued.ID, InvoicePublicOptions{Tenant: "tenant-a", PublicBaseURL: "https://invoices.test"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if raw == "" || link.URL == "" || strings.Contains(link.URL, issued.ID) == false && !strings.Contains(link.URL, raw) {
		t.Fatalf("expected public URL with token, got %#v", link)
	}
	if issued.PublicTokenHash == raw {
		t.Fatal("raw public token must not be stored")
	}

	view, err := s.LookupPublicInvoice("tenant-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	if view.TotalCents != 540000 || view.AmountDueCents != 540000 || view.InvoiceNumber != "INV-2026-0041" {
		t.Fatalf("public view totals: %#v", view)
	}
	html := RenderPublicInvoiceHTML(view, "")
	if !strings.Contains(html, "INV-2026-0041") || !strings.Contains(html, "USD 5400.00") {
		t.Fatalf("html missing totals: %s", html)
	}
	pdf, err := RenderInvoicePDF(view)
	if err != nil || !strings.HasPrefix(string(pdf), "%PDF") {
		t.Fatalf("pdf: %v %q", err, pdf[:min(20, len(pdf))])
	}

	draft.LineItems[0].UnitAmountCents = 1
	draft.LineItems[0].AmountCents = 1
	draft.SubtotalCents = 1
	draft.TotalCents = 1
	if _, _, err := s.UpdateDraftInvoice(ctx, Invoice{
		ID:            issued.ID,
		InvoiceNumber: "INV-HACKED",
		CustomerID:    customer.ID,
		InvoiceDate:   "2026-08-27",
		LineItems:     draft.LineItems,
	}); err == nil {
		t.Fatal("expected edit after issue to be rejected")
	}
	again, err := s.LookupPublicInvoice("tenant-a", raw)
	if err != nil {
		t.Fatal(err)
	}
	if again.TotalCents != 540000 {
		t.Fatalf("public totals changed after rejected edit: %#v", again)
	}
}

func TestVoidAndCreditMemoAfterIssue(t *testing.T) {
	s, ctx := newTestStore(t)
	_, revenue, customer := seedInvoiceBook(t, s, ctx)
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-VOID-1", 100000)
	issued, _, err := s.IssueInvoice(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	voided, _, err := s.VoidInvoice(ctx, issued.ID, "wrong customer")
	if err != nil {
		t.Fatal(err)
	}
	if voided.Status != SourceDocumentVoid || voided.VoidReason != "wrong customer" {
		t.Fatalf("voided: %#v", voided)
	}
	link, raw, err := s.PublicLink(ctx, voided.ID, InvoicePublicOptions{Tenant: "t1"}, false)
	if err != nil {
		t.Fatal(err)
	}
	view, err := s.LookupPublicInvoice("t1", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !view.Void || !view.PayDisabled {
		t.Fatalf("voided public view should disable pay: %#v link=%#v", view, link)
	}

	memo, _, err := s.CreateCreditMemo(ctx, Invoice{
		InvoiceNumber:     "CM-1",
		CustomerID:        customer.ID,
		InvoiceDate:       "2026-08-28",
		CreditOfInvoiceID: issued.ID,
		LineItems: []InvoiceLineItem{{
			Description:      "Credit roof tear-off",
			RevenueAccountID: revenue.ID,
			Quantity:         1,
			UnitAmountCents:  100000,
			AmountCents:      100000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if memo.Kind != InvoiceKindCreditMemo || memo.CreditOfInvoiceID != issued.ID {
		t.Fatalf("credit memo: %#v", memo)
	}
	issuedMemo, _, err := s.IssueInvoice(ctx, memo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if issuedMemo.Status != InvoiceStatusIssued {
		t.Fatalf("issued credit memo: %#v", issuedMemo)
	}
}

func TestTwoTenantsCannotSeeEachOthersTokens(t *testing.T) {
	a, ctxA := newTestStore(t)
	b, ctxB := newTestStore(t)
	_, revA, custA := seedInvoiceBook(t, a, ctxA)
	_, revB, custB := seedInvoiceBook(t, b, ctxB)
	invA := createDraftInvoice(t, a, ctxA, custA, revA, "INV-A-1", 1000)
	invB := createDraftInvoice(t, b, ctxB, custB, revB, "INV-B-1", 2000)
	if _, _, err := a.IssueInvoice(ctxA, invA.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := b.IssueInvoice(ctxB, invB.ID); err != nil {
		t.Fatal(err)
	}
	_, tokenA, err := a.PublicLink(ctxA, invA.ID, InvoicePublicOptions{Tenant: "alpha"}, false)
	if err != nil {
		t.Fatal(err)
	}
	_, tokenB, err := b.PublicLink(ctxB, invB.ID, InvoicePublicOptions{Tenant: "bravo"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.LookupPublicInvoice("alpha", tokenB); err == nil {
		t.Fatal("tenant A looked up tenant B token")
	}
	if _, err := b.LookupPublicInvoice("bravo", tokenA); err == nil {
		t.Fatal("tenant B looked up tenant A token")
	}
	viewA, err := a.LookupPublicInvoice("alpha", tokenA)
	if err != nil || viewA.InvoiceNumber != "INV-A-1" {
		t.Fatalf("tenant A own token: %v %#v", err, viewA)
	}
}

func TestManualACHMarkPaidRequiresReasonAndIsIdempotentOnRefs(t *testing.T) {
	s, ctx := newTestStore(t)
	cash, revenue, customer := seedInvoiceBook(t, s, ctx)
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-ACH-1", 100000)
	if _, _, err := s.IssueInvoice(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.MarkInvoicePaid(ctx, draft.ID, InvoicePaymentRequest{
		Date:          "2026-08-28",
		AmountCents:   100000,
		CashAccountID: cash.ID,
	}); err == nil {
		t.Fatal("expected mark-paid without evidence to fail")
	}
	paid, _, err := s.MarkInvoicePaid(ctx, draft.ID, InvoicePaymentRequest{
		PaidDate:      "2026-08-28",
		AmountCents:   100000,
		CashAccountID: cash.ID,
		ManualReason:  "ACH received 2026-08-28 bank feed match",
	})
	if err != nil {
		t.Fatal(err)
	}
	if paid.Status != SourceDocumentPaid || paid.AmountPaidCents != 100000 {
		t.Fatalf("manual pay: %#v", paid)
	}

	stripeInv := createDraftInvoice(t, s, ctx, customer, revenue, "INV-STRIPE-1", 100000)
	if _, _, err := s.IssueInvoice(ctx, stripeInv.ID); err != nil {
		t.Fatal(err)
	}
	refs := []ExternalSourceRef{{
		SourceSystem: "stripe",
		ExternalID:   "evt_replay",
		ExternalType: "event",
	}}
	first, root1, err := s.MarkInvoicePaid(ctx, stripeInv.ID, InvoicePaymentRequest{
		Date:          "2026-08-28",
		AmountCents:   100000,
		CashAccountID: cash.ID,
		ExternalRefs:  refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	replay, root2, err := s.MarkInvoicePaid(ctx, stripeInv.ID, InvoicePaymentRequest{
		Date:          "2026-08-28",
		AmountCents:   100000,
		CashAccountID: cash.ID,
		ExternalRefs:  refs,
	})
	if err != nil {
		t.Fatal(err)
	}
	if root2 != root1 || len(replay.Payments) != 1 || len(first.Payments) != 1 {
		t.Fatalf("stripe event replay should be a no-op, roots %s vs %s payments=%d", root1, root2, len(replay.Payments))
	}
}

func TestPayThousandInvoiceJournalsMatchCashBasis(t *testing.T) {
	s, ctx := newTestStore(t)
	cash, revenue, customer := seedInvoiceBook(t, s, ctx)
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-1000", 100000)
	if _, _, err := s.IssueInvoice(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	paid, _, err := s.MarkInvoicePaid(ctx, draft.ID, InvoicePaymentRequest{
		Date:          "2026-08-28",
		AmountCents:   100000,
		CashAccountID: cash.ID,
		ExternalRefs: []ExternalSourceRef{
			{SourceSystem: "stripe", ExternalID: "cs_test_1", ExternalType: "checkout_session"},
			{SourceSystem: "stripe", ExternalID: "pi_test_1", ExternalType: "payment_intent"},
			{SourceSystem: "stripe", ExternalID: "evt_test_1", ExternalType: "event"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	entry := st.JournalEntries[paid.PaymentJournalEntryIDs[0]]
	assertPosting(t, entry, cash.ID, 100000, 0)
	assertPosting(t, entry, revenue.ID, 0, 100000)
	for _, journal := range st.JournalEntries {
		if strings.Contains(journal.Memo, "platform") || strings.Contains(strings.ToLower(journal.Memo), "future perfect") {
			t.Fatalf("platform charge appeared in tenant journals: %#v", journal)
		}
	}
}

func TestRailWebhookActorCannotCreateManualJournals(t *testing.T) {
	s, owner := newTestStore(t)
	cash := mustRoleAccount(t, s, owner, "1010", "Operating Bank", AccountAsset, AccountRoleOperatingCash)
	revenue := mustRoleAccount(t, s, owner, "4000", "Service Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	rail := Context{Actor: "rail-webhook"}
	_, _, err := s.CreateJournalEntry(rail, JournalEntry{
		Date:         "2026-08-28",
		Memo:         "manual bypass",
		ManualReason: "should be denied",
		Postings: []Posting{
			{AccountID: cash.ID, Debit: 1000},
			{AccountID: revenue.ID, Credit: 1000},
		},
	})
	if err == nil {
		t.Fatal("rail-webhook created a manual journal")
	}
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrPermission {
		t.Fatalf("expected permission error, got %#v", err)
	}
	if _, _, err := s.SetAccountingBasis(rail, AccountingBasisAccrual); err == nil {
		t.Fatal("rail-webhook changed book settings")
	}
}

func TestInvoiceCollectReadinessFailsClosedWithoutCashRole(t *testing.T) {
	s, ctx := newTestStore(t)
	revenue := mustRoleAccount(t, s, ctx, "4000", "Service Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	mustRoleAccount(t, s, ctx, "6500", "Processing Fees", AccountExpense, AccountRolePaymentProcessingFees)
	customer, _, err := s.UpsertCustomer(ctx, Customer{Name: "No Cash Co"})
	if err != nil {
		t.Fatal(err)
	}
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-NO-CASH", 5000)
	if _, _, err := s.IssueInvoice(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	_, err = s.InvoiceCollectReadiness(ctx, draft.ID)
	if err == nil {
		t.Fatal("expected collect readiness to fail without cash role")
	}
	if !strings.Contains(err.Error(), "operating_cash") {
		t.Fatalf("expected cash role guidance, got %v", err)
	}
}

func TestSendRecordsSentAndPublicLink(t *testing.T) {
	s, ctx := newTestStore(t)
	_, revenue, customer := seedInvoiceBook(t, s, ctx)
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-SEND-1", 2500)
	if _, _, err := s.IssueInvoice(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	var sent OutboundEmail
	result, _, err := s.SendInvoice(ctx, draft.ID, InvoiceSendRequest{To: "ap@acme.test"}, InvoicePublicOptions{
		Tenant:        "tenant-a",
		PublicBaseURL: "https://invoices.test",
		SendMail:      func(msg OutboundEmail) error { sent = msg; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Invoice.Status != InvoiceStatusSent || result.To != "ap@acme.test" || result.PublicURL == "" {
		t.Fatalf("send result: %#v", result)
	}
	if sent.To != "ap@acme.test" || !strings.HasPrefix(string(sent.PDF), "%PDF") {
		t.Fatalf("outbound mail: %#v", sent)
	}
}

func TestUnknownPublicTokenIsNotFound(t *testing.T) {
	s, _ := newTestStore(t)
	_, err := s.LookupPublicInvoice("tenant-a", "deadbeef")
	if !PublicInvoiceNotFound(err) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestPublicInvoiceHTTPHandler(t *testing.T) {
	s, ctx := newTestStore(t)
	_, revenue, customer := seedInvoiceBook(t, s, ctx)
	draft := createDraftInvoice(t, s, ctx, customer, revenue, "INV-HTTP-1", 1200)
	if _, _, err := s.IssueInvoice(ctx, draft.ID); err != nil {
		t.Fatal(err)
	}
	_, token, err := s.PublicLink(ctx, draft.ID, InvoicePublicOptions{Tenant: "acme"}, false)
	if err != nil {
		t.Fatal(err)
	}
	mux := testPublicMux(s, "acme")
	req := httptest.NewRequest(http.MethodGet, "/i/acme/"+token, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "INV-HTTP-1") {
		t.Fatalf("public page: %d %s", rec.Code, rec.Body.String())
	}
	wrong := httptest.NewRecorder()
	mux.ServeHTTP(wrong, httptest.NewRequest(http.MethodGet, "/i/other/"+token, nil))
	if wrong.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for other tenant slug, got %d", wrong.Code)
	}
	pdfRec := httptest.NewRecorder()
	mux.ServeHTTP(pdfRec, httptest.NewRequest(http.MethodGet, "/i/acme/"+token+".pdf", nil))
	if pdfRec.Code != http.StatusOK || !strings.HasPrefix(pdfRec.Body.String(), "%PDF") {
		t.Fatalf("pdf: %d", pdfRec.Code)
	}
}

func testPublicMux(store *Store, tenant string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/i/", func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/i/")
		wantPDF := strings.HasSuffix(path, ".pdf")
		path = strings.TrimSuffix(path, ".pdf")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] != tenant {
			http.NotFound(w, r)
			return
		}
		view, err := store.LookupPublicInvoice(parts[0], parts[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if wantPDF {
			pdf, _ := RenderInvoicePDF(view)
			w.Header().Set("Content-Type", "application/pdf")
			_, _ = w.Write(pdf)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(RenderPublicInvoiceHTML(view, "")))
	})
	return mux
}

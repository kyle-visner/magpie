package rail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"magpie/internal/magpie"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestCollectFailsClosedWithoutCashRole(t *testing.T) {
	store, ctx := openBook(t)
	revenue, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Revenue", Type: magpie.AccountRevenue, Role: magpie.AccountRoleDefaultServiceRevenue, Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Fees", Type: magpie.AccountExpense, Role: magpie.AccountRolePaymentProcessingFees, Sensitivity: "confidential",
	}); err != nil {
		t.Fatal(err)
	}
	customer, _, err := store.UpsertCustomer(ctx, magpie.Customer{Name: "Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	invoice, _, err := store.CreateInvoice(ctx, magpie.Invoice{
		InvoiceNumber: "INV-RAIL-1",
		CustomerID:    customer.ID,
		InvoiceDate:   "2026-08-27",
		Status:        magpie.InvoiceStatusDraft,
		LineItems: []magpie.InvoiceLineItem{{
			Description: "Work", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 1000, AmountCents: 1000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.IssueInvoice(ctx, invoice.ID); err != nil {
		t.Fatal(err)
	}
	_, err = Collect(store, ctx, Config{StripeSecretKey: "sk_test", StripeAccount: DefaultConnectedAccount}, nil, CollectRequest{InvoiceID: invoice.ID, Method: CollectCheckout})
	if err == nil || !strings.Contains(err.Error(), "operating_cash") {
		t.Fatalf("expected cash role failure, got %v", err)
	}
}

func TestWebhookMarksPaidOnceAndReplayIsNoop(t *testing.T) {
	store, ctx := openBook(t)
	cash, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Checking", Type: magpie.AccountAsset, Role: magpie.AccountRoleOperatingCash, Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatal(err)
	}
	revenue, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Revenue", Type: magpie.AccountRevenue, Role: magpie.AccountRoleDefaultServiceRevenue, Sensitivity: "confidential",
	})
	if err != nil {
		t.Fatal(err)
	}
	customer, _, err := store.UpsertCustomer(ctx, magpie.Customer{Name: "Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	invoice, _, err := store.CreateInvoice(ctx, magpie.Invoice{
		InvoiceNumber: "INV-RAIL-PAY",
		CustomerID:    customer.ID,
		InvoiceDate:   "2026-08-27",
		Status:        magpie.InvoiceStatusDraft,
		LineItems: []magpie.InvoiceLineItem{{
			Description: "Work", RevenueAccountID: revenue.ID, Quantity: 1, UnitAmountCents: 100000, AmountCents: 100000,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.IssueInvoice(ctx, invoice.ID); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"evt_1","type":"checkout.session.completed","data":{"object":{"id":"cs_1","payment_intent":"pi_1","amount_total":100000,"client_reference_id":"` + invoice.ID + `"}}}`)
	cfg := Config{InboxDir: t.TempDir()}
	first, err := HandleWebhook(store, cfg, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Handled {
		t.Fatalf("expected handled webhook: %#v", first)
	}
	second, err := HandleWebhook(store, cfg, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !second.Handled {
		t.Fatalf("replay should still report handled: %#v", second)
	}
	got, err := store.GetInvoice(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != magpie.SourceDocumentPaid || len(got.Payments) != 1 {
		t.Fatalf("expected one payment, got %#v", got)
	}
	if got.Payments[0].CashAccountID != cash.ID {
		t.Fatalf("expected tenant cash %s, got %#v", cash.ID, got.Payments[0])
	}
}

func TestCollectCreatesCheckoutOnConnectedAccount(t *testing.T) {
	store, ctx := openReadyBook(t)
	invoice := mustIssuedInvoice(t, store, ctx, 2500)
	var sawAccount, sawInvoiceAPI bool
	client := &StripeClient{
		Key:     "sk_test_123",
		Account: DefaultConnectedAccount,
		BaseURL: "https://api.stripe.com",
		HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if strings.Contains(req.URL.Path, "/v1/invoices") {
				sawInvoiceAPI = true
			}
			sawAccount = req.Header.Get("Stripe-Account") == DefaultConnectedAccount
			return jsonResponse(200, `{"id":"cs_test","url":"https://checkout.stripe.com/c/cs_test"}`)
		})},
	}
	result, err := Collect(store, ctx, Config{StripeSecretKey: "sk_test_123", StripeAccount: DefaultConnectedAccount}, client, CollectRequest{InvoiceID: invoice.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.CheckoutURL == "" || result.StripeAccount != DefaultConnectedAccount || !sawAccount || sawInvoiceAPI {
		t.Fatalf("checkout result %#v account=%v invoicesAPI=%v", result, sawAccount, sawInvoiceAPI)
	}
}

func TestPayoutImportFromWebhook(t *testing.T) {
	store, ctx := openReadyBook(t)
	if _, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Stripe Clearing", Type: magpie.AccountAsset, Role: magpie.AccountRoleProcessorClearing, Sensitivity: "confidential",
	}); err != nil {
		t.Fatal(err)
	}
	payload := []byte(`{"id":"evt_po","type":"payout.paid","data":{"object":{"id":"po_1","amount":97000,"arrival_date":1756310400}}}`)
	result, err := HandleWebhook(store, Config{InboxDir: t.TempDir()}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Handled || result.Kind != "payout_import" {
		t.Fatalf("payout webhook: %#v", result)
	}
	st, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Payouts) != 1 {
		t.Fatalf("expected one payout, got %#v", st.Payouts)
	}
}

func TestVerifyStripeSignature(t *testing.T) {
	secret := "whsec_test"
	payload := []byte(`{"id":"evt_sig"}`)
	now := time.Unix(1756310400, 0)
	header := sign(secret, payload, now)
	if err := VerifyStripeSignature(payload, header, secret, now); err != nil {
		t.Fatal(err)
	}
	if err := VerifyStripeSignature(payload, header, "wrong", now); err == nil {
		t.Fatal("expected mismatch")
	}
}

func openBook(t *testing.T) (*magpie.Store, magpie.Context) {
	t.Helper()
	store, err := magpie.OpenStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := magpie.Context{Actor: "owner"}
	if _, err := store.WriteInitialRoot(ctx); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func openReadyBook(t *testing.T) (*magpie.Store, magpie.Context) {
	t.Helper()
	store, ctx := openBook(t)
	if _, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Checking", Type: magpie.AccountAsset, Role: magpie.AccountRoleOperatingCash, Sensitivity: "confidential",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Revenue", Type: magpie.AccountRevenue, Role: magpie.AccountRoleDefaultServiceRevenue, Sensitivity: "confidential",
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateAccountWithDetails(ctx, magpie.Account{
		Name: "Fees", Type: magpie.AccountExpense, Role: magpie.AccountRolePaymentProcessingFees, Sensitivity: "confidential",
	}); err != nil {
		t.Fatal(err)
	}
	return store, ctx
}

func mustIssuedInvoice(t *testing.T, store *magpie.Store, ctx magpie.Context, amount int64) magpie.Invoice {
	t.Helper()
	st, err := store.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	var revenueID, customerID string
	for _, account := range st.Accounts {
		if account.Role == magpie.AccountRoleDefaultServiceRevenue {
			revenueID = account.ID
		}
	}
	customer, _, err := store.UpsertCustomer(ctx, magpie.Customer{Name: "Ready Buyer"})
	if err != nil {
		t.Fatal(err)
	}
	customerID = customer.ID
	invoice, _, err := store.CreateInvoice(ctx, magpie.Invoice{
		InvoiceNumber: "INV-READY",
		CustomerID:    customerID,
		InvoiceDate:   "2026-08-27",
		Status:        magpie.InvoiceStatusDraft,
		LineItems: []magpie.InvoiceLineItem{{
			Description: "Work", RevenueAccountID: revenueID, Quantity: 1, UnitAmountCents: amount, AmountCents: amount,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	issued, _, err := store.IssueInvoice(ctx, invoice.ID)
	if err != nil {
		t.Fatal(err)
	}
	return issued
}

func jsonResponse(code int, body string) (*http.Response, error) {
	return &http.Response{
		StatusCode: code,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func sign(secret string, payload []byte, ts time.Time) string {
	mac := hmac.New(sha256.New, []byte(secret))
	msg := fmt.Sprintf("%d.", ts.Unix())
	_, _ = mac.Write([]byte(msg))
	_, _ = mac.Write(payload)
	return fmt.Sprintf("t=%d,v1=%s", ts.Unix(), hex.EncodeToString(mac.Sum(nil)))
}

func TestUnmatchedEventLandsInInbox(t *testing.T) {
	cfg := Config{InboxDir: t.TempDir()}
	store, _ := openBook(t)
	result, err := HandleWebhook(store, cfg, []byte(`{"id":"evt_unk","type":"customer.created","data":{"object":{}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Inbox {
		t.Fatalf("expected inbox: %#v", result)
	}
	items, err := ListInbox(cfg, "")
	if err != nil || len(items) != 1 {
		t.Fatalf("inbox: %v %#v", err, items)
	}
}

package rail

import (
	"fmt"
	"strings"

	"magpie/internal/magpie"
)

type CollectMethod string

const (
	CollectCheckout    CollectMethod = "checkout"
	CollectPaymentLink CollectMethod = "payment_link"
	CollectManual      CollectMethod = "manual"
)

type CollectRequest struct {
	InvoiceID string
	Method    CollectMethod
}

type CollectResult struct {
	InvoiceID    string `json:"invoice_id"`
	Method       string `json:"method"`
	CheckoutURL  string `json:"checkout_url,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	StripeAccount string `json:"stripe_account,omitempty"`
	Manual       bool   `json:"manual,omitempty"`
}

func Collect(store *magpie.Store, ctx magpie.Context, cfg Config, client *StripeClient, req CollectRequest) (CollectResult, error) {
	req.InvoiceID = strings.TrimSpace(req.InvoiceID)
	if req.InvoiceID == "" {
		return CollectResult{}, fmt.Errorf("invoice id is required")
	}
	if req.Method == "" {
		req.Method = CollectCheckout
	}
	readiness, err := store.InvoiceCollectReadiness(ctx, req.InvoiceID)
	if err != nil {
		return CollectResult{}, err
	}
	if !readiness.Ready {
		return CollectResult{}, fmt.Errorf("rail collect is not ready: %s", strings.Join(readiness.Missing, "; "))
	}
	invoice, err := store.GetInvoice(ctx, req.InvoiceID)
	if err != nil {
		return CollectResult{}, err
	}
	if req.Method == CollectManual {
		return CollectResult{InvoiceID: invoice.ID, Method: string(CollectManual), Manual: true}, nil
	}
	ok, reason := cfg.connectionReady()
	if !ok {
		return CollectResult{}, fmt.Errorf("%s", reason)
	}
	if client == nil {
		client = &StripeClient{Key: cfg.StripeSecretKey, Account: cfg.StripeAccount, BaseURL: cfg.APIBaseURL}
	}
	success := cfg.SuccessURL
	if success == "" {
		success = "https://example.test/success"
	}
	cancel := cfg.CancelURL
	if cancel == "" {
		cancel = "https://example.test/cancel"
	}
	currency := invoice.Currency
	if currency == "" {
		currency = "USD"
	}
	session, err := client.CreateCheckoutSession(invoice.AmountDueCents, currency, invoice.ID, invoice.InvoiceNumber, success, cancel)
	if err != nil {
		return CollectResult{}, err
	}
	return CollectResult{
		InvoiceID:     invoice.ID,
		Method:        string(req.Method),
		CheckoutURL:   session.URL,
		SessionID:     session.ID,
		StripeAccount: cfg.StripeAccount,
	}, nil
}

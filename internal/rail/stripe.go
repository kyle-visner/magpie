package rail

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type StripeClient struct {
	Key     string
	Account string
	BaseURL string
	HTTP    *http.Client
}

type CheckoutSession struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

func (c *StripeClient) CreateCheckoutSession(amountCents int64, currency, invoiceID, invoiceNumber, successURL, cancelURL string) (CheckoutSession, error) {
	if c == nil || strings.TrimSpace(c.Key) == "" {
		return CheckoutSession{}, fmt.Errorf("stripe secret key is required")
	}
	if strings.HasPrefix(strings.ToLower(c.Key), "invoice") {
		return CheckoutSession{}, fmt.Errorf("rail must not use the Stripe Invoicing product")
	}
	form := url.Values{}
	form.Set("mode", "payment")
	form.Set("success_url", successURL)
	form.Set("cancel_url", cancelURL)
	form.Set("client_reference_id", invoiceID)
	form.Set("metadata[invoice_id]", invoiceID)
	form.Set("metadata[invoice_number]", invoiceNumber)
	form.Set("line_items[0][quantity]", "1")
	form.Set("line_items[0][price_data][currency]", strings.ToLower(currency))
	form.Set("line_items[0][price_data][unit_amount]", fmt.Sprintf("%d", amountCents))
	form.Set("line_items[0][price_data][product_data][name]", invoiceNumber)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/v1/checkout/sessions", strings.NewReader(form.Encode()))
	if err != nil {
		return CheckoutSession{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Key)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Stripe-Account", c.Account)
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return CheckoutSession{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CheckoutSession{}, err
	}
	if resp.StatusCode >= 300 {
		return CheckoutSession{}, fmt.Errorf("stripe checkout session failed: %s", strings.TrimSpace(string(body)))
	}
	var session CheckoutSession
	if err := json.Unmarshal(body, &session); err != nil {
		return CheckoutSession{}, err
	}
	if session.URL == "" {
		return CheckoutSession{}, fmt.Errorf("stripe checkout session missing url")
	}
	return session, nil
}

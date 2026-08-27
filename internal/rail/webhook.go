package rail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"magpie/internal/magpie"
)

type StripeEvent struct {
	ID   string          `json:"id"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type stripeEventData struct {
	Object json.RawMessage `json:"object"`
}

type checkoutObject struct {
	ID                 string            `json:"id"`
	PaymentIntent      string            `json:"payment_intent"`
	AmountTotal        int64             `json:"amount_total"`
	Currency           string            `json:"currency"`
	ClientReferenceID  string            `json:"client_reference_id"`
	Metadata           map[string]string `json:"metadata"`
}

type payoutObject struct {
	ID                   string `json:"id"`
	Amount               int64  `json:"amount"`
	ArrivalDate          int64  `json:"arrival_date"`
	Currency             string `json:"currency"`
	BalanceTransaction   string `json:"balance_transaction"`
}

type HandleResult struct {
	Handled bool   `json:"handled"`
	Kind    string `json:"kind,omitempty"`
	ID      string `json:"id,omitempty"`
	Inbox   bool   `json:"inbox,omitempty"`
}

func VerifyStripeSignature(payload []byte, header, secret string, now time.Time) error {
	if strings.TrimSpace(secret) == "" {
		return fmt.Errorf("webhook signature verify required: RAIL_STRIPE_WEBHOOK_SECRET is empty")
	}
	var timestamp string
	var signatures []string
	for _, part := range strings.Split(header, ",") {
		key, val, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch key {
		case "t":
			timestamp = val
		case "v1":
			signatures = append(signatures, val)
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return fmt.Errorf("invalid Stripe-Signature header")
	}
	ts, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("invalid Stripe-Signature timestamp")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if now.Unix()-ts > 300 || ts-now.Unix() > 300 {
		return fmt.Errorf("Stripe-Signature timestamp is outside tolerance")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(timestamp))
	_, _ = mac.Write([]byte("."))
	_, _ = mac.Write(payload)
	expected := hex.EncodeToString(mac.Sum(nil))
	for _, sig := range signatures {
		if hmac.Equal([]byte(sig), []byte(expected)) {
			return nil
		}
	}
	return fmt.Errorf("Stripe-Signature mismatch")
}

func HandleWebhook(store *magpie.Store, cfg Config, payload []byte) (HandleResult, error) {
	var event StripeEvent
	if err := json.Unmarshal(payload, &event); err != nil {
		return HandleResult{}, fmt.Errorf("invalid stripe event: %w", err)
	}
	ctx := magpie.Context{Actor: "rail-webhook"}
	switch event.Type {
	case "checkout.session.completed", "payment_intent.succeeded":
		return handlePaymentEvent(store, ctx, event)
	case "payout.paid":
		return handlePayoutEvent(store, ctx, cfg, event)
	default:
		if err := RecordInbox(cfg, InboxItem{ID: event.ID, EventType: event.Type, Reason: "unmatched event type", Payload: payload}); err != nil {
			return HandleResult{}, err
		}
		return HandleResult{Handled: false, Inbox: true, ID: event.ID, Kind: event.Type}, nil
	}
}

func handlePaymentEvent(store *magpie.Store, ctx magpie.Context, event StripeEvent) (HandleResult, error) {
	var envelope stripeEventData
	if err := json.Unmarshal(event.Data, &envelope); err != nil {
		return HandleResult{}, err
	}
	var session checkoutObject
	if err := json.Unmarshal(envelope.Object, &session); err != nil {
		return HandleResult{}, err
	}
	invoiceID := strings.TrimSpace(session.ClientReferenceID)
	if invoiceID == "" && session.Metadata != nil {
		invoiceID = strings.TrimSpace(session.Metadata["invoice_id"])
	}
	if invoiceID == "" {
		return HandleResult{}, fmt.Errorf("stripe event %s has no invoice_id", event.ID)
	}
	st, err := store.LoadState()
	if err != nil {
		return HandleResult{}, err
	}
	cashID, err := magpie.CashAccountIDForCollect(st)
	if err != nil {
		return HandleResult{}, err
	}
	invoice, err := store.GetInvoice(ctx, invoiceID)
	if err != nil {
		return HandleResult{}, err
	}
	amount := session.AmountTotal
	if amount <= 0 {
		amount = invoice.AmountDueCents
	}
	refs := []magpie.ExternalSourceRef{{
		SourceSystem: "stripe",
		ExternalID:   event.ID,
		ExternalType: "event",
	}}
	if session.ID != "" {
		refs = append(refs, magpie.ExternalSourceRef{SourceSystem: "stripe", ExternalID: session.ID, ExternalType: "checkout_session"})
	}
	if session.PaymentIntent != "" {
		refs = append(refs, magpie.ExternalSourceRef{SourceSystem: "stripe", ExternalID: session.PaymentIntent, ExternalType: "payment_intent"})
	}
	if _, _, err := store.MarkInvoicePaid(ctx, invoiceID, magpie.InvoicePaymentRequest{
		Date:         time.Now().UTC().Format("2006-01-02"),
		AmountCents:  amount,
		CashAccountID: cashID,
		ExternalRefs: refs,
	}); err != nil {
		return HandleResult{}, err
	}
	return HandleResult{Handled: true, Kind: "invoice_mark_paid", ID: event.ID}, nil
}

func handlePayoutEvent(store *magpie.Store, ctx magpie.Context, cfg Config, event StripeEvent) (HandleResult, error) {
	var envelope stripeEventData
	if err := json.Unmarshal(event.Data, &envelope); err != nil {
		return HandleResult{}, err
	}
	var payout payoutObject
	if err := json.Unmarshal(envelope.Object, &payout); err != nil {
		return HandleResult{}, err
	}
	st, err := store.LoadState()
	if err != nil {
		return HandleResult{}, err
	}
	dest, err := magpie.CashAccountIDForCollect(st)
	if err != nil {
		return HandleResult{}, err
	}
	source := ""
	for _, account := range st.Accounts {
		if account.Role == magpie.AccountRoleProcessorClearing || account.Role == magpie.AccountRoleUndepositedFunds {
			source = account.ID
			break
		}
	}
	if source == "" {
		if err := RecordInbox(cfg, InboxItem{ID: event.ID, EventType: event.Type, Reason: "payout.paid missing processor_clearing account", Payload: event.Data}); err != nil {
			return HandleResult{}, err
		}
		return HandleResult{Handled: false, Inbox: true, ID: event.ID, Kind: event.Type}, nil
	}
	date := time.Now().UTC().Format("2006-01-02")
	if payout.ArrivalDate > 0 {
		date = time.Unix(payout.ArrivalDate, 0).UTC().Format("2006-01-02")
	}
	feeAccount := ""
	for _, account := range st.Accounts {
		if account.Role == magpie.AccountRolePaymentProcessingFees || account.Role == magpie.AccountRoleMerchantFeesExpense {
			feeAccount = account.ID
			break
		}
	}
	imported, _, err := store.ImportPayout(ctx, magpie.Payout{
		Date:                 date,
		Description:          "Stripe payout",
		SourceAccountID:      source,
		DestinationAccountID: dest,
		NetAmountCents:       payout.Amount,
		FeeExpenseAccountID:  feeAccount,
		ExternalRefs: []magpie.ExternalSourceRef{{
			SourceSystem: "stripe",
			ExternalID:   payout.ID,
			ExternalType: "payout",
		}, {
			SourceSystem: "stripe",
			ExternalID:   event.ID,
			ExternalType: "event",
		}},
	})
	if err != nil {
		return HandleResult{}, err
	}
	return HandleResult{Handled: true, Kind: "payout_import", ID: imported.ID}, nil
}

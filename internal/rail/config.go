package rail

import (
	"os"
	"strings"
)

const DefaultConnectedAccount = "acct_test_connected"

type Config struct {
	StoreDir           string
	Tenant             string
	StripeSecretKey    string
	StripeWebhookSecret string
	StripeAccount      string
	NangoConnectionID  string
	PublicBaseURL      string
	SuccessURL         string
	CancelURL          string
	InboxDir           string
	APIBaseURL         string
}

func ConfigFromEnv() Config {
	account := strings.TrimSpace(os.Getenv("RAIL_STRIPE_ACCOUNT"))
	if account == "" {
		account = DefaultConnectedAccount
	}
	api := strings.TrimSpace(os.Getenv("RAIL_STRIPE_API_BASE"))
	if api == "" {
		api = "https://api.stripe.com"
	}
	return Config{
		StoreDir:            strings.TrimSpace(os.Getenv("MAGPIE_STORE")),
		Tenant:              strings.TrimSpace(os.Getenv("RAIL_TENANT")),
		StripeSecretKey:     strings.TrimSpace(os.Getenv("RAIL_STRIPE_SECRET_KEY")),
		StripeWebhookSecret: strings.TrimSpace(os.Getenv("RAIL_STRIPE_WEBHOOK_SECRET")),
		StripeAccount:       account,
		NangoConnectionID:   strings.TrimSpace(os.Getenv("RAIL_NANGO_CONNECTION_ID")),
		PublicBaseURL:       strings.TrimSpace(os.Getenv("RAIL_PUBLIC_BASE_URL")),
		SuccessURL:          strings.TrimSpace(os.Getenv("RAIL_SUCCESS_URL")),
		CancelURL:           strings.TrimSpace(os.Getenv("RAIL_CANCEL_URL")),
		InboxDir:            strings.TrimSpace(os.Getenv("RAIL_INBOX_DIR")),
		APIBaseURL:          api,
	}
}

func (c Config) connectionReady() (bool, string) {
	if c.StripeSecretKey == "" && c.NangoConnectionID == "" {
		return false, "missing Stripe connection: set RAIL_STRIPE_SECRET_KEY or RAIL_NANGO_CONNECTION_ID for this volume"
	}
	if c.StripeAccount == "" {
		return false, "missing connected account: set RAIL_STRIPE_ACCOUNT"
	}
	return true, ""
}

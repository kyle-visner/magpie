package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"magpie/internal/magpie"
	"magpie/internal/rail"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"code": "error", "message": err.Error()})
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	global := flag.NewFlagSet("rail", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	storeDir := global.String("store", envOr("MAGPIE_STORE", ".magpie"), "Magpie store directory")
	actor := global.String("actor", "owner", "Magpie actor")
	if err := global.Parse(args); err != nil {
		return err
	}
	rest := global.Args()
	if len(rest) == 0 || rest[0] == "help" {
		return usage(out)
	}
	cfg := rail.ConfigFromEnv()
	cfg.StoreDir = *storeDir
	store, err := magpie.OpenStore(*storeDir)
	if err != nil {
		return err
	}
	defer store.Close()
	ctx := magpie.Context{Actor: *actor}
	switch rest[0] {
	case "collect":
		fs := flag.NewFlagSet("rail collect", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		invoiceID := fs.String("invoice-id", "", "Magpie invoice id")
		method := fs.String("method", "checkout", "checkout|payment_link|manual")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		result, err := rail.Collect(store, ctx, cfg, nil, rail.CollectRequest{InvoiceID: *invoiceID, Method: rail.CollectMethod(*method)})
		if err != nil {
			return err
		}
		return writeJSON(out, result)
	case "inbox":
		items, err := rail.ListInbox(cfg, "")
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"items": items})
	case "sync":
		fs := flag.NewFlagSet("rail sync", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		since := fs.String("since", "", "RFC3339 or YYYY-MM-DD")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		items, err := rail.ListInbox(cfg, *since)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"since": *since, "unmatched": items})
	case "serve":
		fs := flag.NewFlagSet("rail serve", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		listen := fs.String("listen", "127.0.0.1:8090", "listen address")
		if err := fs.Parse(rest[1:]); err != nil {
			return err
		}
		return http.ListenAndServe(*listen, railMux(store, ctx, cfg))
	default:
		return fmt.Errorf("unknown rail command %q", rest[0])
	}
}

func railMux(store *magpie.Store, collectActor magpie.Context, cfg rail.Config) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		if err := rail.VerifyStripeSignature(body, r.Header.Get("Stripe-Signature"), cfg.StripeWebhookSecret, time.Now().UTC()); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		result, err := rail.HandleWebhook(store, cfg, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("/checkout/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/checkout/"), "/")
		if len(parts) != 2 {
			http.NotFound(w, r)
			return
		}
		view, err := store.LookupPublicInvoice(parts[0], parts[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if view.PayDisabled {
			http.Error(w, "payment disabled", http.StatusConflict)
			return
		}
		result, err := rail.Collect(store, collectActor, cfg, nil, rail.CollectRequest{InvoiceID: view.InvoiceID, Method: rail.CollectCheckout})
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Redirect(w, r, result.CheckoutURL, http.StatusFound)
	})
	return mux
}

func usage(out io.Writer) error {
	_, err := fmt.Fprintln(out, `rail — tenant payment adapter (never imported by Magpie)

Commands:
  collect --invoice-id ID --method checkout|payment_link|manual
  inbox
  sync --since DATE
  serve --listen ADDR

Secrets stay in rail env / Nango. Direct charges on the tenant Stripe account only.`)
	return err
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

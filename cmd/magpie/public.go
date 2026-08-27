package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"magpie/internal/magpie"
)

func servePublicInvoices(store *magpie.Store, listen, tenant, payBase string, out io.Writer) error {
	mux := publicInvoiceMux(store, tenant, payBase)
	if _, err := fmt.Fprintf(out, "{\n  \"listen\": %q,\n  \"tenant\": %q\n}\n", listen, tenant); err != nil {
		return err
	}
	return http.ListenAndServe(listen, mux)
}

func publicInvoiceMux(store *magpie.Store, tenant, payBase string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/i/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/i/")
		wantPDF := strings.HasSuffix(path, ".pdf")
		path = strings.TrimSuffix(path, ".pdf")
		parts := strings.Split(path, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, r)
			return
		}
		if parts[0] != tenant {
			http.NotFound(w, r)
			return
		}
		view, err := store.LookupPublicInvoice(parts[0], parts[1])
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if wantPDF {
			pdf, err := magpie.RenderInvoicePDF(view)
			if err != nil {
				http.Error(w, "pdf render failed", http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename="`+view.InvoiceNumber+`.pdf"`)
			_, _ = w.Write(pdf)
			return
		}
		payURL := ""
		if strings.TrimSpace(payBase) != "" && !view.PayDisabled {
			payURL = strings.TrimRight(payBase, "/") + "/checkout/" + parts[0] + "/" + parts[1]
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, magpie.RenderPublicInvoiceHTML(view, payURL))
	})
	return mux
}

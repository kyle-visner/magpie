package magpie

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"strings"
)

type InvoicePublicOptions struct {
	Tenant        string
	PublicBaseURL string
	FromAddress   string
	SendMail      func(OutboundEmail) error
}

type PublicInvoiceView struct {
	Tenant              string                     `json:"tenant"`
	InvoiceID           string                     `json:"invoice_id"`
	InvoiceNumber       string                     `json:"invoice_number"`
	Status              SourceDocumentStatus       `json:"status"`
	Kind                string                     `json:"kind"`
	InvoiceDate         string                     `json:"invoice_date"`
	DueDate             string                     `json:"due_date,omitempty"`
	Terms               string                     `json:"terms,omitempty"`
	Currency            string                     `json:"currency"`
	Seller              InvoiceBranding            `json:"seller"`
	BuyerName           string                     `json:"buyer_name"`
	BuyerEmail          string                     `json:"buyer_email,omitempty"`
	BuyerAddress        string                     `json:"buyer_address,omitempty"`
	LineItems           []InvoiceLineItem          `json:"line_items"`
	SubtotalCents       int64                      `json:"subtotal_cents"`
	TaxAmountCents      int64                      `json:"tax_amount_cents"`
	RetainageCents      int64                      `json:"retainage_cents,omitempty"`
	TotalCents          int64                      `json:"total_cents"`
	AmountPaidCents     int64                      `json:"amount_paid_cents"`
	AmountDueCents      int64                      `json:"amount_due_cents"`
	Memo                string                     `json:"memo,omitempty"`
	PONumber            string                     `json:"po_number,omitempty"`
	PaymentInstructions InvoicePaymentInstructions `json:"payment_instructions"`
	PayDisabled         bool                       `json:"pay_disabled"`
	Void                bool                       `json:"void"`
	Paid                bool                       `json:"paid"`
}

type CollectReadiness struct {
	InvoiceID string   `json:"invoice_id"`
	Ready     bool     `json:"ready"`
	Missing   []string `json:"missing,omitempty"`
}

func (s *Store) PublicLink(ctx Context, invoiceID string, opts InvoicePublicOptions, rotate bool) (InvoicePublicLink, string, error) {
	return s.ensurePublicLink(ctx, invoiceID, opts, rotate)
}

func (s *Store) ensurePublicLink(ctx Context, invoiceID string, opts InvoicePublicOptions, rotate bool) (InvoicePublicLink, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return InvoicePublicLink{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionInvoiceIssue); err != nil {
		if err2 := EnsurePermission(st, ctx, PermissionInvoiceSend); err2 != nil {
			return InvoicePublicLink{}, "", err
		}
	}
	invoiceID = strings.TrimSpace(invoiceID)
	invoice, ok := st.Invoices[invoiceID]
	if !ok {
		return InvoicePublicLink{}, "", appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	if invoiceIsDraft(invoice.Status) {
		return InvoicePublicLink{}, "", appErr(ErrValidation, "draft invoice has no public link; issue it first")
	}
	alreadyHad := invoice.PublicTokenHash != ""
	token, hash, err := newPublicToken()
	if err != nil {
		return InvoicePublicLink{}, "", err
	}
	invoice.PublicTokenHash = hash
	invoice.UpdatedAt = s.now().UTC()
	invoice.UpdatedBy = ctx.Actor
	if _, err := s.appendEventAt(ctx, "invoice", invoice.ID, "invoice public-link", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), st.Root); err != nil {
		return InvoicePublicLink{}, "", err
	}
	raw := token
	rotate = alreadyHad || rotate
	link := InvoicePublicLink{
		InvoiceID: invoice.ID,
		Tenant:    strings.TrimSpace(opts.Tenant),
		Token:     raw,
		URL:       publicInvoiceURL(opts, raw),
		Rotated:   rotate,
	}
	return link, raw, nil
}

func (s *Store) LookupPublicInvoice(tenant, token string) (PublicInvoiceView, error) {
	st, err := s.LoadState()
	if err != nil {
		return PublicInvoiceView{}, err
	}
	token = strings.TrimSpace(token)
	tenant = strings.TrimSpace(tenant)
	if token == "" {
		return PublicInvoiceView{}, appErr(ErrNotFound, "invoice not found")
	}
	hash := HashPublicToken(token)
	var found Invoice
	var ok bool
	for _, invoice := range st.Invoices {
		if invoice.PublicTokenHash != "" && invoice.PublicTokenHash == hash {
			found = invoice
			ok = true
			break
		}
	}
	if !ok {
		return PublicInvoiceView{}, appErr(ErrNotFound, "invoice not found")
	}
	return s.publicViewFromInvoice(st, found, tenant, token)
}

func (s *Store) publicViewFromInvoice(st State, invoice Invoice, tenant, token string) (PublicInvoiceView, error) {
	invoice = enrichInvoice(invoice, s.now())
	source := invoice
	if invoice.IssuedSnapshot != nil {
		snap := invoice.IssuedSnapshot
		source.InvoiceNumber = snap.InvoiceNumber
		source.CustomerID = snap.CustomerID
		source.InvoiceDate = snap.InvoiceDate
		source.DueDate = snap.DueDate
		source.Terms = snap.Terms
		source.Currency = snap.Currency
		source.LineItems = snap.LineItems
		source.SubtotalCents = snap.SubtotalCents
		source.TaxAmountCents = snap.TaxAmountCents
		source.RetainageCents = snap.RetainageCents
		source.TotalCents = snap.TotalCents
		source.Memo = snap.Memo
		source.PONumber = snap.PONumber
		source.Branding = snap.Branding
		source.PaymentInstructions = snap.PaymentInstructions
		source.Kind = snap.Kind
	}
	paid, _ := invoicePaidAmount(invoice)
	source.AmountPaidCents = paid
	source.AmountDueCents = source.TotalCents - paid
	if source.AmountDueCents < 0 {
		source.AmountDueCents = 0
	}
	buyerName := source.CustomerID
	buyerEmail := ""
	buyerAddress := ""
	if customer, ok := st.Customers[source.CustomerID]; ok {
		buyerName = customer.Name
		buyerEmail = customer.Email
		buyerAddress = customer.Address
	}
	voided := invoice.Status == SourceDocumentVoid
	paidFull := invoice.Status == SourceDocumentPaid
	return PublicInvoiceView{
		Tenant:              tenant,
		InvoiceID:           invoice.ID,
		InvoiceNumber:       source.InvoiceNumber,
		Status:              invoice.Status,
		Kind:                source.Kind,
		InvoiceDate:         source.InvoiceDate,
		DueDate:             source.DueDate,
		Terms:               source.Terms,
		Currency:            source.Currency,
		Seller:              source.Branding,
		BuyerName:           buyerName,
		BuyerEmail:          buyerEmail,
		BuyerAddress:        buyerAddress,
		LineItems:           source.LineItems,
		SubtotalCents:       source.SubtotalCents,
		TaxAmountCents:      source.TaxAmountCents,
		RetainageCents:      source.RetainageCents,
		TotalCents:          source.TotalCents,
		AmountPaidCents:     source.AmountPaidCents,
		AmountDueCents:      source.AmountDueCents,
		Memo:                source.Memo,
		PONumber:            source.PONumber,
		PaymentInstructions: source.PaymentInstructions,
		PayDisabled:         voided || paidFull || invoice.Status == InvoiceStatusUncollectible,
		Void:                voided,
		Paid:                paidFull,
	}, nil
}

func (s *Store) InvoiceCollectReadiness(ctx Context, invoiceID string) (CollectReadiness, error) {
	st, err := s.LoadState()
	if err != nil {
		return CollectReadiness{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionRailCollect); err != nil {
		return CollectReadiness{}, err
	}
	invoiceID = strings.TrimSpace(invoiceID)
	invoice, ok := st.Invoices[invoiceID]
	if !ok {
		return CollectReadiness{}, appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	result := CollectReadiness{InvoiceID: invoice.ID}
	if invoiceIsDraft(invoice.Status) {
		result.Missing = append(result.Missing, "invoice is still a draft; run invoice issue")
	}
	if invoice.Status == SourceDocumentVoid {
		result.Missing = append(result.Missing, "invoice is void")
	}
	if invoice.Status == SourceDocumentPaid {
		result.Missing = append(result.Missing, "invoice is already paid")
	}
	if _, err := accountIDByRole(st, AccountRoleOperatingCash); err != nil {
		if _, err := accountIDByRole(st, AccountRoleBankAccount); err != nil {
			result.Missing = append(result.Missing, "configure an account with role operating_cash or bank_account")
		}
	}
	if _, err := feeExpenseAccountID(st); err != nil {
		result.Missing = append(result.Missing, "configure an account with role payment_processing_fees or merchant_fees_expense")
	}
	settings := st.effectiveSettings()
	if settings.AccountingBasis == AccountingBasisAccrual {
		if _, err := accountIDByRole(st, AccountRoleAccountsReceivable); err != nil {
			result.Missing = append(result.Missing, "accrual books require an accounts_receivable account")
		}
	}
	if _, err := accountIDByRole(st, AccountRoleDefaultServiceRevenue); err != nil {
		if _, err := accountIDByRole(st, AccountRoleDefaultProductRevenue); err != nil {
			result.Missing = append(result.Missing, "configure a default_service_revenue or default_product_revenue account")
		}
	}
	result.Ready = len(result.Missing) == 0
	if !result.Ready {
		return result, appErr(ErrValidation, "rail collect is not ready: %s", strings.Join(result.Missing, "; "))
	}
	return result, nil
}

func feeExpenseAccountID(st State) (string, error) {
	if id, err := accountIDByRole(st, AccountRolePaymentProcessingFees); err == nil {
		return id, nil
	}
	return accountIDByRole(st, AccountRoleMerchantFeesExpense)
}

func CashAccountIDForCollect(st State) (string, error) {
	if id, err := accountIDByRole(st, AccountRoleOperatingCash); err == nil {
		return id, nil
	}
	return accountIDByRole(st, AccountRoleBankAccount)
}

func publicInvoiceURL(opts InvoicePublicOptions, token string) string {
	base := strings.TrimRight(strings.TrimSpace(opts.PublicBaseURL), "/")
	if base == "" {
		base = "http://127.0.0.1"
	}
	tenant := strings.TrimSpace(opts.Tenant)
	if tenant == "" {
		tenant = "local"
	}
	return base + "/i/" + url.PathEscape(tenant) + "/" + url.PathEscape(token)
}

func RenderPublicInvoiceHTML(view PublicInvoiceView, payURL string) string {
	status := string(view.Status)
	pay := ""
	if view.PayDisabled {
		pay = "<p class=\"muted\">Payment is disabled.</p>"
	} else if strings.TrimSpace(payURL) != "" {
		pay = `<p><a class="pay" href="` + html.EscapeString(payURL) + `">Pay this invoice</a></p>
<p class="muted">Success page is cosmetic. Paid status comes from the payment webhook.</p>`
	}
	voidBanner := ""
	if view.Void {
		voidBanner = `<p class="void">This invoice is void.</p>`
	}
	paidBanner := ""
	if view.Paid {
		paidBanner = `<p class="paid">Paid</p>`
	}
	var lines strings.Builder
	for _, line := range view.LineItems {
		fmt.Fprintf(&lines, "<tr><td>%s</td><td>%d</td><td>%s</td><td>%s</td></tr>",
			html.EscapeString(line.Description),
			line.Quantity,
			formatCents(line.UnitAmountCents, view.Currency),
			formatCents(line.AmountCents, view.Currency),
		)
	}
	instructions := renderPaymentInstructionsHTML(view.PaymentInstructions)
	seller := html.EscapeString(view.Seller.LegalName)
	if seller == "" {
		seller = html.EscapeString(view.Seller.DBA)
	}
	return `<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8"><title>Invoice ` + html.EscapeString(view.InvoiceNumber) + `</title>
<style>
body{font-family:Georgia,serif;max-width:720px;margin:2rem auto;padding:0 1rem;color:#222}
table{width:100%;border-collapse:collapse;margin:1rem 0}
td,th{border-bottom:1px solid #ddd;padding:.4rem;text-align:left}
.pay{display:inline-block;background:#111;color:#fff;padding:.6rem 1rem;text-decoration:none}
.void{color:#a00;font-weight:bold}
.paid{color:#0a0;font-weight:bold}
.muted{color:#666;font-size:.9rem}
</style></head><body>
<h1>Invoice ` + html.EscapeString(view.InvoiceNumber) + `</h1>
` + voidBanner + paidBanner + `
<p>Status: ` + html.EscapeString(status) + `</p>
<p>From: ` + seller + `<br>` + html.EscapeString(view.Seller.Address) + `</p>
<p>Bill to: ` + html.EscapeString(view.BuyerName) + `<br>` + html.EscapeString(view.BuyerAddress) + `</p>
<p>Date ` + html.EscapeString(view.InvoiceDate) + ` · Due ` + html.EscapeString(view.DueDate) + `</p>
<table><thead><tr><th>Description</th><th>Qty</th><th>Unit</th><th>Amount</th></tr></thead><tbody>` + lines.String() + `</tbody></table>
<p>Subtotal ` + formatCents(view.SubtotalCents, view.Currency) + `<br>
Tax ` + formatCents(view.TaxAmountCents, view.Currency) + `<br>
Total ` + formatCents(view.TotalCents, view.Currency) + `<br>
Amount due ` + formatCents(view.AmountDueCents, view.Currency) + `</p>
` + pay + instructions + `
<p class="muted">Future Perfect issues and tracks your invoices. Your customer pays you — card through your Stripe, or ACH/wire to your bank. We never take the payment. Your books update when it clears.</p>
</body></html>`
}

func renderPaymentInstructionsHTML(instr InvoicePaymentInstructions) string {
	var b strings.Builder
	write := func(title string, item *PaymentInstruction) {
		if item == nil {
			return
		}
		b.WriteString("<h2>" + html.EscapeString(title) + "</h2><p>")
		if item.BankName != "" {
			b.WriteString(html.EscapeString(item.BankName) + "<br>")
		}
		if item.AccountName != "" {
			b.WriteString(html.EscapeString(item.AccountName) + "<br>")
		}
		if item.RoutingLast4 != "" {
			b.WriteString("Routing ••••" + html.EscapeString(item.RoutingLast4) + "<br>")
		}
		if item.AccountLast4 != "" {
			b.WriteString("Account ••••" + html.EscapeString(item.AccountLast4) + "<br>")
		}
		if item.Instructions != "" {
			b.WriteString(html.EscapeString(item.Instructions))
		}
		b.WriteString("</p>")
	}
	write("ACH", instr.ACH)
	write("Wire", instr.Wire)
	write("Check", instr.Check)
	return b.String()
}

func formatCents(cents int64, currency string) string {
	if currency == "" {
		currency = "USD"
	}
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	return fmt.Sprintf("%s%s %d.%02d", sign, currency, cents/100, cents%100)
}

func RenderInvoicePDF(view PublicInvoiceView) ([]byte, error) {
	var lines []string
	lines = append(lines, "Invoice "+view.InvoiceNumber)
	if view.Void {
		lines = append(lines, "VOID")
	}
	if view.Paid {
		lines = append(lines, "PAID")
	}
	lines = append(lines, "Status: "+string(view.Status))
	seller := view.Seller.LegalName
	if seller == "" {
		seller = view.Seller.DBA
	}
	lines = append(lines, "From: "+seller)
	if view.Seller.Address != "" {
		lines = append(lines, view.Seller.Address)
	}
	lines = append(lines, "Bill to: "+view.BuyerName)
	if view.BuyerAddress != "" {
		lines = append(lines, view.BuyerAddress)
	}
	lines = append(lines, "Date: "+view.InvoiceDate+"  Due: "+view.DueDate)
	for _, line := range view.LineItems {
		lines = append(lines, fmt.Sprintf("%s  qty %d  %s", line.Description, line.Quantity, formatCents(line.AmountCents, view.Currency)))
	}
	lines = append(lines, "Subtotal "+formatCents(view.SubtotalCents, view.Currency))
	lines = append(lines, "Tax "+formatCents(view.TaxAmountCents, view.Currency))
	lines = append(lines, "Total "+formatCents(view.TotalCents, view.Currency))
	lines = append(lines, "Amount due "+formatCents(view.AmountDueCents, view.Currency))
	lines = append(lines, "Your customer pays you. Future Perfect never takes the payment.")
	return writeSimplePDF(lines), nil
}

func writeSimplePDF(lines []string) []byte {
	var content bytes.Buffer
	content.WriteString("BT /F1 12 Tf 50 750 Td\n")
	for i, line := range lines {
		if i > 0 {
			content.WriteString("0 -16 Td\n")
		}
		content.WriteString("(" + pdfEscape(line) + ") Tj\n")
	}
	content.WriteString("ET\n")
	stream := content.Bytes()
	var buf bytes.Buffer
	write := func(s string) { buf.WriteString(s) }
	write("%PDF-1.4\n")
	offsets := make([]int, 5)
	offsets[1] = buf.Len()
	write("1 0 obj << /Type /Catalog /Pages 2 0 R >> endobj\n")
	offsets[2] = buf.Len()
	write("2 0 obj << /Type /Pages /Kids [3 0 R] /Count 1 >> endobj\n")
	offsets[3] = buf.Len()
	write("3 0 obj << /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >> endobj\n")
	offsets[4] = buf.Len()
	fmt.Fprintf(&buf, "4 0 obj << /Length %d >> stream\n%s\nendstream\nendobj\n", len(stream), stream)
	offsets = append(offsets, buf.Len())
	write("5 0 obj << /Type /Font /Subtype /Type1 /BaseFont /Helvetica >> endobj\n")
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 6\n0000000000 65535 f \n")
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer << /Size 6 /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", xref)
	return buf.Bytes()
}

func pdfEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `(`, `\(`)
	s = strings.ReplaceAll(s, `)`, `\)`)
	return s
}

func PublicInvoiceNotFound(err error) bool {
	if err == nil {
		return false
	}
	app, ok := err.(*AppError)
	return ok && app.Code == ErrNotFound
}

package magpie

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"
)

func invoiceIsDraft(status SourceDocumentStatus) bool {
	return status == SourceDocumentImported || status == InvoiceStatusDraft
}

func invoiceIsIssuedLike(status SourceDocumentStatus) bool {
	switch status {
	case SourceDocumentOpen, InvoiceStatusIssued, InvoiceStatusSent, InvoiceStatusPartiallyPaid, InvoiceStatusOverdue:
		return true
	default:
		return false
	}
}

func validInvoiceStatus(status SourceDocumentStatus) bool {
	switch status {
	case SourceDocumentImported, SourceDocumentOpen, SourceDocumentPaid, SourceDocumentVoid,
		InvoiceStatusDraft, InvoiceStatusIssued, InvoiceStatusSent, InvoiceStatusPartiallyPaid,
		InvoiceStatusOverdue, InvoiceStatusUncollectible:
		return true
	default:
		return false
	}
}

func enrichInvoice(invoice Invoice, now time.Time) Invoice {
	paid, err := invoicePaidAmount(invoice)
	if err != nil {
		return invoice
	}
	invoice.AmountPaidCents = paid
	invoice.AmountDueCents = invoice.TotalCents - paid
	if invoice.AmountDueCents < 0 {
		invoice.AmountDueCents = 0
	}
	if invoice.Currency == "" {
		invoice.Currency = "USD"
	}
	if invoice.Kind == "" {
		invoice.Kind = InvoiceKindInvoice
	}
	if invoice.Status != SourceDocumentPaid && invoice.Status != SourceDocumentVoid && invoice.Status != InvoiceStatusUncollectible {
		if invoiceIsIssuedLike(invoice.Status) && invoice.DueDate != "" && invoice.DueDate < now.UTC().Format("2006-01-02") && paid < invoice.TotalCents {
			invoice.Status = InvoiceStatusOverdue
		}
	}
	return invoice
}

func normalizeInvoicePaymentRequest(req InvoicePaymentRequest) (InvoicePaymentRequest, error) {
	req.Date = strings.TrimSpace(req.Date)
	req.PaidDate = strings.TrimSpace(req.PaidDate)
	if req.Date == "" {
		req.Date = req.PaidDate
	}
	if req.Date != "" {
		if _, err := time.Parse("2006-01-02", req.Date); err != nil {
			return InvoicePaymentRequest{}, appErr(ErrValidation, "payment date must use YYYY-MM-DD")
		}
	}
	req.CashAccountID = strings.TrimSpace(req.CashAccountID)
	req.ExternalSource = strings.TrimSpace(req.ExternalSource)
	req.ExternalID = strings.TrimSpace(req.ExternalID)
	req.PaymentEvidence = strings.TrimSpace(req.PaymentEvidence)
	req.ManualReason = strings.TrimSpace(req.ManualReason)
	refs, err := normalizeExternalRefs(req.ExternalRefs)
	if err != nil {
		return InvoicePaymentRequest{}, err
	}
	if len(refs) == 0 && req.ExternalSource != "" && req.ExternalID != "" {
		refs = []ExternalSourceRef{{
			SourceSystem: strings.ToLower(req.ExternalSource),
			ExternalID:   req.ExternalID,
			ExternalType: "payment",
		}}
	}
	req.ExternalRefs = refs
	return req, nil
}

func paymentHasEvidence(req InvoicePaymentRequest) bool {
	if req.ManualReason != "" {
		return true
	}
	if len(req.ExternalRefs) > 0 {
		return true
	}
	if req.ExternalSource != "" && req.ExternalID != "" {
		return true
	}
	return req.PaymentEvidence != ""
}

func invoicePaymentSourceKey(invoiceID string, req InvoicePaymentRequest) string {
	for _, ref := range req.ExternalRefs {
		if strings.EqualFold(ref.ExternalType, "event") || strings.HasPrefix(ref.ExternalID, "evt_") {
			return "payment:" + strings.ToLower(ref.SourceSystem) + ":event:" + ref.ExternalID
		}
	}
	if len(req.ExternalRefs) > 0 {
		ref := req.ExternalRefs[0]
		return "payment:" + strings.ToLower(ref.SourceSystem) + ":" + ref.ExternalID
	}
	if req.ExternalSource != "" && req.ExternalID != "" {
		return "payment:" + strings.ToLower(req.ExternalSource) + ":" + req.ExternalID
	}
	return invoiceID + ":paid:" + req.Date + ":" + int64String(req.AmountCents)
}

func paymentMatchesEvidence(payment InvoicePayment, req InvoicePaymentRequest) bool {
	for _, ref := range req.ExternalRefs {
		key := externalRefKey(ref)
		if payment.ExternalSource != "" && payment.ExternalID != "" &&
			externalRefKey(ExternalSourceRef{SourceSystem: payment.ExternalSource, ExternalID: payment.ExternalID}) == key {
			return true
		}
		for _, existing := range payment.ExternalRefs {
			if externalRefKey(existing) == key {
				return true
			}
		}
	}
	return false
}

func invoiceIssueJournalPostings(st State, invoice Invoice) ([]Posting, error) {
	ar, err := accountIDByRole(st, AccountRoleAccountsReceivable)
	if err != nil {
		return nil, err
	}
	creditMemo := invoice.Kind == InvoiceKindCreditMemo
	var postings []Posting
	if creditMemo {
		postings = append(postings, Posting{AccountID: ar, Credit: invoice.TotalCents, Memo: "Credit memo issued"})
		for _, line := range invoice.LineItems {
			postings = append(postings, Posting{AccountID: line.RevenueAccountID, Debit: line.AmountCents, Memo: line.Description})
		}
		if invoice.TaxAmountCents > 0 {
			tax, err := accountIDByRole(st, AccountRoleSalesTaxPayable)
			if err != nil {
				return nil, err
			}
			postings = append(postings, Posting{AccountID: tax, Debit: invoice.TaxAmountCents, Memo: "Sales tax credit"})
		}
		return postings, nil
	}
	postings = append(postings, Posting{AccountID: ar, Debit: invoice.TotalCents, Memo: "Invoice issued"})
	postings = append(postings, invoiceRevenuePostings(invoice)...)
	if invoice.TaxAmountCents > 0 {
		tax, err := accountIDByRole(st, AccountRoleSalesTaxPayable)
		if err != nil {
			return nil, err
		}
		postings = append(postings, Posting{AccountID: tax, Credit: invoice.TaxAmountCents, Memo: "Sales tax payable"})
	}
	return postings, nil
}

func (s *Store) IssueInvoice(ctx Context, invoiceID string) (Invoice, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionInvoiceIssue); err != nil {
		return Invoice{}, "", err
	}
	invoiceID = strings.TrimSpace(invoiceID)
	if invoiceID == "" {
		return Invoice{}, "", appErr(ErrValidation, "invoice id is required")
	}
	invoice, ok := st.Invoices[invoiceID]
	if !ok {
		return Invoice{}, "", appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	if invoice.Status == SourceDocumentVoid {
		return Invoice{}, "", appErr(ErrValidation, "void invoice cannot be issued")
	}
	if invoice.Status == SourceDocumentPaid || invoice.Status == InvoiceStatusUncollectible {
		return Invoice{}, "", appErr(ErrValidation, "closed invoice cannot be issued")
	}
	root := st.Root
	if invoiceIsDraft(invoice.Status) || (st.effectiveSettings().AccountingBasis == AccountingBasisAccrual && invoice.IssuedJournalEntryID == "") {
		posted, postedRoot, err := s.PostInvoice(ctx, invoice.ID)
		if err != nil {
			return Invoice{}, "", err
		}
		invoice = posted
		if postedRoot != "" {
			root = postedRoot
		}
		st, err = s.LoadState()
		if err != nil {
			return Invoice{}, "", err
		}
		if current, ok := st.Invoices[invoice.ID]; ok {
			invoice = current
		}
	}
	if invoice.IssuedSnapshot != nil && !invoiceIsDraft(invoice.Status) && invoice.Status != SourceDocumentOpen {
		return enrichInvoice(invoice, s.now()), root, nil
	}
	if invoice.IssuedSnapshot == nil {
		snapshot := issuedSnapshotFrom(invoice)
		invoice.IssuedSnapshot = &snapshot
	}
	if invoice.IssuedAt.IsZero() {
		invoice.IssuedAt = s.now().UTC()
	}
	if invoiceIsDraft(invoice.Status) || invoice.Status == SourceDocumentOpen {
		invoice.Status = InvoiceStatusIssued
	}
	invoice.UpdatedAt = s.now().UTC()
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "invoice", invoice.ID, "invoice issue", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), root)
	if err != nil {
		return Invoice{}, "", err
	}
	if hash != "" {
		root = hash
	}
	return enrichInvoice(invoice, s.now()), root, nil
}

func (s *Store) UpdateDraftInvoice(ctx Context, invoice Invoice) (Invoice, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Invoice{}, "", err
	}
	invoice.ID = strings.TrimSpace(invoice.ID)
	if invoice.ID == "" {
		return Invoice{}, "", appErr(ErrValidation, "invoice id is required")
	}
	existing, ok := st.Invoices[invoice.ID]
	if !ok {
		return Invoice{}, "", appErr(ErrNotFound, "invoice %s not found", invoice.ID)
	}
	if existing.IssuedSnapshot != nil || !invoiceIsDraft(existing.Status) {
		return Invoice{}, "", appErr(ErrValidation, "issued invoice is immutable; void and issue a credit memo to correct it")
	}
	invoice.CreatedAt = existing.CreatedAt
	invoice.CreatedBy = existing.CreatedBy
	invoice.Payments = existing.Payments
	invoice.IssuedJournalEntryID = existing.IssuedJournalEntryID
	invoice.PaymentJournalEntryIDs = existing.PaymentJournalEntryIDs
	invoice, err = normalizeInvoice(st, invoice)
	if err != nil {
		return Invoice{}, "", err
	}
	if !invoiceIsDraft(invoice.Status) && invoice.Status != existing.Status {
		return Invoice{}, "", appErr(ErrValidation, "draft invoice update cannot change status to %q", invoice.Status)
	}
	invoice.Status = existing.Status
	now := s.now().UTC()
	invoice.UpdatedAt = now
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "invoice", invoice.ID, "invoice update", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), st.Root)
	if err != nil {
		return Invoice{}, "", err
	}
	return enrichInvoice(invoice, now), hash, nil
}

func (s *Store) VoidInvoice(ctx Context, invoiceID, reason string) (Invoice, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionInvoiceVoid); err != nil {
		return Invoice{}, "", err
	}
	invoiceID = strings.TrimSpace(invoiceID)
	reason = strings.TrimSpace(reason)
	if invoiceID == "" {
		return Invoice{}, "", appErr(ErrValidation, "invoice id is required")
	}
	if reason == "" {
		return Invoice{}, "", appErr(ErrValidation, "void reason is required")
	}
	invoice, ok := st.Invoices[invoiceID]
	if !ok {
		return Invoice{}, "", appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	if invoice.Status == SourceDocumentVoid {
		return enrichInvoice(invoice, s.now()), st.Root, nil
	}
	if invoice.Status == SourceDocumentPaid {
		return Invoice{}, "", appErr(ErrValidation, "paid invoice cannot be voided; reverse the payment first")
	}
	paid, err := invoicePaidAmount(invoice)
	if err != nil {
		return Invoice{}, "", err
	}
	if paid > 0 {
		return Invoice{}, "", appErr(ErrValidation, "partially paid invoice cannot be voided; reverse payments first")
	}
	root := st.Root
	if invoice.IssuedJournalEntryID != "" {
		original, ok := st.JournalEntries[invoice.IssuedJournalEntryID]
		if !ok {
			return Invoice{}, "", appErr(ErrValidation, "issued journal entry %s not found", invoice.IssuedJournalEntryID)
		}
		if err := ensurePostingDateOpen(st, invoice.InvoiceDate); err != nil {
			return Invoice{}, "", err
		}
		entry, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
			Date:               invoice.InvoiceDate,
			Memo:               "Invoice voided " + invoice.InvoiceNumber,
			Workflow:           "invoice.void",
			PostingSemantics:   "invoice_voided",
			SourceDocumentType: "invoice",
			SourceDocumentID:   invoice.ID,
			Source:             "invoice",
			SourceKey:          invoice.ID + ":void",
			Postings:           reversePostings(original.Postings),
			Metadata:           map[string]string{"reason": reason},
		})
		if err != nil {
			return Invoice{}, "", err
		}
		if newRoot != "" {
			root = newRoot
		}
		invoice.PaymentJournalEntryIDs = append(invoice.PaymentJournalEntryIDs, entry.ID)
	}
	invoice.Status = SourceDocumentVoid
	invoice.VoidReason = reason
	invoice.VoidedAt = s.now().UTC()
	invoice.UpdatedAt = invoice.VoidedAt
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "invoice", invoice.ID, "invoice void", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), root)
	if err != nil {
		return Invoice{}, "", err
	}
	if hash != "" {
		root = hash
	}
	return enrichInvoice(invoice, s.now()), root, nil
}

func (s *Store) CreateCreditMemo(ctx Context, invoice Invoice) (Invoice, string, error) {
	invoice.Kind = InvoiceKindCreditMemo
	if strings.TrimSpace(string(invoice.Status)) == "" {
		invoice.Status = InvoiceStatusDraft
	}
	return s.CreateInvoice(ctx, invoice)
}

func (s *Store) SendInvoice(ctx Context, invoiceID string, req InvoiceSendRequest, opts InvoicePublicOptions) (InvoiceSendResult, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionInvoiceSend); err != nil {
		return InvoiceSendResult{}, "", err
	}
	invoiceID = strings.TrimSpace(invoiceID)
	if invoiceID == "" {
		return InvoiceSendResult{}, "", appErr(ErrValidation, "invoice id is required")
	}
	invoice, ok := st.Invoices[invoiceID]
	if !ok {
		return InvoiceSendResult{}, "", appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	if invoice.Status == SourceDocumentVoid {
		return InvoiceSendResult{}, "", appErr(ErrValidation, "void invoice cannot be sent")
	}
	if invoiceIsDraft(invoice.Status) {
		return InvoiceSendResult{}, "", appErr(ErrValidation, "draft invoice must be issued before send")
	}
	root := st.Root
	if invoice.IssuedSnapshot == nil || invoice.PublicTokenHash == "" || invoice.Status == SourceDocumentOpen {
		issued, issuedRoot, err := s.IssueInvoice(ctx, invoice.ID)
		if err != nil {
			return InvoiceSendResult{}, "", err
		}
		invoice = issued
		if issuedRoot != "" {
			root = issuedRoot
		}
		st, err = s.LoadState()
		if err != nil {
			return InvoiceSendResult{}, "", err
		}
		if current, ok := st.Invoices[invoice.ID]; ok {
			invoice = current
		}
	}
	link, rawToken, err := s.ensurePublicLink(ctx, invoice.ID, opts, false)
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	st, err = s.LoadState()
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	invoice = st.Invoices[invoice.ID]
	root = st.Root
	to := strings.TrimSpace(req.To)
	if to == "" {
		if customer, ok := st.Customers[invoice.CustomerID]; ok {
			to = strings.TrimSpace(customer.Email)
		}
	}
	if to == "" {
		return InvoiceSendResult{}, "", appErr(ErrValidation, "send requires --to or a customer email")
	}
	from := strings.TrimSpace(opts.FromAddress)
	if from == "" {
		from = displaySender(invoice)
	}
	subject := "Invoice " + invoice.InvoiceNumber
	body := "Future Perfect issues and tracks your invoices. Your customer pays you — card through your Stripe, or ACH/wire to your bank. We never take the payment. Your books update when it clears.\n\nPay or view this invoice:\n" + link.URL + "\n"
	view, err := s.publicViewFromInvoice(st, invoice, opts.Tenant, rawToken)
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	pdf, err := RenderInvoicePDF(view)
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	mail := OutboundEmail{From: from, To: to, Subject: subject, Body: body, PDFFilename: invoice.InvoiceNumber + ".pdf", PDF: pdf}
	sent := false
	if opts.SendMail != nil {
		if err := opts.SendMail(mail); err != nil {
			return InvoiceSendResult{}, "", err
		}
		sent = true
	}
	now := s.now().UTC()
	invoice.SentAt = now
	invoice.SentTo = to
	if invoice.Status == InvoiceStatusIssued || invoice.Status == SourceDocumentOpen {
		invoice.Status = InvoiceStatusSent
	}
	invoice.UpdatedAt = now
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "invoice", invoice.ID, "invoice send", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), root)
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	if hash != "" {
		root = hash
	}
	return InvoiceSendResult{
		Invoice:   enrichInvoice(invoice, now),
		To:        to,
		PublicURL: link.URL,
		Subject:   subject,
		Body:      body,
		Sent:      sent,
	}, root, nil
}

func (s *Store) RemindInvoice(ctx Context, invoiceID string, opts InvoicePublicOptions) (InvoiceSendResult, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionInvoiceSend); err != nil {
		return InvoiceSendResult{}, "", err
	}
	invoice, ok := st.Invoices[strings.TrimSpace(invoiceID)]
	if !ok {
		return InvoiceSendResult{}, "", appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	if !invoiceIsIssuedLike(invoice.Status) {
		return InvoiceSendResult{}, "", appErr(ErrValidation, "only issued invoices can be reminded")
	}
	link, _, err := s.ensurePublicLink(ctx, invoice.ID, opts, false)
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	st, err = s.LoadState()
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	invoice = st.Invoices[invoice.ID]
	invoice.RemindedAt = s.now().UTC()
	invoice.UpdatedAt = invoice.RemindedAt
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEventAt(ctx, "invoice", invoice.ID, "invoice remind", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), st.Root)
	if err != nil {
		return InvoiceSendResult{}, "", err
	}
	return InvoiceSendResult{
		Invoice:   enrichInvoice(invoice, s.now()),
		PublicURL: link.URL,
		Subject:   "Reminder: invoice " + invoice.InvoiceNumber,
		Body:      "Invoice reminder is recorded. Public link: " + link.URL,
		Sent:      false,
	}, hash, nil
}

func issuedSnapshotFrom(invoice Invoice) IssuedInvoiceSnapshot {
	lines := append([]InvoiceLineItem(nil), invoice.LineItems...)
	return IssuedInvoiceSnapshot{
		InvoiceNumber:       invoice.InvoiceNumber,
		CustomerID:          invoice.CustomerID,
		InvoiceDate:         invoice.InvoiceDate,
		DueDate:             invoice.DueDate,
		Terms:               invoice.Terms,
		Currency:            invoice.Currency,
		LineItems:           lines,
		SubtotalCents:       invoice.SubtotalCents,
		TaxAmountCents:      invoice.TaxAmountCents,
		RetainageCents:      invoice.RetainageCents,
		TotalCents:          invoice.TotalCents,
		Memo:                invoice.Memo,
		PONumber:            invoice.PONumber,
		Branding:            invoice.Branding,
		PaymentInstructions: invoice.PaymentInstructions,
		Kind:                invoice.Kind,
		CreditOfInvoiceID:   invoice.CreditOfInvoiceID,
	}
}

func newPublicToken() (raw string, hash string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", appErr(ErrInternal, "generate public token: %s", err)
	}
	raw = hex.EncodeToString(buf)
	return raw, HashPublicToken(raw), nil
}

func HashPublicToken(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func displaySender(invoice Invoice) string {
	name := strings.TrimSpace(invoice.Branding.LegalName)
	if name == "" {
		name = strings.TrimSpace(invoice.Branding.DBA)
	}
	if name == "" {
		name = "Invoice"
	}
	return name + " <invoices@localhost>"
}

package infobase

import (
	"strings"
	"time"
)

type customerUpsertPayload struct {
	Customer Customer `json:"customer"`
}

type invoiceCreatePayload struct {
	Invoice Invoice `json:"invoice"`
}

type invoiceUpdatePayload struct {
	Invoice Invoice `json:"invoice"`
}

type InvoicePaymentRequest struct {
	Date            string `json:"date"`
	AmountCents     int64  `json:"amount_cents"`
	CashAccountID   string `json:"cash_account_id"`
	ExternalSource  string `json:"external_source,omitempty"`
	ExternalID      string `json:"external_id,omitempty"`
	PaymentEvidence string `json:"payment_evidence,omitempty"`
}

func (s *Store) UpsertCustomer(ctx Context, customer Customer) (Customer, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Customer{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Customer{}, "", err
	}
	customer.Name = strings.TrimSpace(customer.Name)
	if customer.Name == "" {
		return Customer{}, "", appErr(ErrValidation, "customer name is required")
	}
	customer.ExternalRefs, err = normalizeExternalRefs(customer.ExternalRefs)
	if err != nil {
		return Customer{}, "", err
	}
	now := s.now().UTC()
	if strings.TrimSpace(customer.ID) == "" {
		customer.ID = makeID("cust", strings.ToLower(customer.Name))
	}
	existing, exists := st.Customers[customer.ID]
	if exists {
		customer.CreatedAt = existing.CreatedAt
		customer.CreatedBy = existing.CreatedBy
	} else {
		customer.CreatedAt = now
		customer.CreatedBy = ctx.Actor
	}
	customer.UpdatedAt = now
	customer.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "customer", customer.ID, "customer upsert", wrapEvent("customer.upsert", customerUpsertPayload{Customer: customer}), true)
	return customer, hash, err
}

func (s *Store) CreateInvoice(ctx Context, invoice Invoice) (Invoice, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Invoice{}, "", err
	}
	invoice, err = normalizeInvoice(st, invoice)
	if err != nil {
		return Invoice{}, "", err
	}
	if _, exists := st.Invoices[invoice.ID]; exists {
		return Invoice{}, "", appErr(ErrConflict, "invoice already exists: %s", invoice.ID)
	}
	now := s.now().UTC()
	invoice.CreatedAt = now
	invoice.UpdatedAt = now
	invoice.CreatedBy = ctx.Actor
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "invoice", invoice.ID, "invoice create", wrapEvent("invoice.create", invoiceCreatePayload{Invoice: invoice}), true)
	return invoice, hash, err
}

func (s *Store) PostInvoice(ctx Context, invoiceID string) (Invoice, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
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
		return Invoice{}, "", appErr(ErrValidation, "void invoice cannot be posted")
	}
	settings := st.effectiveSettings()
	if invoice.Status != SourceDocumentImported {
		if settings.AccountingBasis != AccountingBasisAccrual || invoice.IssuedJournalEntryID != "" {
			return invoice, st.Root, nil
		}
	}
	root := st.Root
	if settings.AccountingBasis == AccountingBasisAccrual && invoice.IssuedJournalEntryID == "" {
		ar, err := accountIDByRole(st, AccountRoleAccountsReceivable)
		if err != nil {
			return Invoice{}, "", err
		}
		postings := []Posting{{AccountID: ar, Debit: invoice.TotalCents, Memo: "Invoice issued"}}
		postings = append(postings, invoiceRevenuePostings(invoice)...)
		if invoice.TaxAmountCents > 0 {
			tax, err := accountIDByRole(st, AccountRoleSalesTaxPayable)
			if err != nil {
				return Invoice{}, "", err
			}
			postings = append(postings, Posting{AccountID: tax, Credit: invoice.TaxAmountCents, Memo: "Sales tax payable"})
		}
		entry, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
			Date:               invoice.InvoiceDate,
			Memo:               "Invoice issued " + invoice.InvoiceNumber,
			Workflow:           "invoice.post",
			PostingSemantics:   "invoice_issued",
			SourceDocumentType: "invoice",
			SourceDocumentID:   invoice.ID,
			Source:             "invoice",
			SourceKey:          invoice.ID + ":issued",
			Postings:           postings,
		})
		if err != nil {
			return Invoice{}, "", err
		}
		root = newRoot
		invoice.IssuedJournalEntryID = entry.ID
	}
	if invoice.Status == SourceDocumentImported {
		invoice.Status = SourceDocumentOpen
	}
	invoice.UpdatedAt = s.now().UTC()
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "invoice", invoice.ID, "invoice post", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), true)
	if err != nil {
		return Invoice{}, "", err
	}
	if hash != "" {
		root = hash
	}
	return invoice, root, nil
}

func (s *Store) MarkInvoicePaid(ctx Context, invoiceID string, req InvoicePaymentRequest) (Invoice, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
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
		return Invoice{}, "", appErr(ErrValidation, "void invoice cannot be paid")
	}
	req.Date = strings.TrimSpace(req.Date)
	if req.Date == "" {
		req.Date = s.now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		return Invoice{}, "", appErr(ErrValidation, "payment date must use YYYY-MM-DD")
	}
	if req.AmountCents <= 0 {
		return Invoice{}, "", appErr(ErrValidation, "payment amount must be positive")
	}
	cash, ok := st.Accounts[strings.TrimSpace(req.CashAccountID)]
	if !ok {
		return Invoice{}, "", appErr(ErrValidation, "cash account %s not found", req.CashAccountID)
	}
	if cash.Role != AccountRoleOperatingCash && cash.Role != AccountRoleBankAccount {
		return Invoice{}, "", appErr(ErrValidation, "invoice payment cash account must have role %q or %q", AccountRoleOperatingCash, AccountRoleBankAccount)
	}
	settings := st.effectiveSettings()
	if settings.AccountingBasis == AccountingBasisAccrual && invoice.IssuedJournalEntryID == "" {
		return Invoice{}, "", appErr(ErrValidation, "accrual invoice must be posted before payment")
	}
	sourceKey := invoice.ID + ":paid:" + req.Date + ":" + int64String(req.AmountCents)
	if req.ExternalSource != "" && req.ExternalID != "" {
		sourceKey = "payment:" + strings.ToLower(strings.TrimSpace(req.ExternalSource)) + ":" + strings.TrimSpace(req.ExternalID)
	}
	paymentID := makeID("pay", invoice.ID, req.Date, int64String(req.AmountCents), sourceKey)
	for _, payment := range invoice.Payments {
		if payment.ID == paymentID {
			return invoice, st.Root, nil
		}
	}
	if paid := invoicePaidAmount(invoice); paid+req.AmountCents > invoice.TotalCents {
		return Invoice{}, "", appErr(ErrValidation, "payment exceeds open invoice balance")
	}
	postings := []Posting{{AccountID: cash.ID, Debit: req.AmountCents, Memo: "Invoice payment"}}
	if settings.AccountingBasis == AccountingBasisAccrual {
		ar, err := accountIDByRole(st, AccountRoleAccountsReceivable)
		if err != nil {
			return Invoice{}, "", err
		}
		postings = append(postings, Posting{AccountID: ar, Credit: req.AmountCents, Memo: "Clear accounts receivable"})
	} else {
		if req.AmountCents != invoice.TotalCents {
			return Invoice{}, "", appErr(ErrValidation, "cash-basis invoice payment must equal invoice total until partial payment allocation is supported")
		}
		postings = append(postings, invoiceRevenuePostings(invoice)...)
		if invoice.TaxAmountCents > 0 {
			tax, err := accountIDByRole(st, AccountRoleSalesTaxPayable)
			if err != nil {
				return Invoice{}, "", err
			}
			postings = append(postings, Posting{AccountID: tax, Credit: invoice.TaxAmountCents, Memo: "Sales tax collected"})
		}
	}
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               req.Date,
		Memo:               "Invoice paid " + invoice.InvoiceNumber,
		Workflow:           "invoice.mark_paid",
		PostingSemantics:   "invoice_paid",
		SourceDocumentType: "invoice",
		SourceDocumentID:   invoice.ID,
		Source:             "invoice",
		SourceKey:          sourceKey,
		Postings:           postings,
	})
	if err != nil {
		return Invoice{}, "", err
	}
	payment := InvoicePayment{
		ID:              paymentID,
		Date:            req.Date,
		AmountCents:     req.AmountCents,
		CashAccountID:   cash.ID,
		JournalEntryID:  entry.ID,
		ExternalSource:  strings.TrimSpace(req.ExternalSource),
		ExternalID:      strings.TrimSpace(req.ExternalID),
		PaymentEvidence: strings.TrimSpace(req.PaymentEvidence),
	}
	invoice.Payments = append(invoice.Payments, payment)
	invoice.PaymentJournalEntryIDs = append(invoice.PaymentJournalEntryIDs, entry.ID)
	if invoicePaidAmount(invoice) == invoice.TotalCents {
		invoice.Status = SourceDocumentPaid
	} else {
		invoice.Status = SourceDocumentOpen
	}
	invoice.UpdatedAt = s.now().UTC()
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "invoice", invoice.ID, "invoice mark-paid", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), true)
	if err != nil {
		return Invoice{}, "", err
	}
	if hash != "" {
		root = hash
	}
	return invoice, root, nil
}

func normalizeInvoice(st State, invoice Invoice) (Invoice, error) {
	invoice.InvoiceNumber = strings.TrimSpace(invoice.InvoiceNumber)
	invoice.CustomerID = strings.TrimSpace(invoice.CustomerID)
	if invoice.InvoiceNumber == "" {
		return Invoice{}, appErr(ErrValidation, "invoice number is required")
	}
	if _, ok := st.Customers[invoice.CustomerID]; !ok {
		return Invoice{}, appErr(ErrValidation, "invoice references unknown customer %s", invoice.CustomerID)
	}
	if invoice.InvoiceDate == "" {
		return Invoice{}, appErr(ErrValidation, "invoice date is required")
	}
	if _, err := time.Parse("2006-01-02", invoice.InvoiceDate); err != nil {
		return Invoice{}, appErr(ErrValidation, "invoice date must use YYYY-MM-DD")
	}
	if invoice.DueDate != "" {
		if _, err := time.Parse("2006-01-02", invoice.DueDate); err != nil {
			return Invoice{}, appErr(ErrValidation, "due date must use YYYY-MM-DD")
		}
	}
	if len(invoice.LineItems) == 0 {
		return Invoice{}, appErr(ErrValidation, "invoice requires at least one line item")
	}
	var subtotal int64
	for i := range invoice.LineItems {
		line := &invoice.LineItems[i]
		line.Description = strings.TrimSpace(line.Description)
		line.RevenueAccountID = strings.TrimSpace(line.RevenueAccountID)
		if line.Quantity <= 0 {
			return Invoice{}, appErr(ErrValidation, "invoice line %d quantity must be positive", i)
		}
		expected := line.Quantity * line.UnitAmountCents
		if line.AmountCents == 0 {
			line.AmountCents = expected
		}
		if line.AmountCents != expected {
			return Invoice{}, appErr(ErrValidation, "invoice line %d amount does not equal quantity times unit amount", i)
		}
		account, ok := st.Accounts[line.RevenueAccountID]
		if !ok {
			return Invoice{}, appErr(ErrValidation, "invoice line %d revenue account %s not found", i, line.RevenueAccountID)
		}
		if account.Type != AccountRevenue {
			return Invoice{}, appErr(ErrValidation, "invoice line %d account must be revenue", i)
		}
		subtotal += line.AmountCents
	}
	if invoice.SubtotalCents == 0 {
		invoice.SubtotalCents = subtotal
	}
	if invoice.SubtotalCents != subtotal {
		return Invoice{}, appErr(ErrValidation, "invoice subtotal does not equal line item total")
	}
	if invoice.TaxAmountCents < 0 {
		return Invoice{}, appErr(ErrValidation, "invoice tax cannot be negative")
	}
	total := invoice.SubtotalCents + invoice.TaxAmountCents
	if invoice.TotalCents == 0 {
		invoice.TotalCents = total
	}
	if invoice.TotalCents != total {
		return Invoice{}, appErr(ErrValidation, "invoice total must equal subtotal plus tax")
	}
	if invoice.Status == "" {
		invoice.Status = SourceDocumentImported
	}
	switch invoice.Status {
	case SourceDocumentImported, SourceDocumentOpen, SourceDocumentPaid, SourceDocumentVoid:
	default:
		return Invoice{}, appErr(ErrValidation, "invalid invoice status %q", invoice.Status)
	}
	externalRefs, err := normalizeExternalRefs(invoice.ExternalRefs)
	if err != nil {
		return Invoice{}, err
	}
	invoice.ExternalRefs = externalRefs
	if strings.TrimSpace(invoice.ID) == "" {
		invoice.ID = makeID("inv", invoice.InvoiceNumber, invoice.CustomerID)
	}
	return invoice, nil
}

func invoiceRevenuePostings(invoice Invoice) []Posting {
	postings := make([]Posting, 0, len(invoice.LineItems))
	for _, line := range invoice.LineItems {
		postings = append(postings, Posting{AccountID: line.RevenueAccountID, Credit: line.AmountCents, Memo: line.Description})
	}
	return postings
}

func invoicePaidAmount(invoice Invoice) int64 {
	var total int64
	for _, payment := range invoice.Payments {
		total += payment.AmountCents
	}
	return total
}

func accountIDByRole(st State, role AccountRole) (string, error) {
	for _, account := range st.Accounts {
		if account.Role == role {
			return account.ID, nil
		}
	}
	return "", appErr(ErrValidation, "required account role %q is not configured", role)
}

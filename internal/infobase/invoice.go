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

type InvoicePaymentReversalRequest struct {
	PaymentID      string `json:"payment_id,omitempty"`
	JournalEntryID string `json:"journal_entry_id,omitempty"`
	Date           string `json:"date"`
	Reason         string `json:"reason"`
}

func (s *Store) GetCustomer(ctx Context, customerID string) (Customer, error) {
	st, err := s.LoadState()
	if err != nil {
		return Customer{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return Customer{}, err
	}
	customerID = strings.TrimSpace(customerID)
	if customerID == "" {
		return Customer{}, appErr(ErrValidation, "customer id is required")
	}
	customer, ok := st.Customers[customerID]
	if !ok {
		return Customer{}, appErr(ErrNotFound, "customer %s not found", customerID)
	}
	return customer, nil
}

func (s *Store) GetInvoice(ctx Context, invoiceID string) (Invoice, error) {
	st, err := s.LoadState()
	if err != nil {
		return Invoice{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return Invoice{}, err
	}
	invoiceID = strings.TrimSpace(invoiceID)
	if invoiceID == "" {
		return Invoice{}, appErr(ErrValidation, "invoice id is required")
	}
	invoice, ok := st.Invoices[invoiceID]
	if !ok {
		return Invoice{}, appErr(ErrNotFound, "invoice %s not found", invoiceID)
	}
	return invoice, nil
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
		if existingID, ok := customerIDByExternalRefs(st, customer.ExternalRefs, ""); ok {
			customer.ID = existingID
		} else {
			customer.ID = makeID("cust", strings.ToLower(customer.Name))
		}
	} else if existingID, ok := customerIDByExternalRefs(st, customer.ExternalRefs, customer.ID); ok {
		return Customer{}, "", appErr(ErrConflict, "external ref already belongs to customer %s", existingID)
	}
	existing, exists := st.Customers[customer.ID]
	if exists {
		if existing.Name == customer.Name && externalRefsEqual(existing.ExternalRefs, customer.ExternalRefs) {
			return existing, st.Root, nil
		}
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

func (s *Store) ImportExternalInvoice(ctx Context, req ExternalInvoiceImportRequest) (ExternalInvoiceImportResult, string, error) {
	if strings.TrimSpace(req.Customer.Name) == "" && strings.TrimSpace(req.Invoice.CustomerID) == "" {
		return ExternalInvoiceImportResult{}, "", appErr(ErrValidation, "external invoice import requires a customer or customer_id")
	}
	if req.Invoice.Status == SourceDocumentPaid && (req.Payment == nil || strings.TrimSpace(req.Payment.PaymentEvidence) == "") {
		return ExternalInvoiceImportResult{}, "", appErr(ErrValidation, "paid external invoice import requires payment evidence and cash account")
	}
	if req.Payment != nil && strings.TrimSpace(req.Payment.PaymentEvidence) == "" {
		return ExternalInvoiceImportResult{}, "", appErr(ErrValidation, "external invoice payment import requires payment evidence")
	}

	root := ""
	var customer Customer
	var err error
	if strings.TrimSpace(req.Customer.Name) != "" || len(req.Customer.ExternalRefs) > 0 || strings.TrimSpace(req.Customer.ID) != "" {
		customer, root, err = s.UpsertCustomer(ctx, req.Customer)
		if err != nil {
			return ExternalInvoiceImportResult{}, "", err
		}
		req.Invoice.CustomerID = customer.ID
	} else {
		customer, err = s.GetCustomer(ctx, req.Invoice.CustomerID)
		if err != nil {
			return ExternalInvoiceImportResult{}, "", err
		}
	}

	intendedStatus := req.Invoice.Status
	if intendedStatus == "" {
		intendedStatus = SourceDocumentImported
	}
	if intendedStatus == SourceDocumentPaid {
		req.Invoice.Status = SourceDocumentImported
	}

	invoice, invoiceRoot, err := s.findOrCreateImportedInvoice(ctx, req.Invoice)
	if err != nil {
		return ExternalInvoiceImportResult{}, "", err
	}
	if invoiceRoot != "" {
		root = invoiceRoot
	}

	result := ExternalInvoiceImportResult{Customer: customer, Invoice: invoice}
	if req.Post || intendedStatus == SourceDocumentOpen || intendedStatus == SourceDocumentPaid {
		invoice, root, err = s.PostInvoice(ctx, invoice.ID)
		if err != nil {
			return ExternalInvoiceImportResult{}, "", err
		}
		result.Posted = true
		result.Invoice = invoice
	}
	if req.Payment != nil {
		invoice, root, err = s.MarkInvoicePaid(ctx, invoice.ID, *req.Payment)
		if err != nil {
			return ExternalInvoiceImportResult{}, "", err
		}
		result.Paid = invoice.Status == SourceDocumentPaid
		result.Invoice = invoice
	}
	return result, root, nil
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
	if existingID, ok := invoiceIDByExternalRefs(st, invoice.ExternalRefs, ""); ok {
		return Invoice{}, "", appErr(ErrConflict, "external ref already belongs to invoice %s", existingID)
	}
	now := s.now().UTC()
	invoice.CreatedAt = now
	invoice.UpdatedAt = now
	invoice.CreatedBy = ctx.Actor
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "invoice", invoice.ID, "invoice create", wrapEvent("invoice.create", invoiceCreatePayload{Invoice: invoice}), true)
	return invoice, hash, err
}

func (s *Store) findOrCreateImportedInvoice(ctx Context, invoice Invoice) (Invoice, string, error) {
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
	if existingID, ok := invoiceIDByExternalRefs(st, invoice.ExternalRefs, ""); ok {
		return st.Invoices[existingID], st.Root, nil
	}
	if existing, ok := st.Invoices[invoice.ID]; ok {
		return existing, st.Root, nil
	}
	return s.CreateInvoice(ctx, invoice)
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
	invoice.Status = invoiceStatusFromPayments(invoice)
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

func (s *Store) ReverseInvoicePayment(ctx Context, invoiceID string, req InvoicePaymentReversalRequest) (Invoice, string, error) {
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
		return Invoice{}, "", appErr(ErrValidation, "void invoice payment cannot be reversed")
	}
	req.PaymentID = strings.TrimSpace(req.PaymentID)
	req.JournalEntryID = strings.TrimSpace(req.JournalEntryID)
	if req.PaymentID == "" && req.JournalEntryID == "" {
		return Invoice{}, "", appErr(ErrValidation, "payment_id or journal_entry_id is required")
	}
	req.Date = strings.TrimSpace(req.Date)
	if req.Date == "" {
		req.Date = s.now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", req.Date); err != nil {
		return Invoice{}, "", appErr(ErrValidation, "reversal date must use YYYY-MM-DD")
	}
	req.Reason = strings.TrimSpace(req.Reason)
	if req.Reason == "" {
		return Invoice{}, "", appErr(ErrValidation, "payment reversal reason is required")
	}
	paymentIndex := -1
	for i, payment := range invoice.Payments {
		if paymentMatchesReversalRequest(payment, req) {
			paymentIndex = i
			break
		}
	}
	if paymentIndex < 0 {
		return Invoice{}, "", appErr(ErrNotFound, "invoice payment not found")
	}
	payment := invoice.Payments[paymentIndex]
	if payment.Reversed {
		return invoice, st.Root, nil
	}
	if payment.JournalEntryID == "" {
		return Invoice{}, "", appErr(ErrValidation, "invoice payment %s has no journal entry to reverse", payment.ID)
	}
	original, ok := st.JournalEntries[payment.JournalEntryID]
	if !ok {
		return Invoice{}, "", appErr(ErrValidation, "payment journal entry %s not found", payment.JournalEntryID)
	}
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               req.Date,
		Memo:               "Invoice payment reversed " + invoice.InvoiceNumber,
		Workflow:           "invoice.reverse_payment",
		PostingSemantics:   "invoice_payment_reversed",
		SourceDocumentType: "invoice",
		SourceDocumentID:   invoice.ID,
		Source:             "invoice",
		SourceKey:          invoice.ID + ":payment_reversal:" + payment.ID,
		Postings:           reversePostings(original.Postings),
		Metadata: map[string]string{
			"payment_id":                payment.ID,
			"original_journal_entry_id": payment.JournalEntryID,
			"reason":                    req.Reason,
		},
	})
	if err != nil {
		return Invoice{}, "", err
	}
	payment.Reversed = true
	payment.ReversalDate = req.Date
	payment.ReversalReason = req.Reason
	payment.ReversalJournalEntryID = entry.ID
	invoice.Payments[paymentIndex] = payment
	invoice.PaymentJournalEntryIDs = append(invoice.PaymentJournalEntryIDs, entry.ID)
	invoice.Status = invoiceStatusFromPayments(invoice)
	invoice.UpdatedAt = s.now().UTC()
	invoice.UpdatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "invoice", invoice.ID, "invoice payment reverse", wrapEvent("invoice.update", invoiceUpdatePayload{Invoice: invoice}), true)
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
	var lineTaxTotal int64
	for i := range invoice.LineItems {
		line := &invoice.LineItems[i]
		line.Description = strings.TrimSpace(line.Description)
		line.RevenueAccountID = strings.TrimSpace(line.RevenueAccountID)
		if line.RevenueAccountID == "" {
			revenueAccountID, err := accountIDByRole(st, AccountRoleDefaultServiceRevenue)
			if err != nil {
				return Invoice{}, appErr(ErrValidation, "invoice line %d revenue account is required because default_service_revenue is not configured", i)
			}
			line.RevenueAccountID = revenueAccountID
		}
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
		if line.TaxAmountCents < 0 {
			return Invoice{}, appErr(ErrValidation, "invoice line %d tax cannot be negative", i)
		}
		account, ok := st.Accounts[line.RevenueAccountID]
		if !ok {
			return Invoice{}, appErr(ErrValidation, "invoice line %d revenue account %s not found", i, line.RevenueAccountID)
		}
		if account.Type != AccountRevenue {
			return Invoice{}, appErr(ErrValidation, "invoice line %d account must be revenue", i)
		}
		if !invoiceRevenueRoles()[account.Role] {
			return Invoice{}, appErr(ErrValidation, "invoice line %d revenue account must have an invoice revenue role", i)
		}
		subtotal += line.AmountCents
		lineTaxTotal += line.TaxAmountCents
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
	if invoice.TaxAmountCents == 0 && lineTaxTotal > 0 {
		invoice.TaxAmountCents = lineTaxTotal
	}
	if invoice.TaxAmountCents > 0 && lineTaxTotal > 0 && invoice.TaxAmountCents != lineTaxTotal {
		return Invoice{}, appErr(ErrValidation, "invoice tax does not equal line item tax total")
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
		if payment.Reversed {
			continue
		}
		total += payment.AmountCents
	}
	return total
}

func invoiceStatusFromPayments(invoice Invoice) SourceDocumentStatus {
	if invoicePaidAmount(invoice) == invoice.TotalCents {
		return SourceDocumentPaid
	}
	return SourceDocumentOpen
}

func paymentMatchesReversalRequest(payment InvoicePayment, req InvoicePaymentReversalRequest) bool {
	if req.PaymentID != "" && req.JournalEntryID != "" {
		return payment.ID == req.PaymentID && payment.JournalEntryID == req.JournalEntryID
	}
	return (req.PaymentID != "" && payment.ID == req.PaymentID) || (req.JournalEntryID != "" && payment.JournalEntryID == req.JournalEntryID)
}

func reversePostings(postings []Posting) []Posting {
	reversed := make([]Posting, 0, len(postings))
	for _, posting := range postings {
		reversed = append(reversed, Posting{
			AccountID: posting.AccountID,
			Debit:     posting.Credit,
			Credit:    posting.Debit,
			Memo:      "Reverse " + posting.Memo,
		})
	}
	return reversed
}

func accountIDByRole(st State, role AccountRole) (string, error) {
	for _, account := range st.Accounts {
		if account.Role == role {
			return account.ID, nil
		}
	}
	return "", appErr(ErrValidation, "required account role %q is not configured", role)
}

func invoiceRevenueRoles() map[AccountRole]bool {
	return map[AccountRole]bool{
		AccountRoleDefaultServiceRevenue: true,
		AccountRoleDefaultProductRevenue: true,
		AccountRoleOtherIncome:           true,
	}
}

func customerIDByExternalRefs(st State, refs []ExternalSourceRef, currentCustomerID string) (string, bool) {
	for _, ref := range refs {
		key := externalRefKey(ref)
		for _, existing := range st.Customers {
			if existing.ID == currentCustomerID {
				continue
			}
			for _, existingRef := range existing.ExternalRefs {
				if externalRefKey(existingRef) == key {
					return existing.ID, true
				}
			}
		}
	}
	return "", false
}

func invoiceIDByExternalRefs(st State, refs []ExternalSourceRef, currentInvoiceID string) (string, bool) {
	for _, ref := range refs {
		key := externalRefKey(ref)
		for _, existing := range st.Invoices {
			if existing.ID == currentInvoiceID {
				continue
			}
			for _, existingRef := range existing.ExternalRefs {
				if externalRefKey(existingRef) == key {
					return existing.ID, true
				}
			}
		}
	}
	return "", false
}

func externalRefsEqual(a, b []ExternalSourceRef) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].SourceSystem != b[i].SourceSystem ||
			a[i].ExternalID != b[i].ExternalID ||
			a[i].ExternalType != b[i].ExternalType ||
			a[i].DisplayName != b[i].DisplayName ||
			a[i].URL != b[i].URL ||
			len(a[i].Metadata) != len(b[i].Metadata) {
			return false
		}
		for key, val := range a[i].Metadata {
			if b[i].Metadata[key] != val {
				return false
			}
		}
	}
	return true
}

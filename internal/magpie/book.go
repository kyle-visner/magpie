package magpie

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

// Book is the first-class programmatic bookkeeping API. The CLI and the MCP
// server are adapters over this type. They do not wrap each other.
type Book struct {
	Store *Store
	Ctx   Context
}

func NewBook(store *Store, ctx Context) *Book {
	if strings.TrimSpace(ctx.Actor) == "" {
		ctx.Actor = "owner"
	}
	return &Book{Store: store, Ctx: ctx}
}

// Tool describes one Magpie operation as an MCP tool. Names are stable and
// map 1:1 onto the domain operations, not onto argv strings.
type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func (b *Book) Init() (map[string]any, error) {
	root, err := b.Store.WriteInitialRoot(b.Ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "store": b.Store.Dir()}, nil
}

func (b *Book) State() (State, error) {
	st, err := b.Store.LoadState()
	if err != nil {
		return State{}, err
	}
	if err := EnsurePermission(st, b.Ctx, PermissionAuditRead); err != nil {
		return State{}, err
	}
	return st, nil
}

func (b *Book) Audit() ([]Node, error) {
	st, err := b.Store.LoadState()
	if err != nil {
		return nil, err
	}
	if err := EnsurePermission(st, b.Ctx, PermissionAuditRead); err != nil {
		return nil, err
	}
	return b.Store.AuditLog()
}

func (b *Book) GetSettings() (BookSettings, error) {
	return b.Store.GetBookSettings(b.Ctx)
}

func (b *Book) SetAccountingBasis(basis AccountingBasis) (map[string]any, error) {
	settings, root, err := b.Store.SetAccountingBasis(b.Ctx, basis)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "settings": settings}, nil
}

func (b *Book) ListAccounts() (map[string]Account, error) {
	st, err := b.require(PermissionLedgerRead)
	if err != nil {
		return nil, err
	}
	return st.Accounts, nil
}

func (b *Book) CreateAccount(account Account) (map[string]any, error) {
	created, root, err := b.Store.CreateAccountWithDetails(b.Ctx, account)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "account": created}, nil
}

func (b *Book) SetAccountNumber(accountID, number string) (map[string]any, error) {
	account, root, err := b.Store.SetAccountNumber(b.Ctx, accountID, number)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "account": account}, nil
}

func (b *Book) SetAccountRole(accountID string, role AccountRole) (map[string]any, error) {
	account, root, err := b.Store.SetAccountRole(b.Ctx, accountID, role)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "account": account}, nil
}

func (b *Book) SetAccountExternalRef(accountID string, ref ExternalSourceRef, role AccountRole) (map[string]any, error) {
	account, root, err := b.Store.SetAccountExternalRefWithRole(b.Ctx, accountID, ref, role)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "account": account}, nil
}

func (b *Book) AccountRoles() []string {
	return AccountRoleNames()
}

func (b *Book) ListJournals() (map[string]JournalEntry, error) {
	st, err := b.require(PermissionLedgerRead)
	if err != nil {
		return nil, err
	}
	return st.JournalEntries, nil
}

func (b *Book) CreateJournal(entry JournalEntry) (map[string]any, error) {
	created, root, err := b.Store.CreateJournalEntry(b.Ctx, entry)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "entry": created}, nil
}

func (b *Book) ListCustomers() (map[string]Customer, error) {
	st, err := b.require(PermissionLedgerRead)
	if err != nil {
		return nil, err
	}
	return st.Customers, nil
}

func (b *Book) GetCustomer(id string) (Customer, error) {
	return b.Store.GetCustomer(b.Ctx, id)
}

func (b *Book) CreateCustomer(customer Customer) (map[string]any, error) {
	created, root, err := b.Store.UpsertCustomer(b.Ctx, customer)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "customer": created}, nil
}

func (b *Book) ListInvoices() (map[string]Invoice, error) {
	st, err := b.require(PermissionLedgerRead)
	if err != nil {
		return nil, err
	}
	return st.Invoices, nil
}

func (b *Book) GetInvoice(id string) (Invoice, error) {
	return b.Store.GetInvoice(b.Ctx, id)
}

func (b *Book) CreateInvoice(invoice Invoice) (map[string]any, error) {
	created, root, err := b.Store.CreateInvoice(b.Ctx, invoice)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": created}, nil
}

func (b *Book) ImportInvoice(req ExternalInvoiceImportRequest) (map[string]any, error) {
	result, root, err := b.Store.ImportExternalInvoice(b.Ctx, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "import": result}, nil
}

func (b *Book) PostInvoice(id string) (map[string]any, error) {
	invoice, root, err := b.Store.PostInvoice(b.Ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": invoice}, nil
}

func (b *Book) MarkInvoicePaid(id string, req InvoicePaymentRequest) (map[string]any, error) {
	invoice, root, err := b.Store.MarkInvoicePaid(b.Ctx, id, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": invoice}, nil
}

func (b *Book) ReverseInvoicePayment(id string, req InvoicePaymentReversalRequest) (map[string]any, error) {
	invoice, root, err := b.Store.ReverseInvoicePayment(b.Ctx, id, req)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": invoice}, nil
}

func (b *Book) IssueInvoice(id string) (map[string]any, error) {
	invoice, root, err := b.Store.IssueInvoice(b.Ctx, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": invoice}, nil
}

func (b *Book) SendInvoice(id, to string) (map[string]any, error) {
	result, root, err := b.Store.SendInvoice(b.Ctx, id, InvoiceSendRequest{To: to}, InvoicePublicOptions{})
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "send": result}, nil
}

func (b *Book) PublicLink(id, tenant, publicBaseURL string, rotate bool) (map[string]any, error) {
	link, _, err := b.Store.PublicLink(b.Ctx, id, InvoicePublicOptions{Tenant: tenant, PublicBaseURL: publicBaseURL}, rotate)
	if err != nil {
		return nil, err
	}
	return map[string]any{"public_link": link}, nil
}

func (b *Book) VoidInvoice(id, reason string) (map[string]any, error) {
	invoice, root, err := b.Store.VoidInvoice(b.Ctx, id, reason)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": invoice}, nil
}

func (b *Book) CreateCreditMemo(invoice Invoice) (map[string]any, error) {
	created, root, err := b.Store.CreateCreditMemo(b.Ctx, invoice)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": created}, nil
}

func (b *Book) RemindInvoice(id string) (map[string]any, error) {
	result, root, err := b.Store.RemindInvoice(b.Ctx, id, InvoicePublicOptions{})
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "remind": result}, nil
}

func (b *Book) UpdateDraftInvoice(invoice Invoice) (map[string]any, error) {
	updated, root, err := b.Store.UpdateDraftInvoice(b.Ctx, invoice)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "invoice": updated}, nil
}

func (b *Book) ListPayouts() (map[string]Payout, error) {
	st, err := b.require(PermissionLedgerRead)
	if err != nil {
		return nil, err
	}
	return st.Payouts, nil
}

func (b *Book) GetPayout(id string) (Payout, error) {
	return b.Store.GetPayout(b.Ctx, id)
}

func (b *Book) ImportPayout(payout Payout) (map[string]any, error) {
	imported, root, err := b.Store.ImportPayout(b.Ctx, payout)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "payout": imported}, nil
}

func (b *Book) ListNotes() (map[string]Note, error) {
	st, err := b.require(PermissionNotesRead)
	if err != nil {
		return nil, err
	}
	return st.Notes, nil
}

func (b *Book) GetNote(id string) (Note, error) {
	return b.Store.GetNote(b.Ctx, id)
}

func (b *Book) PutNote(id, title, body, sensitivity string) (map[string]any, error) {
	note, root, err := b.Store.UpsertNote(b.Ctx, id, title, body, sensitivity)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "note": note}, nil
}

func (b *Book) CreateSnapshot(name string) (Snapshot, error) {
	return b.Store.CreateSnapshot(b.Ctx, name)
}

func (b *Book) Permissions() []string {
	return PermissionNames()
}

func (b *Book) SetRole(name string, permissions []Permission) (map[string]any, error) {
	root, err := b.Store.UpsertRole(b.Ctx, Role{Name: name, Permissions: permissions})
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "role": name}, nil
}

func (b *Book) SetUser(id, role string) (map[string]any, error) {
	root, err := b.Store.UpsertUser(b.Ctx, User{ID: id, Role: role})
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "user": id}, nil
}

func (b *Book) RepairDefaultRoles() (map[string]any, error) {
	result, root, err := b.Store.RepairDefaultRoles(b.Ctx)
	if err != nil {
		return nil, err
	}
	return map[string]any{"root": root, "repair": result}, nil
}

func (b *Book) require(permission Permission) (State, error) {
	st, err := b.Store.LoadState()
	if err != nil {
		return State{}, err
	}
	if err := EnsurePermission(st, b.Ctx, permission); err != nil {
		return State{}, err
	}
	return st, nil
}

// Invoke dispatches a named Book operation. params is a JSON object. This is
// the MCP tools/call path and the only supported non-CLI entry.
func (b *Book) Invoke(name string, params json.RawMessage) (any, error) {
	switch strings.TrimSpace(name) {
	case "init":
		return b.Init()
	case "state":
		return b.State()
	case "audit":
		nodes, err := b.Audit()
		if err != nil {
			return nil, err
		}
		return catalogObject("events", nodes), nil
	case "book_settings_get":
		return b.GetSettings()
	case "book_settings_set":
		var in struct {
			AccountingBasis AccountingBasis `json:"accounting_basis"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SetAccountingBasis(in.AccountingBasis)
	case "ledger_account_list":
		return b.ListAccounts()
	case "ledger_account_create":
		var account Account
		if err := decodeParams(params, &account); err != nil {
			return nil, err
		}
		return b.CreateAccount(account)
	case "ledger_account_number_set":
		var in struct {
			AccountID string `json:"account_id"`
			Number    string `json:"number"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SetAccountNumber(in.AccountID, in.Number)
	case "ledger_account_role_list":
		return catalogObject("roles", b.AccountRoles()), nil
	case "ledger_account_role_set":
		var in struct {
			AccountID string      `json:"account_id"`
			Role      AccountRole `json:"role"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SetAccountRole(in.AccountID, in.Role)
	case "ledger_account_external_ref_set":
		var in struct {
			AccountID    string            `json:"account_id"`
			Role         AccountRole       `json:"role"`
			SourceSystem string            `json:"source_system"`
			ExternalID   string            `json:"external_id"`
			ExternalType string            `json:"external_type"`
			DisplayName  string            `json:"display_name"`
			URL          string            `json:"url"`
			Metadata     map[string]string `json:"metadata"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SetAccountExternalRef(in.AccountID, ExternalSourceRef{
			SourceSystem: in.SourceSystem,
			ExternalID:   in.ExternalID,
			ExternalType: in.ExternalType,
			DisplayName:  in.DisplayName,
			URL:          in.URL,
			Metadata:     in.Metadata,
		}, in.Role)
	case "ledger_journal_list":
		return b.ListJournals()
	case "ledger_journal_create":
		var entry JournalEntry
		if err := decodeParams(params, &entry); err != nil {
			return nil, err
		}
		return b.CreateJournal(entry)
	case "customer_list":
		return b.ListCustomers()
	case "customer_get":
		var in struct {
			CustomerID string `json:"customer_id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.GetCustomer(in.CustomerID)
	case "customer_create":
		var customer Customer
		if err := decodeParams(params, &customer); err != nil {
			return nil, err
		}
		return b.CreateCustomer(customer)
	case "invoice_list":
		return b.ListInvoices()
	case "invoice_get":
		var in struct {
			InvoiceID string `json:"invoice_id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.GetInvoice(in.InvoiceID)
	case "invoice_create":
		var invoice Invoice
		if err := decodeParams(params, &invoice); err != nil {
			return nil, err
		}
		return b.CreateInvoice(invoice)
	case "invoice_import":
		var req ExternalInvoiceImportRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, err
		}
		return b.ImportInvoice(req)
	case "invoice_post":
		var in struct {
			InvoiceID string `json:"invoice_id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.PostInvoice(in.InvoiceID)
	case "invoice_mark_paid":
		var in struct {
			InvoiceID string `json:"invoice_id"`
			InvoicePaymentRequest
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.MarkInvoicePaid(in.InvoiceID, in.InvoicePaymentRequest)
	case "invoice_reverse_payment":
		var in struct {
			InvoiceID string `json:"invoice_id"`
			InvoicePaymentReversalRequest
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.ReverseInvoicePayment(in.InvoiceID, in.InvoicePaymentReversalRequest)
	case "invoice_issue":
		var in struct {
			InvoiceID string `json:"invoice_id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.IssueInvoice(in.InvoiceID)
	case "invoice_send":
		var in struct {
			InvoiceID string `json:"invoice_id"`
			To        string `json:"to"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SendInvoice(in.InvoiceID, in.To)
	case "invoice_public_link":
		var in struct {
			InvoiceID     string `json:"invoice_id"`
			Tenant        string `json:"tenant"`
			PublicBaseURL string `json:"public_base_url"`
			Rotate        bool   `json:"rotate"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.PublicLink(in.InvoiceID, in.Tenant, in.PublicBaseURL, in.Rotate)
	case "invoice_void":
		var in struct {
			InvoiceID string `json:"invoice_id"`
			Reason    string `json:"reason"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.VoidInvoice(in.InvoiceID, in.Reason)
	case "invoice_remind":
		var in struct {
			InvoiceID string `json:"invoice_id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.RemindInvoice(in.InvoiceID)
	case "invoice_update":
		var invoice Invoice
		if err := decodeParams(params, &invoice); err != nil {
			return nil, err
		}
		return b.UpdateDraftInvoice(invoice)
	case "credit_memo_create":
		var invoice Invoice
		if err := decodeParams(params, &invoice); err != nil {
			return nil, err
		}
		return b.CreateCreditMemo(invoice)
	case "payout_list":
		return b.ListPayouts()
	case "payout_get":
		var in struct {
			PayoutID string `json:"payout_id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.GetPayout(in.PayoutID)
	case "payout_import":
		var payout Payout
		if err := decodeParams(params, &payout); err != nil {
			return nil, err
		}
		return b.ImportPayout(payout)
	case "note_list":
		return b.ListNotes()
	case "note_get":
		var in struct {
			ID string `json:"id"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.GetNote(in.ID)
	case "note_put":
		var in struct {
			ID          string `json:"id"`
			Title       string `json:"title"`
			Body        string `json:"body"`
			Sensitivity string `json:"sensitivity"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.PutNote(in.ID, in.Title, in.Body, in.Sensitivity)
	case "snapshot_create":
		var in struct {
			Name string `json:"name"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.CreateSnapshot(in.Name)
	case "rbac_permissions":
		return catalogObject("permissions", b.Permissions()), nil
	case "rbac_role_set":
		var in struct {
			Name        string       `json:"name"`
			Permissions []Permission `json:"permissions"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SetRole(in.Name, in.Permissions)
	case "rbac_user_set":
		var in struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		}
		if err := decodeParams(params, &in); err != nil {
			return nil, err
		}
		return b.SetUser(in.ID, in.Role)
	case "rbac_defaults_repair":
		return b.RepairDefaultRoles()
	default:
		return nil, appErr(ErrValidation, "unknown Magpie operation %q", name)
	}
}

func decodeParams[T any](raw json.RawMessage, dest *T) error {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		raw = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dest); err != nil {
		return appErr(ErrValidation, "invalid arguments: %s", err)
	}
	return nil
}

func ToolCatalog() []Tool {
	cents := "Integer cents. Never send floating-point dollars."
	date := "Accounting date YYYY-MM-DD."
	ids := "Use opaque Magpie IDs returned by prior calls. Do not invent them."
	return []Tool{
		tool("init", "Initialize the book once. Idempotent. Creates the owner actor and defaults the book to cash until book_settings_set is called.", objectSchema(nil, nil)),
		tool("state", "Reconstructed book state. Requires audit:read. Broad; prefer list tools for ordinary work.", objectSchema(nil, nil)),
		tool("audit", "Append-only event history as an object with an events array. Requires audit:read.", objectSchema(nil, nil)),
		tool("book_settings_get", "Read the active accounting basis and modified-cash policy. Call this before posting.", objectSchema(nil, nil)),
		tool("book_settings_set", "Set the book accounting basis. Magpie rejects a change after journals exist.", objectSchema([]string{"accounting_basis"}, map[string]any{
			"accounting_basis": map[string]any{"type": "string", "enum": []string{"cash", "modified_cash", "accrual"}},
		})),
		tool("ledger_account_list", "List chart of accounts keyed by account id. Select accounts by role, then use the returned id.", objectSchema(nil, nil)),
		tool("ledger_account_create", "Create a chart account. "+cents, objectSchema([]string{"name", "type"}, map[string]any{
			"name":         map[string]any{"type": "string"},
			"type":         map[string]any{"type": "string", "enum": []string{"asset", "liability", "equity", "revenue", "expense"}},
			"number":       map[string]any{"type": "string"},
			"role":         map[string]any{"type": "string", "description": "Typed account role such as operating_cash or bank_account."},
			"sensitivity":  map[string]any{"type": "string"},
			"external_refs": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})),
		tool("ledger_account_number_set", "Set a chart number on an existing account.", objectSchema([]string{"account_id", "number"}, map[string]any{
			"account_id": map[string]any{"type": "string", "description": ids},
			"number":     map[string]any{"type": "string"},
		})),
		tool("ledger_account_role_list", "List allowed Magpie account roles as an object with a roles array.", objectSchema(nil, nil)),
		tool("ledger_account_role_set", "Assign a typed role to an account.", objectSchema([]string{"account_id", "role"}, map[string]any{
			"account_id": map[string]any{"type": "string"},
			"role":       map[string]any{"type": "string"},
		})),
		tool("ledger_account_external_ref_set", "Attach a durable external reference (bank account, Stripe, Mercury) to a Magpie account.", objectSchema([]string{"account_id", "source_system", "external_id"}, map[string]any{
			"account_id":    map[string]any{"type": "string"},
			"source_system": map[string]any{"type": "string"},
			"external_id":   map[string]any{"type": "string"},
			"external_type": map[string]any{"type": "string"},
			"display_name":  map[string]any{"type": "string"},
			"url":           map[string]any{"type": "string"},
			"role":          map[string]any{"type": "string"},
			"metadata":      map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}},
		})),
		tool("ledger_journal_list", "List journal entries keyed by id.", objectSchema(nil, nil)),
		tool("ledger_journal_create", "Post a balanced manual journal. Privileged. Requires manual_reason. Prefer invoice and payout tools for ordinary activity. "+cents+" "+date, objectSchema([]string{"date", "memo", "manual_reason", "postings"}, map[string]any{
			"date":          map[string]any{"type": "string", "description": date},
			"memo":          map[string]any{"type": "string"},
			"manual_reason": map[string]any{"type": "string"},
			"source":        map[string]any{"type": "string"},
			"source_key":    map[string]any{"type": "string", "description": "Stable idempotency key for this source document."},
			"postings": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"required": []string{"account_id"},
					"properties": map[string]any{
						"account_id":   map[string]any{"type": "string"},
						"debit_cents":  map[string]any{"type": "integer"},
						"credit_cents": map[string]any{"type": "integer"},
						"memo":         map[string]any{"type": "string"},
					},
				},
			},
		})),
		tool("customer_list", "List customers keyed by id.", objectSchema(nil, nil)),
		tool("customer_get", "Get one customer.", objectSchema([]string{"customer_id"}, map[string]any{"customer_id": map[string]any{"type": "string"}})),
		tool("customer_create", "Create or upsert a customer. Prefer external_refs for idempotent imports.", objectSchema([]string{"name"}, map[string]any{
			"name":          map[string]any{"type": "string"},
			"external_refs": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})),
		tool("invoice_list", "List invoices keyed by id.", objectSchema(nil, nil)),
		tool("invoice_get", "Get one invoice.", objectSchema([]string{"invoice_id"}, map[string]any{"invoice_id": map[string]any{"type": "string"}})),
		tool("invoice_create", "Create an invoice from Magpie's invoice JSON contract. "+cents+" "+date, objectSchema([]string{"customer_id", "invoice_date", "line_items"}, map[string]any{
			"customer_id":     map[string]any{"type": "string"},
			"invoice_number":  map[string]any{"type": "string"},
			"invoice_date":    map[string]any{"type": "string"},
			"due_date":        map[string]any{"type": "string"},
			"line_items":      map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"external_refs":   map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})),
		tool("invoice_import", "Idempotent external invoice import. May post and record payment when evidence is supplied.", objectSchema([]string{"customer", "invoice"}, map[string]any{
			"customer": map[string]any{"type": "object"},
			"invoice":  map[string]any{"type": "object"},
			"post":     map[string]any{"type": "boolean"},
			"payment":  map[string]any{"type": "object"},
		})),
		tool("invoice_post", "Post an invoice. Cash vs accrual semantics follow book settings.", objectSchema([]string{"invoice_id"}, map[string]any{"invoice_id": map[string]any{"type": "string"}})),
		tool("invoice_mark_paid", "Record an invoice payment against a cash or bank account. Never mark paid without external_refs or manual_reason. "+cents, objectSchema([]string{"invoice_id", "amount_cents", "cash_account_id"}, map[string]any{
			"invoice_id":       map[string]any{"type": "string"},
			"date":             map[string]any{"type": "string"},
			"paid_date":        map[string]any{"type": "string"},
			"amount_cents":     map[string]any{"type": "integer"},
			"cash_account_id":  map[string]any{"type": "string"},
			"external_source":  map[string]any{"type": "string"},
			"external_id":      map[string]any{"type": "string"},
			"payment_evidence": map[string]any{"type": "string"},
			"external_refs":    map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
			"manual_reason":    map[string]any{"type": "string"},
		})),
		tool("invoice_issue", "Issue a draft invoice. Freezes an immutable snapshot and posts accrual journals. Prefer this over invoice_post for tenant invoicing.", objectSchema([]string{"invoice_id"}, map[string]any{"invoice_id": map[string]any{"type": "string"}})),
		tool("invoice_send", "Record a send of an issued invoice and return the public link. Attach the issued PDF when mail is configured.", objectSchema([]string{"invoice_id"}, map[string]any{
			"invoice_id": map[string]any{"type": "string"},
			"to":         map[string]any{"type": "string"},
		})),
		tool("invoice_public_link", "Mint a high-entropy public invoice token (hashed at rest). Calling again rotates the token.", objectSchema([]string{"invoice_id"}, map[string]any{
			"invoice_id":      map[string]any{"type": "string"},
			"tenant":          map[string]any{"type": "string"},
			"public_base_url": map[string]any{"type": "string"},
			"rotate":          map[string]any{"type": "boolean"},
		})),
		tool("invoice_void", "Void an unpaid invoice. Issued invoices are immutable; corrections use void plus a credit memo.", objectSchema([]string{"invoice_id", "reason"}, map[string]any{
			"invoice_id": map[string]any{"type": "string"},
			"reason":     map[string]any{"type": "string"},
		})),
		tool("invoice_remind", "Stub reminder: records reminded_at and returns the public link. Full reminder cadence is deferred.", objectSchema([]string{"invoice_id"}, map[string]any{"invoice_id": map[string]any{"type": "string"}})),
		tool("invoice_update", "Update a draft invoice. Rejected after issue.", objectSchema([]string{"id"}, map[string]any{
			"id":             map[string]any{"type": "string"},
			"invoice_number": map[string]any{"type": "string"},
			"customer_id":    map[string]any{"type": "string"},
			"invoice_date":   map[string]any{"type": "string"},
			"due_date":       map[string]any{"type": "string"},
			"line_items":     map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})),
		tool("credit_memo_create", "Create a credit-memo invoice linked to an issued invoice. Do not edit the original.", objectSchema([]string{"credit_of_invoice_id", "customer_id", "invoice_date", "line_items"}, map[string]any{
			"credit_of_invoice_id": map[string]any{"type": "string"},
			"customer_id":          map[string]any{"type": "string"},
			"invoice_number":       map[string]any{"type": "string"},
			"invoice_date":         map[string]any{"type": "string"},
			"line_items":           map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})),
		tool("invoice_reverse_payment", "Reverse an invoice payment. Corrections reverse; they do not edit or delete.", objectSchema([]string{"invoice_id", "date", "reason"}, map[string]any{
			"invoice_id":       map[string]any{"type": "string"},
			"payment_id":       map[string]any{"type": "string"},
			"journal_entry_id": map[string]any{"type": "string"},
			"date":             map[string]any{"type": "string"},
			"reason":           map[string]any{"type": "string"},
		})),
		tool("payout_list", "List payouts keyed by id.", objectSchema(nil, nil)),
		tool("payout_get", "Get one payout.", objectSchema([]string{"payout_id"}, map[string]any{"payout_id": map[string]any{"type": "string"}})),
		tool("payout_import", "Import a normalized payout (processor deposit). Requires destination cash/bank and a merchant-fees account when a fee is present.", objectSchema([]string{"date", "destination_account_id", "net_amount_cents"}, map[string]any{
			"date":                    map[string]any{"type": "string"},
			"description":             map[string]any{"type": "string"},
			"source_account_id":       map[string]any{"type": "string"},
			"destination_account_id":  map[string]any{"type": "string"},
			"net_amount_cents":        map[string]any{"type": "integer"},
			"fee_amount_cents":        map[string]any{"type": "integer"},
			"fee_expense_account_id":  map[string]any{"type": "string"},
			"external_refs":           map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		})),
		tool("note_list", "List durable book notes keyed by id.", objectSchema(nil, nil)),
		tool("note_get", "Get one note.", objectSchema([]string{"id"}, map[string]any{"id": map[string]any{"type": "string"}})),
		tool("note_put", "Create or update a durable markdown note (policy, vendors, Profit First splits).", objectSchema([]string{"title", "body"}, map[string]any{
			"id":          map[string]any{"type": "string"},
			"title":       map[string]any{"type": "string"},
			"body":        map[string]any{"type": "string"},
			"sensitivity": map[string]any{"type": "string"},
		})),
		tool("snapshot_create", "Create a named local root checkpoint. This is not an off-host backup.", objectSchema([]string{"name"}, map[string]any{"name": map[string]any{"type": "string"}})),
		tool("rbac_permissions", "List permission names the book understands as an object with a permissions array.", objectSchema(nil, nil)),
		tool("rbac_role_set", "Create or replace a role.", objectSchema([]string{"name", "permissions"}, map[string]any{
			"name":        map[string]any{"type": "string"},
			"permissions": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		})),
		tool("rbac_user_set", "Assign a user id to a role. The MCP process actor is bound at server start and cannot be changed by a tool.", objectSchema([]string{"id", "role"}, map[string]any{
			"id":   map[string]any{"type": "string"},
			"role": map[string]any{"type": "string"},
		})),
		tool("rbac_defaults_repair", "Add missing current permissions to built-in roles.", objectSchema(nil, nil)),
	}
}

func tool(name, description string, schema map[string]any) Tool {
	return Tool{Name: name, Description: description, InputSchema: schema}
}

// catalogObject wraps a list so MCP structuredContent is a JSON object.
// Role and permission names are unchanged; only the envelope is an object.
func catalogObject(field string, items any) map[string]any {
	if items == nil {
		return map[string]any{field: []any{}}
	}
	return map[string]any{field: items}
}

func objectSchema(required []string, props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           props,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func KnownTool(name string) bool {
	for _, tool := range ToolCatalog() {
		if tool.Name == name {
			return true
		}
	}
	return false
}

func EncodeJSON(v any) ([]byte, error) {
	buf, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode: %w", err)
	}
	return buf, nil
}

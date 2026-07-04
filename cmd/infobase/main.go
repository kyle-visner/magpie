package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"infobase/internal/infobase"
)

type app struct {
	store *infobase.Store
	ctx   infobase.Context
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		writeError(err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	global := flag.NewFlagSet("infobase", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	storeDir := global.String("store", ".infobase", "store directory")
	actor := global.String("actor", "owner", "authenticated actor id")
	role := global.String("role", "", "role to assume; defaults to the actor's configured role")
	if err := global.Parse(args); err != nil {
		return err
	}
	rest := global.Args()
	if len(rest) == 0 {
		return usage(out)
	}
	if rest[0] == "help" || rest[0] == "--help" || rest[0] == "-h" {
		return usage(out)
	}
	store, err := infobase.OpenStore(*storeDir)
	if err != nil {
		return err
	}
	a := app{store: store, ctx: infobase.Context{Actor: *actor, Role: *role}}
	switch rest[0] {
	case "init":
		root, err := store.WriteInitialRoot(a.ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "store": store.Dir()})
	case "state":
		st, err := store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionAuditRead); err != nil {
			return err
		}
		return writeJSON(out, st)
	case "audit":
		st, err := store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionAuditRead); err != nil {
			return err
		}
		nodes, err := store.AuditLog()
		if err != nil {
			return err
		}
		return writeJSON(out, nodes)
	case "rbac":
		return a.rbac(rest[1:], out)
	case "book":
		return a.book(rest[1:], out)
	case "customer":
		return a.customer(rest[1:], out)
	case "invoice":
		return a.invoice(rest[1:], out)
	case "ledger":
		return a.ledger(rest[1:], out)
	case "note":
		return a.note(rest[1:], out)
	case "snapshot":
		return a.snapshot(rest[1:], out)
	default:
		return fmt.Errorf("unknown command %q", rest[0])
	}
}

func (a app) customer(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("customer command required")
	}
	switch args[0] {
	case "create-json":
		fs := newFlagSet("customer create-json")
		file := fs.String("file", "", "JSON file with a customer; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var customer infobase.Customer
		if err := readJSONFile(*file, &customer); err != nil {
			return err
		}
		created, root, err := a.store.UpsertCustomer(a.ctx, customer)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "customer": created})
	case "get":
		fs := newFlagSet("customer get")
		customerID := fs.String("customer-id", "", "InfoBase customer id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		customer, err := a.store.GetCustomer(a.ctx, *customerID)
		if err != nil {
			return err
		}
		return writeJSON(out, customer)
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionLedgerRead); err != nil {
			return err
		}
		return writeJSON(out, st.Customers)
	default:
		return fmt.Errorf("unknown customer command %q", args[0])
	}
}

func (a app) invoice(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("invoice command required")
	}
	switch args[0] {
	case "create-json":
		fs := newFlagSet("invoice create-json")
		file := fs.String("file", "", "JSON file with an invoice; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var invoice infobase.Invoice
		if err := readJSONFile(*file, &invoice); err != nil {
			return err
		}
		created, root, err := a.store.CreateInvoice(a.ctx, invoice)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "invoice": created})
	case "import-json":
		fs := newFlagSet("invoice import-json")
		file := fs.String("file", "", "JSON file with normalized external invoice import; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var req infobase.ExternalInvoiceImportRequest
		if err := readJSONFile(*file, &req); err != nil {
			return err
		}
		result, root, err := a.store.ImportExternalInvoice(a.ctx, req)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "import": result})
	case "post":
		fs := newFlagSet("invoice post")
		invoiceID := fs.String("invoice-id", "", "InfoBase invoice id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		invoice, root, err := a.store.PostInvoice(a.ctx, *invoiceID)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "invoice": invoice})
	case "mark-paid":
		fs := newFlagSet("invoice mark-paid")
		invoiceID := fs.String("invoice-id", "", "InfoBase invoice id")
		cashAccountID := fs.String("cash-account-id", "", "cash or bank account id")
		date := fs.String("paid-date", "", "payment date YYYY-MM-DD")
		amount := fs.Int64("amount-cents", 0, "payment amount in cents")
		externalSource := fs.String("external-source", "", "external payment source")
		externalID := fs.String("external-id", "", "external payment id")
		paymentEvidence := fs.String("payment-evidence", "", "payment evidence provenance")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		invoice, root, err := a.store.MarkInvoicePaid(a.ctx, *invoiceID, infobase.InvoicePaymentRequest{
			Date:            *date,
			AmountCents:     *amount,
			CashAccountID:   *cashAccountID,
			ExternalSource:  *externalSource,
			ExternalID:      *externalID,
			PaymentEvidence: *paymentEvidence,
		})
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "invoice": invoice})
	case "get":
		fs := newFlagSet("invoice get")
		invoiceID := fs.String("invoice-id", "", "InfoBase invoice id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		invoice, err := a.store.GetInvoice(a.ctx, *invoiceID)
		if err != nil {
			return err
		}
		return writeJSON(out, invoice)
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionLedgerRead); err != nil {
			return err
		}
		return writeJSON(out, st.Invoices)
	default:
		return fmt.Errorf("unknown invoice command %q", args[0])
	}
}

func (a app) book(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("book command required")
	}
	switch args[0] {
	case "settings":
		return a.bookSettings(args[1:], out)
	default:
		return fmt.Errorf("unknown book command %q", args[0])
	}
}

func (a app) bookSettings(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("book settings command required")
	}
	switch args[0] {
	case "get":
		settings, err := a.store.GetBookSettings(a.ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, settings)
	case "set":
		fs := newFlagSet("book settings set")
		accountingBasis := fs.String("accounting-basis", "", "cash|modified_cash|accrual")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		settings, root, err := a.store.SetAccountingBasis(a.ctx, infobase.AccountingBasis(*accountingBasis))
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "settings": settings})
	default:
		return fmt.Errorf("unknown book settings command %q", args[0])
	}
}

func (a app) rbac(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("rbac command required")
	}
	switch args[0] {
	case "user":
		if len(args) < 2 || args[1] != "set" {
			return fmt.Errorf("usage: rbac user set --id ID --role ROLE")
		}
		fs := newFlagSet("rbac user set")
		id := fs.String("id", "", "user id")
		role := fs.String("role", "", "role name")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		hash, err := a.store.UpsertUser(a.ctx, infobase.User{ID: *id, Role: *role})
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": hash, "user": *id})
	case "role":
		if len(args) < 2 || args[1] != "set" {
			return fmt.Errorf("usage: rbac role set --name NAME --permissions p1,p2")
		}
		fs := newFlagSet("rbac role set")
		name := fs.String("name", "", "role name")
		perms := fs.String("permissions", "", "comma-separated permissions")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		role := infobase.Role{Name: *name, Permissions: parsePermissions(*perms)}
		hash, err := a.store.UpsertRole(a.ctx, role)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": hash, "role": *name})
	case "permissions":
		return writeJSON(out, infobase.PermissionNames())
	default:
		return fmt.Errorf("unknown rbac command %q", args[0])
	}
}

func (a app) ledger(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("ledger command required")
	}
	switch args[0] {
	case "account":
		return a.ledgerAccount(args[1:], out)
	case "journal":
		return a.ledgerJournal(args[1:], out)
	default:
		return fmt.Errorf("unknown ledger command %q", args[0])
	}
}

func (a app) ledgerAccount(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("ledger account command required")
	}
	switch args[0] {
	case "create":
		fs := newFlagSet("ledger account create")
		number := fs.String("number", "", "chart of accounts number")
		name := fs.String("name", "", "account name")
		typ := fs.String("type", "", "asset|liability|equity|revenue|expense")
		role := fs.String("role", "", "account role, such as bank_account or accounts_receivable")
		sensitivity := fs.String("sensitivity", "internal", "sensitivity label")
		externalSource := fs.String("external-source", "", "external source system")
		externalID := fs.String("external-id", "", "external source id")
		externalType := fs.String("external-type", "", "external source type, such as bank_account or chart_account")
		externalDisplayName := fs.String("external-display-name", "", "external display name")
		externalURL := fs.String("external-url", "", "external source URL")
		externalMetadata := metadataFlag{}
		fs.Var(&externalMetadata, "external-meta", "external metadata key=value; may be repeated")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		externalRefs := []infobase.ExternalSourceRef{{
			SourceSystem: *externalSource,
			ExternalID:   *externalID,
			ExternalType: *externalType,
			DisplayName:  *externalDisplayName,
			URL:          *externalURL,
			Metadata:     externalMetadata,
		}}
		acct, root, err := a.store.CreateAccountWithDetails(a.ctx, infobase.Account{
			Number:       *number,
			Name:         *name,
			Type:         infobase.AccountType(*typ),
			Role:         infobase.AccountRole(*role),
			Sensitivity:  *sensitivity,
			ExternalRefs: externalRefs,
		})
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "account": acct})
	case "create-json":
		fs := newFlagSet("ledger account create-json")
		file := fs.String("file", "", "JSON file with an account; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var account infobase.Account
		if err := readJSONFile(*file, &account); err != nil {
			return err
		}
		acct, root, err := a.store.CreateAccountWithDetails(a.ctx, account)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "account": acct})
	case "number":
		return a.ledgerAccountNumber(args[1:], out)
	case "role":
		return a.ledgerAccountRole(args[1:], out)
	case "external-ref":
		return a.ledgerAccountExternalRef(args[1:], out)
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionLedgerRead); err != nil {
			return err
		}
		return writeJSON(out, st.Accounts)
	default:
		return fmt.Errorf("unknown ledger account command %q", args[0])
	}
}

func (a app) ledgerAccountRole(args []string, out io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: ledger account role set --account-id ACCOUNT_ID --role ROLE")
	}
	switch args[0] {
	case "list":
		return writeJSON(out, infobase.AccountRoleNames())
	case "set":
		fs := newFlagSet("ledger account role set")
		accountID := fs.String("account-id", "", "InfoBase account id")
		role := fs.String("role", "", "account role")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		account, root, err := a.store.SetAccountRole(a.ctx, *accountID, infobase.AccountRole(*role))
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "account": account})
	default:
		return fmt.Errorf("usage: ledger account role set --account-id ACCOUNT_ID --role ROLE")
	}
}

func (a app) ledgerAccountNumber(args []string, out io.Writer) error {
	if len(args) < 1 || args[0] != "set" {
		return fmt.Errorf("usage: ledger account number set --account-id ACCOUNT_ID --number NUMBER")
	}
	fs := newFlagSet("ledger account number set")
	accountID := fs.String("account-id", "", "InfoBase account id")
	number := fs.String("number", "", "chart of accounts number")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	account, root, err := a.store.SetAccountNumber(a.ctx, *accountID, *number)
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"root": root, "account": account})
}

func (a app) ledgerAccountExternalRef(args []string, out io.Writer) error {
	if len(args) < 1 || args[0] != "set" {
		return fmt.Errorf("usage: ledger account external-ref set --account-id ACCOUNT_ID --external-source SOURCE --external-id ID")
	}
	fs := newFlagSet("ledger account external-ref set")
	accountID := fs.String("account-id", "", "InfoBase account id")
	externalSource := fs.String("external-source", "", "external source system")
	externalID := fs.String("external-id", "", "external source id")
	externalType := fs.String("external-type", "", "external source type, such as bank_account or chart_account")
	externalDisplayName := fs.String("external-display-name", "", "external display name")
	externalURL := fs.String("external-url", "", "external source URL")
	externalMetadata := metadataFlag{}
	fs.Var(&externalMetadata, "external-meta", "external metadata key=value; may be repeated")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	account, root, err := a.store.SetAccountExternalRef(a.ctx, *accountID, infobase.ExternalSourceRef{
		SourceSystem: *externalSource,
		ExternalID:   *externalID,
		ExternalType: *externalType,
		DisplayName:  *externalDisplayName,
		URL:          *externalURL,
		Metadata:     externalMetadata,
	})
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"root": root, "account": account})
}

func (a app) ledgerJournal(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("ledger journal command required")
	}
	switch args[0] {
	case "create":
		fs := newFlagSet("ledger journal create")
		file := fs.String("file", "", "JSON file with a journal entry; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var entry infobase.JournalEntry
		if err := readJSONFile(*file, &entry); err != nil {
			return err
		}
		created, root, err := a.store.CreateJournalEntry(a.ctx, entry)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "entry": created})
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionLedgerRead); err != nil {
			return err
		}
		return writeJSON(out, st.JournalEntries)
	default:
		return fmt.Errorf("unknown ledger journal command %q", args[0])
	}
}

func (a app) note(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("note command required")
	}
	switch args[0] {
	case "put":
		fs := newFlagSet("note put")
		id := fs.String("id", "", "note id; generated from title when omitted")
		title := fs.String("title", "", "note title")
		body := fs.String("body", "", "note body")
		bodyFile := fs.String("body-file", "", "file containing note body")
		sensitivity := fs.String("sensitivity", "internal", "sensitivity label")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *bodyFile != "" {
			b, err := os.ReadFile(*bodyFile)
			if err != nil {
				return err
			}
			*body = string(b)
		}
		note, root, err := a.store.UpsertNote(a.ctx, *id, *title, *body, *sensitivity)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "note": note})
	case "get":
		fs := newFlagSet("note get")
		id := fs.String("id", "", "note id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		note, err := a.store.GetNote(a.ctx, *id)
		if err != nil {
			return err
		}
		return writeJSON(out, note)
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := infobase.EnsurePermission(st, a.ctx, infobase.PermissionNotesRead); err != nil {
			return err
		}
		return writeJSON(out, st.Notes)
	default:
		return fmt.Errorf("unknown note command %q", args[0])
	}
}

func (a app) snapshot(args []string, out io.Writer) error {
	if len(args) < 1 || args[0] != "create" {
		return fmt.Errorf("usage: snapshot create --name NAME")
	}
	fs := newFlagSet("snapshot create")
	name := fs.String("name", "", "snapshot name")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	snap, err := a.store.CreateSnapshot(a.ctx, *name)
	if err != nil {
		return err
	}
	return writeJSON(out, snap)
}

func usage(out io.Writer) error {
	_, err := fmt.Fprintln(out, `InfoBase CLI

Commands:
  init
  state
  audit
  book settings get
  book settings set --accounting-basis cash|modified_cash|accrual
  customer create-json --file customer.json
  customer get --customer-id ID
  customer list
  invoice create-json --file invoice.json
  invoice import-json --file external-invoice.json
  invoice post --invoice-id ID
  invoice mark-paid --invoice-id ID --cash-account-id ID --paid-date YYYY-MM-DD --amount-cents N
  invoice get --invoice-id ID
  invoice list
  rbac permissions
  rbac role set --name NAME --permissions p1,p2
  rbac user set --id ID --role ROLE
  ledger account create --name NAME --type TYPE [--number NUMBER] [--role ROLE]
  ledger account create-json --file account.json
  ledger account number set --account-id ID --number NUMBER
  ledger account role list
  ledger account role set --account-id ID --role ROLE
  ledger account external-ref set --account-id ID --external-source SOURCE --external-id ID
  ledger account list
  ledger journal create --file entry.json
  ledger journal list
  note put --title TITLE --body BODY
  note get --id ID
  note list
  snapshot create --name NAME

Global flags:
  --store DIR
  --actor USER_ID
  --role ROLE`)
	return err
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func writeError(err error) {
	code := "error"
	msg := err.Error()
	if appErr, ok := err.(*infobase.AppError); ok {
		code = string(appErr.Code)
		msg = appErr.Message
	}
	_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"code": code, "message": msg})
}

func parsePermissions(s string) []infobase.Permission {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	perms := make([]infobase.Permission, 0, len(parts))
	for _, p := range parts {
		perms = append(perms, infobase.Permission(strings.TrimSpace(p)))
	}
	return perms
}

type metadataFlag map[string]string

func (m *metadataFlag) String() string {
	if m == nil || len(*m) == 0 {
		return ""
	}
	encoded, err := json.Marshal(*m)
	if err != nil {
		return ""
	}
	return string(encoded)
}

func (m *metadataFlag) Set(value string) error {
	key, val, ok := strings.Cut(value, "=")
	if !ok {
		return fmt.Errorf("metadata must use key=value")
	}
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	if key == "" || val == "" {
		return fmt.Errorf("metadata key and value are required")
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[key] = val
	return nil
}

func readJSONFile(path string, into any) error {
	if path == "" {
		return fmt.Errorf("--file is required")
	}
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	return json.NewDecoder(r).Decode(into)
}

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"magpie/internal/magpie"
)

type app struct {
	store *magpie.Store
	ctx   magpie.Context
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		writeError(err)
		os.Exit(1)
	}
}

func run(args []string, out io.Writer) error {
	global := flag.NewFlagSet("magpie", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	storeDir := global.String("store", ".magpie", "store directory")
	jaybaseURL := global.String("jaybase-url", os.Getenv("JAYBASE_URL"), "hosted Jaybase HTTPS origin; defaults to JAYBASE_URL")
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
	storeExplicit := false
	global.Visit(func(f *flag.Flag) {
		if f.Name == "store" {
			storeExplicit = true
		}
	})
	if strings.TrimSpace(*jaybaseURL) != "" && storeExplicit {
		return fmt.Errorf("--store and --jaybase-url/JAYBASE_URL are mutually exclusive")
	}
	var store *magpie.Store
	var err error
	if strings.TrimSpace(*jaybaseURL) != "" {
		store, err = magpie.OpenRemoteStore(*jaybaseURL, os.Getenv("JAYBASE_TOKEN"))
	} else {
		store, err = magpie.OpenStore(*storeDir)
	}
	if err != nil {
		return err
	}
	defer store.Close()
	a := app{store: store, ctx: magpie.Context{Actor: *actor, Role: *role}}
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
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionAuditRead); err != nil {
			return err
		}
		return writeJSON(out, st)
	case "audit":
		st, err := store.LoadState()
		if err != nil {
			return err
		}
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionAuditRead); err != nil {
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
	case "payout":
		return a.payout(rest[1:], out)
	case "bank":
		return a.bank(rest[1:], out)
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

func (a app) bank(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("bank command required")
	}
	switch args[0] {
	case "statement":
		return a.bankStatement(args[1:], out)
	case "transaction":
		return a.bankTransaction(args[1:], out)
	case "transfer":
		return a.bankTransfer(args[1:], out)
	case "reconciliation":
		return a.bankReconciliation(args[1:], out)
	default:
		return fmt.Errorf("unknown bank command %q", args[0])
	}
}

func (a app) bankStatement(args []string, out io.Writer) error {
	if len(args) == 0 || args[0] != "import-json" {
		return fmt.Errorf("usage: bank statement import-json --file statement.json")
	}
	fs := newFlagSet("bank statement import-json")
	file := fs.String("file", "", "canonical bank/card statement JSON; '-' reads stdin")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var statement magpie.BankStatement
	if err := readJSONFile(*file, &statement); err != nil {
		return err
	}
	imported, root, err := a.store.ImportBankStatement(a.ctx, statement)
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"root": root, "statement": imported})
}

func (a app) bankTransaction(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("bank transaction command required")
	}
	switch args[0] {
	case "import-json":
		fs := newFlagSet("bank transaction import-json")
		file := fs.String("file", "", "canonical bank/card transaction JSON; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var transaction magpie.BankTransaction
		if err := readJSONFile(*file, &transaction); err != nil {
			return err
		}
		imported, root, err := a.store.ImportBankTransaction(a.ctx, transaction)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "transaction": imported})
	case "post":
		fs := newFlagSet("bank transaction post")
		transactionID := fs.String("transaction-id", "", "Magpie bank transaction id")
		accountID := fs.String("account-id", "", "classification account id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		transaction, root, err := a.store.PostBankTransaction(a.ctx, *transactionID, *accountID)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "transaction": transaction})
	case "reverse":
		fs := newFlagSet("bank transaction reverse")
		transactionID := fs.String("transaction-id", "", "Magpie bank transaction id")
		date := fs.String("date", "", "reversal date; defaults to and must equal the source transaction date")
		reason := fs.String("reason", "", "audited reversal reason")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		transaction, root, err := a.store.ReverseBankTransaction(a.ctx, *transactionID, *reason, *date)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "transaction": transaction})
	case "reclassify":
		fs := newFlagSet("bank transaction reclassify")
		transactionID := fs.String("transaction-id", "", "Magpie bank transaction id")
		accountID := fs.String("account-id", "", "new classification account id")
		reason := fs.String("reason", "", "audited reclassification reason")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		transaction, root, err := a.store.ReclassifyBankTransaction(a.ctx, *transactionID, *accountID, *reason)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "transaction": transaction})
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionLedgerRead); err != nil {
			return err
		}
		return writeJSON(out, st.BankTransactions)
	default:
		return fmt.Errorf("unknown bank transaction command %q", args[0])
	}
}

func (a app) bankTransfer(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("bank transfer command required")
	}
	fs := newFlagSet("bank transfer " + args[0])
	fromID := fs.String("from-transaction-id", "", "outgoing transaction id")
	toID := fs.String("to-transaction-id", "", "incoming transaction id")
	reason := fs.String("reason", "", "audited transfer reversal reason")
	date := fs.String("date", "", "reversal date; defaults to the original transfer journal date")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	var transactions []magpie.BankTransaction
	var root string
	var err error
	switch args[0] {
	case "pair":
		transactions, root, err = a.store.PairBankTransfer(a.ctx, *fromID, *toID)
	case "reverse":
		transactions, root, err = a.store.ReverseBankTransfer(a.ctx, *fromID, *toID, *reason, *date)
	default:
		return fmt.Errorf("unknown bank transfer command %q", args[0])
	}
	if err != nil {
		return err
	}
	return writeJSON(out, map[string]any{"root": root, "transactions": transactions})
}

func (a app) bankReconciliation(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("bank reconciliation command required")
	}
	fs := newFlagSet("bank reconciliation " + args[0])
	statementID := fs.String("statement-id", "", "Magpie bank statement id")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "preview":
		report, err := a.store.PreviewBankReconciliation(a.ctx, *statementID)
		if err != nil {
			return err
		}
		return writeJSON(out, report)
	case "complete":
		report, root, err := a.store.CompleteBankReconciliation(a.ctx, *statementID)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "reconciliation": report})
	default:
		return fmt.Errorf("unknown bank reconciliation command %q", args[0])
	}
}

func (a app) payout(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("payout command required")
	}
	switch args[0] {
	case "import-json":
		fs := newFlagSet("payout import-json")
		file := fs.String("file", "", "JSON file with normalized external payout import; '-' reads stdin")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		var payout magpie.Payout
		if err := readJSONFile(*file, &payout); err != nil {
			return err
		}
		imported, root, err := a.store.ImportPayout(a.ctx, payout)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "payout": imported})
	case "get":
		fs := newFlagSet("payout get")
		payoutID := fs.String("payout-id", "", "Magpie payout id")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		payout, err := a.store.GetPayout(a.ctx, *payoutID)
		if err != nil {
			return err
		}
		return writeJSON(out, payout)
	case "list":
		st, err := a.store.LoadState()
		if err != nil {
			return err
		}
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionLedgerRead); err != nil {
			return err
		}
		return writeJSON(out, st.Payouts)
	default:
		return fmt.Errorf("unknown payout command %q", args[0])
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
		var customer magpie.Customer
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
		customerID := fs.String("customer-id", "", "Magpie customer id")
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
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionLedgerRead); err != nil {
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
		var invoice magpie.Invoice
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
		var req magpie.ExternalInvoiceImportRequest
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
		invoiceID := fs.String("invoice-id", "", "Magpie invoice id")
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
		invoiceID := fs.String("invoice-id", "", "Magpie invoice id")
		cashAccountID := fs.String("cash-account-id", "", "cash or bank account id")
		date := fs.String("paid-date", "", "payment date YYYY-MM-DD")
		amount := fs.Int64("amount-cents", 0, "payment amount in cents")
		externalSource := fs.String("external-source", "", "external payment source")
		externalID := fs.String("external-id", "", "external payment id")
		paymentEvidence := fs.String("payment-evidence", "", "payment evidence provenance")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		invoice, root, err := a.store.MarkInvoicePaid(a.ctx, *invoiceID, magpie.InvoicePaymentRequest{
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
	case "reverse-payment":
		fs := newFlagSet("invoice reverse-payment")
		invoiceID := fs.String("invoice-id", "", "Magpie invoice id")
		paymentID := fs.String("payment-id", "", "Magpie payment id")
		journalEntryID := fs.String("journal-entry-id", "", "payment journal entry id")
		date := fs.String("reversal-date", "", "reversal date YYYY-MM-DD")
		reason := fs.String("reason", "", "reason for reversal")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		invoice, root, err := a.store.ReverseInvoicePayment(a.ctx, *invoiceID, magpie.InvoicePaymentReversalRequest{
			PaymentID:      *paymentID,
			JournalEntryID: *journalEntryID,
			Date:           *date,
			Reason:         *reason,
		})
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "invoice": invoice})
	case "get":
		fs := newFlagSet("invoice get")
		invoiceID := fs.String("invoice-id", "", "Magpie invoice id")
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
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionLedgerRead); err != nil {
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
		settings, root, err := a.store.SetAccountingBasis(a.ctx, magpie.AccountingBasis(*accountingBasis))
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
		hash, err := a.store.UpsertUser(a.ctx, magpie.User{ID: *id, Role: *role})
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
		role := magpie.Role{Name: *name, Permissions: parsePermissions(*perms)}
		hash, err := a.store.UpsertRole(a.ctx, role)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": hash, "role": *name})
	case "defaults":
		if len(args) < 2 || args[1] != "repair" {
			return fmt.Errorf("usage: rbac defaults repair")
		}
		result, root, err := a.store.RepairDefaultRoles(a.ctx)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "repair": result})
	case "permissions":
		return writeJSON(out, magpie.PermissionNames())
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
		externalRefs := []magpie.ExternalSourceRef{{
			SourceSystem: *externalSource,
			ExternalID:   *externalID,
			ExternalType: *externalType,
			DisplayName:  *externalDisplayName,
			URL:          *externalURL,
			Metadata:     externalMetadata,
		}}
		acct, root, err := a.store.CreateAccountWithDetails(a.ctx, magpie.Account{
			Number:       *number,
			Name:         *name,
			Type:         magpie.AccountType(*typ),
			Role:         magpie.AccountRole(*role),
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
		var account magpie.Account
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
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionLedgerRead); err != nil {
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
		return writeJSON(out, magpie.AccountRoleNames())
	case "set":
		fs := newFlagSet("ledger account role set")
		accountID := fs.String("account-id", "", "Magpie account id")
		role := fs.String("role", "", "account role")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		account, root, err := a.store.SetAccountRole(a.ctx, *accountID, magpie.AccountRole(*role))
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
	accountID := fs.String("account-id", "", "Magpie account id")
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
	accountID := fs.String("account-id", "", "Magpie account id")
	externalSource := fs.String("external-source", "", "external source system")
	externalID := fs.String("external-id", "", "external source id")
	externalType := fs.String("external-type", "", "external source type, such as bank_account or chart_account")
	externalDisplayName := fs.String("external-display-name", "", "external display name")
	externalURL := fs.String("external-url", "", "external source URL")
	role := fs.String("role", "", "optional account role to assign with this external ref")
	externalMetadata := metadataFlag{}
	fs.Var(&externalMetadata, "external-meta", "external metadata key=value; may be repeated")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	account, root, err := a.store.SetAccountExternalRefWithRole(a.ctx, *accountID, magpie.ExternalSourceRef{
		SourceSystem: *externalSource,
		ExternalID:   *externalID,
		ExternalType: *externalType,
		DisplayName:  *externalDisplayName,
		URL:          *externalURL,
		Metadata:     externalMetadata,
	}, magpie.AccountRole(*role))
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
		var entry magpie.JournalEntry
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
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionLedgerRead); err != nil {
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
		if err := magpie.EnsurePermission(st, a.ctx, magpie.PermissionNotesRead); err != nil {
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
	_, err := fmt.Fprintln(out, `Magpie CLI

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
  invoice reverse-payment --invoice-id ID --payment-id ID --reversal-date YYYY-MM-DD --reason REASON
  invoice get --invoice-id ID
  invoice list
  payout import-json --file payout.json
  payout get --payout-id ID
  payout list
  bank statement import-json --file statement.json
  bank transaction import-json --file transaction.json
  bank transaction post --transaction-id ID --account-id ID
  bank transaction reverse --transaction-id ID --reason REASON [--date YYYY-MM-DD]
  bank transaction reclassify --transaction-id ID --account-id ID --reason REASON
  bank transaction list
  bank transfer pair --from-transaction-id ID --to-transaction-id ID
  bank transfer reverse --from-transaction-id ID --to-transaction-id ID --reason REASON [--date YYYY-MM-DD]
  bank reconciliation preview --statement-id ID
  bank reconciliation complete --statement-id ID
  rbac defaults repair
  rbac permissions
  rbac role set --name NAME --permissions p1,p2
  rbac user set --id ID --role ROLE
  ledger account create --name NAME --type TYPE [--number NUMBER] [--role ROLE]
  ledger account create-json --file account.json
  ledger account number set --account-id ID --number NUMBER
  ledger account role list
  ledger account role set --account-id ID --role ROLE
  ledger account external-ref set --account-id ID --external-source SOURCE --external-id ID [--role ROLE]
  ledger account list
  ledger journal create --file entry.json
  ledger journal list
  note put --title TITLE --body BODY
  note get --id ID
  note list
  snapshot create --name NAME

Global flags:
  --store DIR
  --jaybase-url HTTPS_ORIGIN (or JAYBASE_URL; token from JAYBASE_TOKEN)
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
	if appErr, ok := err.(*magpie.AppError); ok {
		code = string(appErr.Code)
		msg = appErr.Message
	}
	_ = json.NewEncoder(os.Stderr).Encode(map[string]string{"code": code, "message": msg})
}

func parsePermissions(s string) []magpie.Permission {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	perms := make([]magpie.Permission, 0, len(parts))
	for _, p := range parts {
		perms = append(perms, magpie.Permission(strings.TrimSpace(p)))
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

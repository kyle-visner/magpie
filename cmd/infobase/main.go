package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
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
	case "ledger":
		return a.ledger(rest[1:], out)
	case "note":
		return a.note(rest[1:], out)
	case "import":
		return a.importData(rest[1:], out)
	case "snapshot":
		return a.snapshot(rest[1:], out)
	case "help", "--help", "-h":
		return usage(out)
	default:
		return fmt.Errorf("unknown command %q", rest[0])
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
		name := fs.String("name", "", "account name")
		typ := fs.String("type", "", "asset|liability|equity|revenue|expense")
		sensitivity := fs.String("sensitivity", "internal", "sensitivity label")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		acct, root, err := a.store.CreateAccount(a.ctx, *name, infobase.AccountType(*typ), *sensitivity)
		if err != nil {
			return err
		}
		return writeJSON(out, map[string]any{"root": root, "account": acct})
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

func (a app) importData(args []string, out io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("import command required")
	}
	switch args[0] {
	case "quickbooks-csv":
		fs := newFlagSet("import quickbooks-csv")
		file := fs.String("file", "", "QuickBooks CSV file")
		cash := fs.String("cash-account", "", "cash/bank account id used as counterparty")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		report, err := a.store.ImportQuickBooksCSV(a.ctx, *file, *cash)
		if err != nil {
			return err
		}
		return writeJSON(out, report)
	case "quickbooks-template":
		return infobase.QuickBooksCSVTemplate(out)
	default:
		return fmt.Errorf("unknown import command %q", args[0])
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
  rbac permissions
  rbac role set --name NAME --permissions p1,p2
  rbac user set --id ID --role ROLE
  ledger account create --name NAME --type TYPE
  ledger account list
  ledger journal create --file entry.json
  ledger journal list
  note put --title TITLE --body BODY
  note get --id ID
  note list
  import quickbooks-csv --file FILE --cash-account ACCOUNT_ID
  import quickbooks-template
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

func parseAmountCents(s string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

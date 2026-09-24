package magpie

import (
	"fmt"
	"regexp"
	"strings"
)

// EventNamespace is the single JayBase namespace for new Magpie appends.
// One allow.types pattern, "magpie.*", covers every type Magpie writes.
const EventNamespace = "magpie"

// RefPrefix is applied to new named refs so a scoped token can set refs to
// RefPattern. Historical period-close refs keep the name stored on the close.
const (
	RefPrefix                   = "magpie-"
	RefPattern                  = "magpie-*"
	LegacyPeriodCloseRefPattern = "period-close-*"
)

const (
	TypeBookSettings    = "magpie.book.settings"
	TypeBankStatement   = "magpie.bank.statement"
	TypeBankTransaction = "magpie.bank.transaction"
	TypeCustomer        = "magpie.customer"
	TypeInvoice         = "magpie.invoice"
	TypeLedgerAccount   = "magpie.ledger.account"
	TypeLedgerJournal   = "magpie.ledger.journal"
	TypeNote            = "magpie.note"
	TypePayout          = "magpie.payout"
	TypePeriodClose     = "magpie.period.close"
	TypePeriodReopen    = "magpie.period.reopen"
	TypeRBACRole        = "magpie.rbac.role"
	TypeRBACUser        = "magpie.rbac.user"
	TypeStoreInit       = "magpie.store.init"
)

const (
	CmdStoreInit                  = "store init"
	CmdRoleUpsert                 = "rbac role upsert"
	CmdDefaultsRepair             = "rbac defaults repair"
	CmdUserUpsert                 = "rbac user upsert"
	CmdBookSettingsSet            = "book settings set"
	CmdLedgerAccountCreate        = "ledger account create"
	CmdLedgerAccountNumberSet     = "ledger account number set"
	CmdLedgerAccountRoleSet       = "ledger account role set"
	CmdLedgerAccountExternalRef   = "ledger account external-ref set"
	CmdLedgerJournalCreate        = "ledger journal create"
	CmdWorkflowJournalCreate      = "workflow journal create"
	CmdBankStatementImport        = "bank statement import"
	CmdBankReconciliationComplete = "bank reconciliation complete"
	CmdBankTransactionImport      = "bank transaction import"
	CmdBankTransactionPost        = "bank transaction post"
	CmdBankTransactionReverse     = "bank transaction reverse"
	CmdBankTransactionReclassify  = "bank transaction reclassify"
	CmdBankTransferPair           = "bank transfer pair"
	CmdBankTransferReverse        = "bank transfer reverse"
	CmdCustomerUpsert             = "customer upsert"
	CmdInvoiceCreate              = "invoice create"
	CmdInvoicePost                = "invoice post"
	CmdInvoiceMarkPaid            = "invoice mark-paid"
	CmdInvoicePaymentReverse      = "invoice payment reverse"
	CmdPayoutCreate               = "payout create"
	CmdPayoutJournalLinksUpdate   = "payout journal links update"
	CmdNoteUpsert                 = "note upsert"
	CmdPeriodCloseComplete        = "period close complete"
	CmdPeriodReopen               = "period reopen"
)

// CatalogEntry is one exact event type and the commands that may append it.
// Hosts install these into a JayBase catalog. Magpie does not install them
// and does not turn catalog enforcement on.
type CatalogEntry struct {
	Type     string   `json:"type"`
	Commands []string `json:"commands"`
}

// writerCatalog is the only set of types and commands Magpie appends.
// Commands are exact stable strings. Grammar matches JayBase server/allow.go
// at a4529fa: namespace.name, and 1-64 letters, digits, spaces, dots, _, -.
var writerCatalog = []CatalogEntry{
	{Type: TypeStoreInit, Commands: []string{CmdStoreInit}},
	{Type: TypeRBACRole, Commands: []string{CmdRoleUpsert, CmdDefaultsRepair}},
	{Type: TypeRBACUser, Commands: []string{CmdUserUpsert}},
	{Type: TypeBookSettings, Commands: []string{CmdBookSettingsSet}},
	{Type: TypeLedgerAccount, Commands: []string{
		CmdLedgerAccountCreate,
		CmdLedgerAccountNumberSet,
		CmdLedgerAccountRoleSet,
		CmdLedgerAccountExternalRef,
	}},
	{Type: TypeLedgerJournal, Commands: []string{CmdLedgerJournalCreate, CmdWorkflowJournalCreate}},
	{Type: TypeBankStatement, Commands: []string{CmdBankStatementImport, CmdBankReconciliationComplete}},
	{Type: TypeBankTransaction, Commands: []string{
		CmdBankTransactionImport,
		CmdBankTransactionPost,
		CmdBankTransactionReverse,
		CmdBankTransactionReclassify,
		CmdBankTransferPair,
		CmdBankTransferReverse,
	}},
	{Type: TypeCustomer, Commands: []string{CmdCustomerUpsert}},
	{Type: TypeInvoice, Commands: []string{
		CmdInvoiceCreate,
		CmdInvoicePost,
		CmdInvoiceMarkPaid,
		CmdInvoicePaymentReverse,
	}},
	{Type: TypePayout, Commands: []string{CmdPayoutCreate, CmdPayoutJournalLinksUpdate}},
	{Type: TypeNote, Commands: []string{CmdNoteUpsert}},
	{Type: TypePeriodClose, Commands: []string{CmdPeriodCloseComplete}},
	{Type: TypePeriodReopen, Commands: []string{CmdPeriodReopen}},
}

// legacyTypeAliases maps historical node types onto the current catalog type.
// Replay reads both. New appends use only the catalog type. History is not
// rewritten, including the undotted names the catalog grammar rejects.
var legacyTypeAliases = map[string]string{
	"book.settings":    TypeBookSettings,
	"bank.statement":   TypeBankStatement,
	"bank.transaction": TypeBankTransaction,
	"customer":         TypeCustomer,
	"invoice":          TypeInvoice,
	"ledger.account":   TypeLedgerAccount,
	"ledger.journal":   TypeLedgerJournal,
	"note":             TypeNote,
	"payout":           TypePayout,
	"period.close":     TypePeriodClose,
	"period.reopen":    TypePeriodReopen,
	"rbac.role":        TypeRBACRole,
	"rbac.user":        TypeRBACUser,
	"store.init":       TypeStoreInit,
}

// Catalog grammar from JayBase server/allow.go (main @ a4529fa). Kept here so
// Magpie can refuse a type or command a host catalog would reject, without
// importing the server or enabling enforcement.
var (
	catalogTypePattern    = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,30}\.[a-z0-9][a-z0-9._-]{0,63}$`)
	catalogCommandPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,63}$`)
)

const (
	maxCatalogCommands = 32
	maxCatalogEntries  = 256
)

var (
	catalogTypes             map[string]struct{}
	catalogCommands          map[string]map[string]struct{}
	magpieReservedTypePrefix map[string]struct{}
)

func init() {
	if err := validateWriterCatalog(writerCatalog); err != nil {
		panic(err)
	}
	catalogTypes = make(map[string]struct{}, len(writerCatalog))
	catalogCommands = make(map[string]map[string]struct{}, len(writerCatalog))
	magpieReservedTypePrefix = map[string]struct{}{}
	for _, entry := range writerCatalog {
		catalogTypes[entry.Type] = struct{}{}
		commands := make(map[string]struct{}, len(entry.Commands))
		for _, command := range entry.Commands {
			commands[command] = struct{}{}
		}
		catalogCommands[entry.Type] = commands
		head, _, _ := strings.Cut(entry.Type, ".")
		magpieReservedTypePrefix[head] = struct{}{}
	}
	for legacy, canonical := range legacyTypeAliases {
		if _, ok := catalogTypes[canonical]; !ok {
			panic(fmt.Sprintf("legacy type %q aliases unknown catalog type %q", legacy, canonical))
		}
		head, _, _ := strings.Cut(legacy, ".")
		magpieReservedTypePrefix[head] = struct{}{}
	}
}

// WriterCatalog returns a copy of the exact types and commands a host installs
// for new Magpie writes. Every type is in EventNamespace.
func WriterCatalog() []CatalogEntry {
	out := make([]CatalogEntry, len(writerCatalog))
	for i, entry := range writerCatalog {
		out[i] = CatalogEntry{Type: entry.Type, Commands: append([]string(nil), entry.Commands...)}
	}
	return out
}

// LegacyTypeAliases returns a copy of historical type name → current type.
// The keys are replay-only. They are absent from WriterCatalog.
func LegacyTypeAliases() map[string]string {
	out := make(map[string]string, len(legacyTypeAliases))
	for legacy, canonical := range legacyTypeAliases {
		out[legacy] = canonical
	}
	return out
}

func validateWriterCatalog(entries []CatalogEntry) error {
	if len(entries) == 0 || len(entries) > maxCatalogEntries {
		return fmt.Errorf("writer catalog must list 1-%d types", maxCatalogEntries)
	}
	seenType := map[string]struct{}{}
	for _, entry := range entries {
		if !catalogTypePattern.MatchString(entry.Type) {
			return fmt.Errorf("type %q must be namespace.name", entry.Type)
		}
		namespace, _, _ := strings.Cut(entry.Type, ".")
		if namespace != EventNamespace {
			return fmt.Errorf("type %q is outside namespace %s", entry.Type, EventNamespace)
		}
		if _, ok := seenType[entry.Type]; ok {
			return fmt.Errorf("type %q is duplicated", entry.Type)
		}
		seenType[entry.Type] = struct{}{}
		if len(entry.Commands) == 0 || len(entry.Commands) > maxCatalogCommands {
			return fmt.Errorf("type %q must list 1-%d commands", entry.Type, maxCatalogCommands)
		}
		seenCommand := map[string]struct{}{}
		for _, command := range entry.Commands {
			if !catalogCommandPattern.MatchString(command) {
				return fmt.Errorf("command %q must be 1-64 letters, digits, spaces, dots, underscores, or hyphens", command)
			}
			if _, ok := seenCommand[command]; ok {
				return fmt.Errorf("command %q is duplicated on type %q", command, entry.Type)
			}
			seenCommand[command] = struct{}{}
		}
	}
	return nil
}

func validateCatalogAppend(typ, command string) error {
	commands, ok := catalogCommands[typ]
	if !ok {
		return appErr(ErrValidation, "type %q is not a Magpie catalog type", typ)
	}
	if _, ok := commands[command]; !ok {
		return appErr(ErrValidation, "command %q is not installed for type %q", command, typ)
	}
	return nil
}

func isMagpieNodeType(typ string) bool {
	if _, ok := catalogTypes[typ]; ok {
		return true
	}
	_, ok := legacyTypeAliases[typ]
	return ok
}

func isStoreInitType(typ string) bool {
	return typ == TypeStoreInit || legacyTypeAliases[typ] == TypeStoreInit
}

func canonicalSnapshotRef(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, `/\\`) {
		return "", appErr(ErrValidation, "snapshot name must be a simple file-safe name")
	}
	if !strings.HasPrefix(name, RefPrefix) {
		name = RefPrefix + name
	}
	if name == RefPrefix || strings.Contains(name, "..") || strings.ContainsAny(name, `/\\`) {
		return "", appErr(ErrValidation, "snapshot name must be a simple file-safe name")
	}
	return name, nil
}

func periodCloseRefName(through string, revision int) string {
	return fmt.Sprintf("%speriod-close-%s-r%d", RefPrefix, through, revision)
}

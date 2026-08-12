package magpie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kyle-visner/jaybase"
)

type Store struct {
	db  storageBackend
	now func() time.Time
}

type storageBackend interface {
	Close() error
	Dir() string
	CurrentRoot() (string, error)
	AppendAt(jaybase.Context, jaybase.AppendOptions, string) (string, error)
	NodesFromRoot(string) ([]jaybase.Node, error)
	AuditLog() ([]jaybase.Node, error)
	NodePayload(jaybase.Node) ([]byte, error)
	NamedRef(string) (string, error)
	WriteNamedRefAt(string, string, string) error
	NodePath(string) string
}

type localStorageBackend struct {
	store *jaybase.Store
}

type Node = jaybase.Node
type EncryptedPayload = jaybase.EncryptedPayload

type eventEnvelope struct {
	Kind string          `json:"kind"`
	Data json.RawMessage `json:"data"`
}

var legacyMagpieNodeTypes = map[string]struct{}{
	"book.settings":    {},
	"bank.statement":   {},
	"bank.transaction": {},
	"customer":         {},
	"invoice":          {},
	"ledger.account":   {},
	"ledger.journal":   {},
	"note":             {},
	"payout":           {},
	"period.close":     {},
	"period.reopen":    {},
	"rbac.role":        {},
	"rbac.user":        {},
	"store.init":       {},
}

var foreignApplicationPrefixes = []string{
	"martin.",
}

func OpenStore(dir string) (*Store, error) {
	db, err := jaybase.OpenStore(dir)
	if err != nil {
		return nil, storageError(err)
	}
	return newStore(&localStorageBackend{store: db}), nil
}

func newStore(db storageBackend) *Store {
	return &Store{
		db:  db,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Store) Close() error {
	return storageError(s.db.Close())
}

func (b *localStorageBackend) Close() error {
	return b.store.Close()
}

func (b *localStorageBackend) Dir() string {
	return b.store.Dir()
}

func (b *localStorageBackend) CurrentRoot() (string, error) {
	return b.store.CurrentRoot()
}

func (b *localStorageBackend) AppendAt(ctx jaybase.Context, options jaybase.AppendOptions, expectedRoot string) (string, error) {
	return b.store.AppendAt(ctx, options, expectedRoot)
}

func (b *localStorageBackend) NodesFromRoot(root string) ([]jaybase.Node, error) {
	return b.store.NodesFromRoot(root)
}

func (b *localStorageBackend) AuditLog() ([]jaybase.Node, error) {
	return b.store.AuditLog()
}

func (b *localStorageBackend) NodePayload(node jaybase.Node) ([]byte, error) {
	return b.store.NodePayload(node)
}

func (b *localStorageBackend) NamedRef(name string) (string, error) {
	return b.store.NamedRef(name)
}

func (b *localStorageBackend) WriteNamedRefAt(name, root, expectedRoot string) error {
	return b.store.WriteNamedRefAt(name, root, expectedRoot)
}

func (b *localStorageBackend) NodePath(hash string) string {
	return b.store.NodePath(hash)
}

func (s *Store) Dir() string {
	return s.db.Dir()
}

func (s *Store) currentRoot() (string, error) {
	root, err := s.db.CurrentRoot()
	if err != nil {
		return "", storageError(err)
	}
	return root, nil
}

func (s *Store) nodePath(hash string) string {
	return s.db.NodePath(hash)
}

// writeNamedRef gives local and hosted backends the same compare-and-swap
// behavior. A failed write is reconciled against the durable value so a lost
// success response or a concurrent writer choosing the same root is idempotent.
func (s *Store) writeNamedRef(name, root string) error {
	expectedRoot, err := s.db.NamedRef(name)
	if err != nil {
		var dbErr *jaybase.AppError
		if !errors.As(err, &dbErr) || dbErr.Code != jaybase.ErrNotFound {
			return storageError(err)
		}
		expectedRoot = ""
	} else if expectedRoot == root {
		return nil
	}

	if err := s.db.WriteNamedRefAt(name, root, expectedRoot); err != nil {
		writeErr := storageError(err)
		current, readErr := s.db.NamedRef(name)
		if readErr == nil && current == root {
			return nil
		}
		return writeErr
	}
	return nil
}

func (s *Store) WriteInitialRoot(ctx Context) (string, error) {
	const appendAttempts = 4
	var conflict error
	for attempt := 0; attempt < appendAttempts; attempt++ {
		state, initialized, err := s.loadState()
		if err != nil {
			return "", err
		}
		if initialized {
			return state.Root, nil
		}

		root, err := s.appendEventAt(ctx, "store.init", "", "store init", initEvent(), state.Root)
		if err == nil {
			return root, nil
		}
		var appError *AppError
		if !errors.As(err, &appError) || appError.Code != ErrConflict {
			return "", err
		}
		conflict = err
	}
	state, initialized, err := s.loadState()
	if err != nil {
		return "", err
	}
	if initialized {
		return state.Root, nil
	}
	return "", conflict
}

func (s *Store) appendEvent(ctx Context, typ, entityID, command string, payload any, skipRootCheck bool) (string, error) {
	expectedRoot, err := s.currentRoot()
	if err != nil {
		return "", err
	}
	if !skipRootCheck {
		state, err := s.LoadState()
		if err != nil {
			return "", err
		}
		expectedRoot = state.Root
	}
	return s.appendEventAt(ctx, typ, entityID, command, payload, expectedRoot)
}

func (s *Store) appendEventAt(ctx Context, typ, entityID, command string, payload any, expectedRoot string) (string, error) {
	hash, err := s.db.AppendAt(jaybase.Context{Actor: ctx.Actor, Role: ctx.Role}, jaybase.AppendOptions{
		Type:      typ,
		EntityID:  entityID,
		Command:   command,
		Payload:   payload,
		CreatedAt: s.now().UTC(),
	}, expectedRoot)
	if err != nil {
		return "", storageError(err)
	}
	return hash, nil
}

func (s *Store) NodesFromRoot(root string) ([]Node, error) {
	nodes, err := s.db.NodesFromRoot(root)
	if err != nil {
		return nil, storageError(err)
	}
	return nodes, nil
}

func (s *Store) AuditLog() ([]Node, error) {
	nodes, err := s.db.AuditLog()
	if err != nil {
		return nil, storageError(err)
	}
	return nodes, nil
}

func (s *Store) LoadState() (State, error) {
	state, _, err := s.loadState()
	return state, err
}

func (s *Store) loadState() (State, bool, error) {
	root, err := s.currentRoot()
	if err != nil {
		return State{}, false, err
	}
	st := emptyState()
	nodes, err := s.NodesFromRoot(root)
	if err != nil {
		return State{}, false, err
	}
	initialized, err := s.replayNodes(&st, nodes)
	if err != nil {
		return State{}, false, err
	}
	return st, initialized, nil
}

func (s *Store) replayNodes(st *State, nodes []Node) (bool, error) {
	initialized := false
	for _, node := range nodes {
		isInitialization, err := s.applyNodeWithMetadata(st, node)
		if err != nil {
			return false, err
		}
		st.Root = node.Hash
		initialized = initialized || isInitialization
	}
	return initialized, nil
}

func (s *Store) applyNodeWithMetadata(st *State, node Node) (bool, error) {
	magpieOwned, err := classifyNodeType(node)
	if err != nil {
		return false, err
	}
	if !magpieOwned {
		return false, nil
	}

	var env eventEnvelope
	payload, err := s.nodePayload(node)
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return false, err
	}
	switch env.Kind {
	case "init":
		var ev initPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Roles = ev.Roles
		st.Users = ev.Users
		st.Settings = ev.Settings
		if st.Settings.AccountingBasis == "" {
			st.Settings = DefaultBookSettings()
		}
	case "role.upsert":
		var ev roleUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Roles[ev.Role.Name] = ev.Role
	case "user.upsert":
		var ev userUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Users[ev.User.ID] = ev.User
	case "account.create":
		var ev accountCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Accounts[ev.Account.ID] = ev.Account
	case "account.update":
		var ev accountUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.Accounts[ev.Account.ID]; !ok {
			return false, appErr(ErrValidation, "account.update references unknown account %s", ev.Account.ID)
		}
		st.Accounts[ev.Account.ID] = ev.Account
	case "journal.create":
		var ev journalCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if ev.Entry.AccountingBasis == "" {
			ev.Entry.AccountingBasis = st.effectiveSettings().AccountingBasis
		}
		if ev.Entry.Origin == "" {
			ev.Entry.Origin = JournalOriginManualAdjustment
		}
		if _, err := normalizeJournalOrigin(ev.Entry.Origin); err != nil {
			return false, err
		}
		st.JournalEntries[ev.Entry.ID] = ev.Entry
		if ev.SourceKey != "" {
			st.SourceKeys[ev.SourceKey] = ev.Entry.ID
		}
	case "period.close.complete":
		var ev periodClosePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if ev.Close.ID == "" || ev.Close.Through == "" || ev.Close.Revision < 1 || ev.Close.Manifest.SourceRoot == "" ||
			ev.Close.Manifest.CloseID != ev.Close.ID || ev.Close.Manifest.Through != ev.Close.Through || ev.Close.Manifest.Revision != ev.Close.Revision ||
			ev.Close.Manifest.PackageID == "" || ev.Close.Manifest.OriginalPackageID == "" {
			return false, appErr(ErrValidation, "invalid period close in node %s", node.Hash)
		}
		if len(node.Parents) != 1 || node.Parents[0] != ev.Close.Manifest.SourceRoot {
			return false, appErr(ErrIntegrity, "period close %s source root does not match its event parent", ev.Close.ID)
		}
		if _, exists := st.PeriodCloses[ev.Close.ID]; exists {
			return false, appErr(ErrValidation, "duplicate period close %s", ev.Close.ID)
		}
		for _, existing := range st.PeriodCloses {
			if existing.Through == ev.Close.Through && existing.Revision == ev.Close.Revision {
				return false, appErr(ErrIntegrity, "period close %s duplicates revision %d through %s", ev.Close.ID, ev.Close.Revision, ev.Close.Through)
			}
		}
		ev.Close.Root = node.Hash
		st.PeriodCloses[ev.Close.ID] = ev.Close
	case "period.reopen":
		var ev periodReopenPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, exists := st.PeriodCloses[ev.Reopen.CloseID]; !exists {
			return false, appErr(ErrValidation, "period reopen references unknown close %s", ev.Reopen.CloseID)
		}
		for _, existing := range st.PeriodReopens {
			if existing.ID == ev.Reopen.ID {
				return false, appErr(ErrValidation, "duplicate period reopen %s", ev.Reopen.ID)
			}
		}
		ev.Reopen.Root = node.Hash
		st.PeriodReopens = append(st.PeriodReopens, ev.Reopen)
	case "customer.upsert":
		var ev customerUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Customers[ev.Customer.ID] = ev.Customer
	case "invoice.create":
		var ev invoiceCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Invoices[ev.Invoice.ID] = ev.Invoice
	case "invoice.update":
		var ev invoiceUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.Invoices[ev.Invoice.ID]; !ok {
			return false, appErr(ErrValidation, "invoice.update references unknown invoice %s", ev.Invoice.ID)
		}
		st.Invoices[ev.Invoice.ID] = ev.Invoice
	case "payout.create":
		var ev payoutCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.Payouts[ev.Payout.ID]; ok {
			return false, appErr(ErrValidation, "payout.create references existing payout %s", ev.Payout.ID)
		}
		st.Payouts[ev.Payout.ID] = ev.Payout
	case "payout.update":
		var ev payoutUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.Payouts[ev.Payout.ID]; !ok {
			return false, appErr(ErrValidation, "payout.update references unknown payout %s", ev.Payout.ID)
		}
		st.Payouts[ev.Payout.ID] = ev.Payout
	case "bank.statement.create":
		var ev bankStatementPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.BankStatements[ev.Statement.ID]; ok {
			return false, appErr(ErrValidation, "bank.statement.create references existing statement %s", ev.Statement.ID)
		}
		st.BankStatements[ev.Statement.ID] = ev.Statement
	case "bank.statement.update":
		var ev bankStatementPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.BankStatements[ev.Statement.ID]; !ok {
			return false, appErr(ErrValidation, "bank.statement.update references unknown statement %s", ev.Statement.ID)
		}
		st.BankStatements[ev.Statement.ID] = ev.Statement
	case "bank.transaction.create":
		var ev bankTransactionPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.BankTransactions[ev.Transaction.ID]; ok {
			return false, appErr(ErrValidation, "bank.transaction.create references existing transaction %s", ev.Transaction.ID)
		}
		st.BankTransactions[ev.Transaction.ID] = ev.Transaction
	case "bank.transaction.update":
		var ev bankTransactionPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.BankTransactions[ev.Transaction.ID]; !ok {
			return false, appErr(ErrValidation, "bank.transaction.update references unknown transaction %s", ev.Transaction.ID)
		}
		st.BankTransactions[ev.Transaction.ID] = ev.Transaction
	case "bank.transfer.pair", "bank.transfer.reverse":
		var ev bankTransferPairPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, ok := st.BankTransactions[ev.From.ID]; !ok {
			return false, appErr(ErrValidation, "bank.transfer.pair references unknown transaction %s", ev.From.ID)
		}
		if _, ok := st.BankTransactions[ev.To.ID]; !ok {
			return false, appErr(ErrValidation, "bank.transfer.pair references unknown transaction %s", ev.To.ID)
		}
		st.BankTransactions[ev.From.ID] = ev.From
		st.BankTransactions[ev.To.ID] = ev.To
	case "settings.update":
		var ev settingsUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		if _, err := normalizeAccountingBasis(ev.Settings.AccountingBasis); err != nil {
			return false, err
		}
		st.Settings = ev.Settings
		if st.Settings.ModifiedCashPolicy == (ModifiedCashPolicy{}) {
			st.Settings.ModifiedCashPolicy = DefaultModifiedCashPolicy()
		}
	case "note.upsert":
		var ev noteUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return false, err
		}
		st.Notes[ev.Note.ID] = ev.Note
	default:
		return false, appErr(ErrValidation, "unknown event kind %q in node %s", env.Kind, node.Hash)
	}
	return node.Type == "store.init" && env.Kind == "init", nil
}

func classifyNodeType(node Node) (bool, error) {
	if _, ok := legacyMagpieNodeTypes[node.Type]; ok {
		return true, nil
	}
	for _, prefix := range foreignApplicationPrefixes {
		if strings.HasPrefix(node.Type, prefix) {
			return false, nil
		}
	}
	return false, appErr(ErrValidation, "unknown event type %q in node %s", node.Type, node.Hash)
}

func (s *Store) nodePayload(node Node) ([]byte, error) {
	payload, err := s.db.NodePayload(node)
	if err != nil {
		return nil, storageError(err)
	}
	return payload, nil
}

func wrapEvent(kind string, data any) eventEnvelope {
	raw, _ := json.Marshal(data)
	return eventEnvelope{Kind: kind, Data: raw}
}

func makeID(prefix string, parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return fmt.Sprintf("%s:%s", prefix, hex.EncodeToString(h.Sum(nil))[:24])
}

func sortedPermissions(perms []Permission) []Permission {
	out := append([]Permission(nil), perms...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func storageError(err error) error {
	if err == nil {
		return nil
	}
	var dbErr *jaybase.AppError
	if errors.As(err, &dbErr) {
		return appErr(ErrorCode(dbErr.Code), "%s", dbErr.Message)
	}
	return err
}

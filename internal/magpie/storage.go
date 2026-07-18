package magpie

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
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
	WriteNamedRef(string, string) error
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

func (b *localStorageBackend) WriteNamedRef(name, root string) error {
	return b.store.WriteNamedRef(name, root)
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

func (s *Store) WriteInitialRoot(ctx Context) (string, error) {
	root, err := s.currentRoot()
	if err != nil {
		return "", err
	}
	if root != "" {
		return root, nil
	}
	event := initEvent()
	return s.appendEventAt(ctx, "store.init", "", "store init", event, root)
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
	root, err := s.currentRoot()
	if err != nil {
		return State{}, err
	}
	st := emptyState()
	nodes, err := s.NodesFromRoot(root)
	if err != nil {
		return State{}, err
	}
	for _, node := range nodes {
		if err := s.applyNode(&st, node); err != nil {
			return State{}, err
		}
		st.Root = node.Hash
	}
	return st, nil
}

func (s *Store) applyNode(st *State, node Node) error {
	var env eventEnvelope
	payload, err := s.nodePayload(node)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return err
	}
	switch env.Kind {
	case "init":
		var ev initPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
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
			return err
		}
		st.Roles[ev.Role.Name] = ev.Role
	case "user.upsert":
		var ev userUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Users[ev.User.ID] = ev.User
	case "account.create":
		var ev accountCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Accounts[ev.Account.ID] = ev.Account
	case "account.update":
		var ev accountUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, ok := st.Accounts[ev.Account.ID]; !ok {
			return appErr(ErrValidation, "account.update references unknown account %s", ev.Account.ID)
		}
		st.Accounts[ev.Account.ID] = ev.Account
	case "journal.create":
		var ev journalCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if ev.Entry.AccountingBasis == "" {
			ev.Entry.AccountingBasis = st.effectiveSettings().AccountingBasis
		}
		if ev.Entry.Origin == "" {
			ev.Entry.Origin = JournalOriginManualAdjustment
		}
		if _, err := normalizeJournalOrigin(ev.Entry.Origin); err != nil {
			return err
		}
		st.JournalEntries[ev.Entry.ID] = ev.Entry
		if ev.SourceKey != "" {
			st.SourceKeys[ev.SourceKey] = ev.Entry.ID
		}
	case "customer.upsert":
		var ev customerUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Customers[ev.Customer.ID] = ev.Customer
	case "invoice.create":
		var ev invoiceCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Invoices[ev.Invoice.ID] = ev.Invoice
	case "invoice.update":
		var ev invoiceUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, ok := st.Invoices[ev.Invoice.ID]; !ok {
			return appErr(ErrValidation, "invoice.update references unknown invoice %s", ev.Invoice.ID)
		}
		st.Invoices[ev.Invoice.ID] = ev.Invoice
	case "payout.create":
		var ev payoutCreatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, ok := st.Payouts[ev.Payout.ID]; ok {
			return appErr(ErrValidation, "payout.create references existing payout %s", ev.Payout.ID)
		}
		st.Payouts[ev.Payout.ID] = ev.Payout
	case "payout.update":
		var ev payoutUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, ok := st.Payouts[ev.Payout.ID]; !ok {
			return appErr(ErrValidation, "payout.update references unknown payout %s", ev.Payout.ID)
		}
		st.Payouts[ev.Payout.ID] = ev.Payout
	case "settings.update":
		var ev settingsUpdatePayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		if _, err := normalizeAccountingBasis(ev.Settings.AccountingBasis); err != nil {
			return err
		}
		st.Settings = ev.Settings
		if st.Settings.ModifiedCashPolicy == (ModifiedCashPolicy{}) {
			st.Settings.ModifiedCashPolicy = DefaultModifiedCashPolicy()
		}
	case "note.upsert":
		var ev noteUpsertPayload
		if err := json.Unmarshal(env.Data, &ev); err != nil {
			return err
		}
		st.Notes[ev.Note.ID] = ev.Note
	default:
		return appErr(ErrValidation, "unknown event kind %q in node %s", env.Kind, node.Hash)
	}
	return nil
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

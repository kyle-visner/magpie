package infobase

import (
	"strconv"
	"strings"
	"time"
)

type accountCreatePayload struct {
	Account Account `json:"account"`
}

type journalCreatePayload struct {
	Entry     JournalEntry `json:"entry"`
	ImportKey string       `json:"import_key,omitempty"`
}

func (s *Store) CreateAccount(ctx Context, name string, typ AccountType, sensitivity string) (Account, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return Account{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return Account{}, "", err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Account{}, "", appErr(ErrValidation, "account name is required")
	}
	if err := validateAccountType(typ); err != nil {
		return Account{}, "", err
	}
	id := makeID("acct", strings.ToLower(name), string(typ))
	if _, exists := st.Accounts[id]; exists {
		return Account{}, "", appErr(ErrConflict, "account already exists: %s", id)
	}
	if sensitivity == "" {
		sensitivity = "internal"
	}
	now := s.now().UTC()
	acct := Account{ID: id, Name: name, Type: typ, Sensitivity: sensitivity, CreatedAt: now, CreatedBy: ctx.Actor}
	hash, err := s.appendEvent(ctx, "ledger.account", id, "ledger account create", wrapEvent("account.create", accountCreatePayload{Account: acct}), true)
	return acct, hash, err
}

func validateAccountType(typ AccountType) error {
	switch typ {
	case AccountAsset, AccountLiability, AccountEquity, AccountRevenue, AccountExpense:
		return nil
	default:
		return appErr(ErrValidation, "invalid account type %q", typ)
	}
}

func (s *Store) CreateJournalEntry(ctx Context, entry JournalEntry) (JournalEntry, string, error) {
	return s.createJournalEntry(ctx, entry, "")
}

func (s *Store) createJournalEntry(ctx Context, entry JournalEntry, importKey string) (JournalEntry, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return JournalEntry{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return JournalEntry{}, "", err
	}
	if importKey != "" {
		if existing, ok := st.ImportKeys[importKey]; ok {
			return st.JournalEntries[existing], st.Root, nil
		}
	}
	entry.Memo = strings.TrimSpace(entry.Memo)
	if entry.Date == "" {
		entry.Date = s.now().UTC().Format("2006-01-02")
	}
	if _, err := time.Parse("2006-01-02", entry.Date); err != nil {
		return JournalEntry{}, "", appErr(ErrValidation, "date must use YYYY-MM-DD")
	}
	if len(entry.Postings) < 2 {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry requires at least two postings")
	}
	var debit, credit int64
	for i, p := range entry.Postings {
		if p.AccountID == "" {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d missing account_id", i)
		}
		if _, ok := st.Accounts[p.AccountID]; !ok {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d references unknown account %s", i, p.AccountID)
		}
		if p.Debit < 0 || p.Credit < 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d has negative amount", i)
		}
		if p.Debit > 0 && p.Credit > 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d cannot have both debit and credit", i)
		}
		if p.Debit == 0 && p.Credit == 0 {
			return JournalEntry{}, "", appErr(ErrValidation, "posting %d must have a debit or credit", i)
		}
		debit += p.Debit
		credit += p.Credit
	}
	if debit != credit {
		return JournalEntry{}, "", appErr(ErrValidation, "journal entry must balance: debit=%d credit=%d", debit, credit)
	}
	if entry.ID == "" {
		entry.ID = makeID("jrnl", entry.Date, entry.Memo, postingFingerprint(entry.Postings), importKey)
	}
	if _, exists := st.JournalEntries[entry.ID]; exists {
		return JournalEntry{}, "", appErr(ErrConflict, "journal entry already exists: %s", entry.ID)
	}
	entry.CreatedAt = s.now().UTC()
	entry.CreatedBy = ctx.Actor
	hash, err := s.appendEvent(ctx, "ledger.journal", entry.ID, "ledger journal create", wrapEvent("journal.create", journalCreatePayload{Entry: entry, ImportKey: importKey}), true)
	return entry, hash, err
}

func postingFingerprint(postings []Posting) string {
	var b strings.Builder
	for _, p := range postings {
		b.WriteString(p.AccountID)
		b.WriteByte(':')
		b.WriteString(int64String(p.Debit))
		b.WriteByte(':')
		b.WriteString(int64String(p.Credit))
		b.WriteByte(';')
	}
	return b.String()
}

func int64String(v int64) string {
	return strconv.FormatInt(v, 10)
}

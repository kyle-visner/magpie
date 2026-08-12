package magpie

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ReportProvenance identifies the immutable ledger view and parameters used to
// build a report. Amounts are always integer cents.
type ReportProvenance struct {
	Root            string          `json:"jaybase_root"`
	AccountingBasis AccountingBasis `json:"accounting_basis"`
	Report          string          `json:"report"`
	From            string          `json:"from,omitempty"`
	Through         string          `json:"through,omitempty"`
	AsOf            string          `json:"as_of,omitempty"`
}

type AccountBalance struct {
	AccountID   string      `json:"account_id"`
	Number      string      `json:"number,omitempty"`
	Name        string      `json:"name"`
	AccountType AccountType `json:"account_type"`
	DebitCents  int64       `json:"debit_cents"`
	CreditCents int64       `json:"credit_cents"`
}

type TrialBalanceReport struct {
	Provenance       ReportProvenance `json:"provenance"`
	Accounts         []AccountBalance `json:"accounts"`
	TotalDebitCents  int64            `json:"total_debit_cents"`
	TotalCreditCents int64            `json:"total_credit_cents"`
}

type ProfitLossReport struct {
	Provenance        ReportProvenance `json:"provenance"`
	Revenue           []AccountBalance `json:"revenue"`
	Expenses          []AccountBalance `json:"expenses"`
	TotalRevenueCents int64            `json:"total_revenue_cents"`
	TotalExpenseCents int64            `json:"total_expense_cents"`
	NetIncomeCents    int64            `json:"net_income_cents"`
}

type BalanceSheetReport struct {
	Provenance             ReportProvenance `json:"provenance"`
	Assets                 []AccountBalance `json:"assets"`
	Liabilities            []AccountBalance `json:"liabilities"`
	Equity                 []AccountBalance `json:"equity"`
	TotalAssetsCents       int64            `json:"total_assets_cents"`
	TotalLiabilitiesCents  int64            `json:"total_liabilities_cents"`
	ExplicitEquityCents    int64            `json:"explicit_equity_cents"`
	CurrentEarningsCents   int64            `json:"current_earnings_cents"`
	TotalEquityCents       int64            `json:"total_equity_cents"`
	LiabilitiesEquityCents int64            `json:"liabilities_and_equity_cents"`
}

type GeneralLedgerLine struct {
	AccountID           string `json:"account_id"`
	AccountNumber       string `json:"account_number,omitempty"`
	AccountName         string `json:"account_name"`
	Date                string `json:"date"`
	JournalEntryID      string `json:"journal_entry_id"`
	Memo                string `json:"memo"`
	PostingMemo         string `json:"posting_memo,omitempty"`
	DebitCents          int64  `json:"debit_cents"`
	CreditCents         int64  `json:"credit_cents"`
	RunningBalanceCents int64  `json:"running_balance_cents"`
}

type GeneralLedgerReport struct {
	Provenance ReportProvenance    `json:"provenance"`
	Lines      []GeneralLedgerLine `json:"lines"`
}

func (s *Store) TrialBalance(ctx Context, asOf string) (TrialBalanceReport, error) {
	st, err := s.reportState(ctx)
	if err != nil {
		return TrialBalanceReport{}, err
	}
	return trialBalance(st, asOf)
}

func (s *Store) ProfitLoss(ctx Context, from, through string) (ProfitLossReport, error) {
	st, err := s.reportState(ctx)
	if err != nil {
		return ProfitLossReport{}, err
	}
	return profitLoss(st, from, through)
}

func (s *Store) BalanceSheet(ctx Context, asOf string) (BalanceSheetReport, error) {
	st, err := s.reportState(ctx)
	if err != nil {
		return BalanceSheetReport{}, err
	}
	return balanceSheet(st, asOf)
}

func (s *Store) GeneralLedger(ctx Context, from, through string) (GeneralLedgerReport, error) {
	st, err := s.reportState(ctx)
	if err != nil {
		return GeneralLedgerReport{}, err
	}
	return generalLedger(st, from, through)
}

func (s *Store) reportState(ctx Context) (State, error) {
	st, err := s.LoadState()
	if err != nil {
		return State{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return State{}, err
	}
	return st, nil
}

func (s *Store) stateAtRoot(root string) (State, error) {
	st := emptyState()
	nodes, err := s.NodesFromRoot(root)
	if err != nil {
		return State{}, err
	}
	if _, err := s.replayNodes(&st, nodes); err != nil {
		return State{}, err
	}
	if root != st.Root {
		return State{}, appErr(ErrIntegrity, "requested report root %s replayed to %s", root, st.Root)
	}
	return st, nil
}

func trialBalance(st State, asOf string) (TrialBalanceReport, error) {
	if err := validDate("as_of", asOf); err != nil {
		return TrialBalanceReport{}, err
	}
	report := TrialBalanceReport{Provenance: provenance(st, "trial_balance", "", "", asOf)}
	balances, err := accountBalances(st, "", asOf)
	if err != nil {
		return report, err
	}
	for _, account := range sortedAccounts(st) {
		debit, credit := normalBalance(account.Type, balances[account.ID])
		row := AccountBalance{AccountID: account.ID, Number: account.Number, Name: account.Name, AccountType: account.Type, DebitCents: debit, CreditCents: credit}
		report.Accounts = append(report.Accounts, row)
		report.TotalDebitCents, err = checkedAddCents(report.TotalDebitCents, debit, "trial balance debit total")
		if err != nil {
			return TrialBalanceReport{}, err
		}
		report.TotalCreditCents, err = checkedAddCents(report.TotalCreditCents, credit, "trial balance credit total")
		if err != nil {
			return TrialBalanceReport{}, err
		}
	}
	if report.TotalDebitCents != report.TotalCreditCents {
		return TrialBalanceReport{}, appErr(ErrIntegrity, "trial balance is out of balance: debit=%d credit=%d", report.TotalDebitCents, report.TotalCreditCents)
	}
	return report, nil
}

func profitLoss(st State, from, through string) (ProfitLossReport, error) {
	if err := validRange(from, through); err != nil {
		return ProfitLossReport{}, err
	}
	report := ProfitLossReport{Provenance: provenance(st, "profit_loss", from, through, "")}
	balances, err := accountBalances(st, from, through)
	if err != nil {
		return report, err
	}
	for _, account := range sortedAccounts(st) {
		balance := balances[account.ID]
		debit, credit := normalBalance(account.Type, balance)
		row := AccountBalance{AccountID: account.ID, Number: account.Number, Name: account.Name, AccountType: account.Type, DebitCents: debit, CreditCents: credit}
		switch account.Type {
		case AccountRevenue:
			report.Revenue = append(report.Revenue, row)
			report.TotalRevenueCents, err = checkedAddCents(report.TotalRevenueCents, balance.credit-balance.debit, "profit and loss revenue total")
		case AccountExpense:
			report.Expenses = append(report.Expenses, row)
			report.TotalExpenseCents, err = checkedAddCents(report.TotalExpenseCents, balance.debit-balance.credit, "profit and loss expense total")
		}
		if err != nil {
			return ProfitLossReport{}, err
		}
	}
	report.NetIncomeCents, err = checkedSubtractCents(report.TotalRevenueCents, report.TotalExpenseCents, "profit and loss net income")
	if err != nil {
		return ProfitLossReport{}, err
	}
	return report, nil
}

func balanceSheet(st State, asOf string) (BalanceSheetReport, error) {
	if err := validDate("as_of", asOf); err != nil {
		return BalanceSheetReport{}, err
	}
	report := BalanceSheetReport{Provenance: provenance(st, "balance_sheet", "", "", asOf)}
	balances, err := accountBalances(st, "", asOf)
	if err != nil {
		return report, err
	}
	for _, account := range sortedAccounts(st) {
		balance := balances[account.ID]
		debit, credit := normalBalance(account.Type, balance)
		row := AccountBalance{AccountID: account.ID, Number: account.Number, Name: account.Name, AccountType: account.Type, DebitCents: debit, CreditCents: credit}
		switch account.Type {
		case AccountAsset:
			report.Assets = append(report.Assets, row)
			report.TotalAssetsCents, err = checkedAddCents(report.TotalAssetsCents, balance.debit-balance.credit, "balance sheet asset total")
		case AccountLiability:
			report.Liabilities = append(report.Liabilities, row)
			report.TotalLiabilitiesCents, err = checkedAddCents(report.TotalLiabilitiesCents, balance.credit-balance.debit, "balance sheet liability total")
		case AccountEquity:
			report.Equity = append(report.Equity, row)
			report.ExplicitEquityCents, err = checkedAddCents(report.ExplicitEquityCents, balance.credit-balance.debit, "balance sheet explicit equity total")
		case AccountRevenue:
			report.CurrentEarningsCents, err = checkedAddCents(report.CurrentEarningsCents, balance.credit-balance.debit, "balance sheet current earnings")
		case AccountExpense:
			report.CurrentEarningsCents, err = checkedAddCents(report.CurrentEarningsCents, balance.credit-balance.debit, "balance sheet current earnings")
		}
		if err != nil {
			return BalanceSheetReport{}, err
		}
	}
	report.TotalEquityCents, err = checkedAddCents(report.ExplicitEquityCents, report.CurrentEarningsCents, "balance sheet equity total")
	if err != nil {
		return BalanceSheetReport{}, err
	}
	report.LiabilitiesEquityCents, err = checkedAddCents(report.TotalLiabilitiesCents, report.TotalEquityCents, "balance sheet liabilities and equity total")
	if err != nil {
		return BalanceSheetReport{}, err
	}
	if report.TotalAssetsCents != report.LiabilitiesEquityCents {
		return BalanceSheetReport{}, appErr(ErrIntegrity, "balance sheet is out of balance: assets=%d liabilities_and_equity=%d", report.TotalAssetsCents, report.LiabilitiesEquityCents)
	}
	return report, nil
}

func generalLedger(st State, from, through string) (GeneralLedgerReport, error) {
	if err := validRange(from, through); err != nil {
		return GeneralLedgerReport{}, err
	}
	report := GeneralLedgerReport{Provenance: provenance(st, "general_ledger", from, through, "")}
	entries := sortedJournals(st)
	for _, account := range sortedAccounts(st) {
		var running int64
		for _, entry := range entries {
			for postingIndex, posting := range entry.Postings {
				if posting.AccountID != account.ID {
					continue
				}
				delta := posting.Debit - posting.Credit
				if isCreditNormal(account.Type) {
					delta = -delta
				}
				var err error
				running, err = checkedAddCents(running, delta, "general ledger running balance")
				if err != nil {
					return GeneralLedgerReport{}, err
				}
				if entry.Date < from || entry.Date > through {
					continue
				}
				report.Lines = append(report.Lines, GeneralLedgerLine{
					AccountID: account.ID, AccountNumber: account.Number, AccountName: account.Name,
					Date: entry.Date, JournalEntryID: entry.ID, Memo: entry.Memo, PostingMemo: posting.Memo,
					DebitCents: posting.Debit, CreditCents: posting.Credit, RunningBalanceCents: running,
				})
				_ = postingIndex // posting order is the stable tie breaker inside a journal.
			}
		}
	}
	return report, nil
}

type rawBalance struct{ debit, credit int64 }

func accountBalances(st State, from, through string) (map[string]rawBalance, error) {
	out := map[string]rawBalance{}
	for _, entry := range st.JournalEntries {
		if (from != "" && entry.Date < from) || entry.Date > through {
			continue
		}
		for _, posting := range entry.Postings {
			balance := out[posting.AccountID]
			var err error
			balance.debit, err = checkedAddCents(balance.debit, posting.Debit, "report account debit")
			if err != nil {
				return nil, err
			}
			balance.credit, err = checkedAddCents(balance.credit, posting.Credit, "report account credit")
			if err != nil {
				return nil, err
			}
			out[posting.AccountID] = balance
		}
	}
	return out, nil
}

func normalBalance(typ AccountType, balance rawBalance) (int64, int64) {
	if isCreditNormal(typ) {
		net := balance.credit - balance.debit
		if net >= 0 {
			return 0, net
		}
		return -net, 0
	}
	net := balance.debit - balance.credit
	if net >= 0 {
		return net, 0
	}
	return 0, -net
}

func isCreditNormal(typ AccountType) bool {
	return typ == AccountLiability || typ == AccountEquity || typ == AccountRevenue
}

func sortedAccounts(st State) []Account {
	out := make([]Account, 0, len(st.Accounts))
	for _, account := range st.Accounts {
		out = append(out, account)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Number != out[j].Number {
			return out[i].Number < out[j].Number
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sortedJournals(st State) []JournalEntry {
	out := make([]JournalEntry, 0, len(st.JournalEntries))
	for _, entry := range st.JournalEntries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Date != out[j].Date {
			return out[i].Date < out[j].Date
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func provenance(st State, report, from, through, asOf string) ReportProvenance {
	return ReportProvenance{Root: st.Root, AccountingBasis: st.effectiveSettings().AccountingBasis, Report: report, From: from, Through: through, AsOf: asOf}
}

func validDate(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return appErr(ErrValidation, "%s is required", name)
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return appErr(ErrValidation, "%s must use YYYY-MM-DD", name)
	}
	return nil
}

func validRange(from, through string) error {
	if err := validDate("from", from); err != nil {
		return err
	}
	if err := validDate("through", through); err != nil {
		return err
	}
	if from > through {
		return appErr(ErrValidation, "from must be on or before through")
	}
	return nil
}

func CanonicalJSON(report any) ([]byte, error) { return json.Marshal(report) }

func ReportCSV(report any) ([]byte, error) {
	var buf bytes.Buffer
	write := func(row ...string) {
		for i, field := range row {
			if i > 0 {
				buf.WriteByte(',')
			}
			if strings.ContainsAny(field, ",\"\r\n") {
				buf.WriteByte('"')
				buf.WriteString(strings.ReplaceAll(field, "\"", "\"\""))
				buf.WriteByte('"')
			} else {
				buf.WriteString(field)
			}
		}
		buf.WriteByte('\n')
	}
	switch r := report.(type) {
	case TrialBalanceReport:
		write("account_id", "number", "name", "account_type", "debit_cents", "credit_cents")
		for _, row := range r.Accounts {
			write(row.AccountID, row.Number, row.Name, string(row.AccountType), cents(row.DebitCents), cents(row.CreditCents))
		}
		write("TOTAL", "", "", "", cents(r.TotalDebitCents), cents(r.TotalCreditCents))
	case ProfitLossReport:
		write("section", "account_id", "number", "name", "debit_cents", "credit_cents")
		for _, row := range r.Revenue {
			write("revenue", row.AccountID, row.Number, row.Name, cents(row.DebitCents), cents(row.CreditCents))
		}
		for _, row := range r.Expenses {
			write("expense", row.AccountID, row.Number, row.Name, cents(row.DebitCents), cents(row.CreditCents))
		}
		write("TOTAL", "", "", "net_income", "", cents(r.NetIncomeCents))
	case BalanceSheetReport:
		write("section", "account_id", "number", "name", "debit_cents", "credit_cents")
		for _, row := range r.Assets {
			write("asset", row.AccountID, row.Number, row.Name, cents(row.DebitCents), cents(row.CreditCents))
		}
		for _, row := range r.Liabilities {
			write("liability", row.AccountID, row.Number, row.Name, cents(row.DebitCents), cents(row.CreditCents))
		}
		for _, row := range r.Equity {
			write("equity", row.AccountID, row.Number, row.Name, cents(row.DebitCents), cents(row.CreditCents))
		}
		write("current_earnings", "", "", "Current earnings", "", cents(r.CurrentEarningsCents))
		write("TOTAL", "", "", "assets", cents(r.TotalAssetsCents), "")
		write("TOTAL", "", "", "liabilities_and_equity", "", cents(r.LiabilitiesEquityCents))
	case GeneralLedgerReport:
		write("account_id", "account_number", "account_name", "date", "journal_entry_id", "memo", "posting_memo", "debit_cents", "credit_cents", "running_balance_cents")
		for _, row := range r.Lines {
			write(row.AccountID, row.AccountNumber, row.AccountName, row.Date, row.JournalEntryID, row.Memo, row.PostingMemo, cents(row.DebitCents), cents(row.CreditCents), cents(row.RunningBalanceCents))
		}
	default:
		return nil, fmt.Errorf("unsupported report type %T", report)
	}
	return buf.Bytes(), nil
}

func cents(value int64) string { return strconv.FormatInt(value, 10) }

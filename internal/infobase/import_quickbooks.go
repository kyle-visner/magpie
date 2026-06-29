package infobase

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

type ImportReport struct {
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Entries  []string `json:"entries"`
	Root     string   `json:"root"`
}

func (s *Store) ImportQuickBooksCSV(ctx Context, path string, cashAccountID string) (ImportReport, error) {
	st, err := s.LoadState()
	if err != nil {
		return ImportReport{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionImportQB); err != nil {
		return ImportReport{}, err
	}
	if _, ok := st.Accounts[cashAccountID]; !ok {
		return ImportReport{}, appErr(ErrValidation, "cash account %s does not exist", cashAccountID)
	}
	f, err := os.Open(path)
	if err != nil {
		return ImportReport{}, err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.TrimLeadingSpace = true
	headers, err := r.Read()
	if err != nil {
		return ImportReport{}, err
	}
	index := map[string]int{}
	for i, h := range headers {
		index[normalizeHeader(h)] = i
	}
	required := []string{"date", "memo", "account", "amount_cents"}
	for _, h := range required {
		if _, ok := index[h]; !ok {
			return ImportReport{}, appErr(ErrValidation, "quickbooks csv missing required column %q", h)
		}
	}
	report := ImportReport{}
	for {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ImportReport{}, err
		}
		date := cell(row, index, "date")
		memo := cell(row, index, "memo")
		acct := cell(row, index, "account")
		sourceID := cell(row, index, "source_id")
		amount, err := strconv.ParseInt(strings.TrimSpace(cell(row, index, "amount_cents")), 10, 64)
		if err != nil {
			return ImportReport{}, appErr(ErrValidation, "invalid amount_cents for row %q", memo)
		}
		target, ok := st.Accounts[acct]
		if !ok {
			return ImportReport{}, appErr(ErrValidation, "row %q references unknown mapped account %s", memo, acct)
		}
		importKey := "quickbooks:" + sourceID
		if sourceID == "" {
			importKey = "quickbooks:" + makeID("row", date, memo, acct, strconv.FormatInt(amount, 10))
		}
		if _, exists := st.ImportKeys[importKey]; exists {
			report.Skipped++
			continue
		}
		entry := JournalEntry{Date: date, Memo: memo, Source: "quickbooks_csv", SourceKey: sourceID}
		if amount >= 0 {
			entry.Postings = []Posting{
				{AccountID: cashAccountID, Debit: amount},
				{AccountID: target.ID, Credit: amount},
			}
		} else {
			amount = -amount
			entry.Postings = []Posting{
				{AccountID: target.ID, Debit: amount},
				{AccountID: cashAccountID, Credit: amount},
			}
		}
		created, _, err := s.createJournalEntry(ctx, entry, importKey)
		if err != nil {
			return ImportReport{}, err
		}
		report.Imported++
		report.Entries = append(report.Entries, created.ID)
		st, err = s.LoadState()
		if err != nil {
			return ImportReport{}, err
		}
	}
	report.Root = st.Root
	return report, nil
}

func normalizeHeader(h string) string {
	return strings.ToLower(strings.TrimSpace(strings.ReplaceAll(h, " ", "_")))
}

func cell(row []string, index map[string]int, name string) string {
	i, ok := index[name]
	if !ok || i >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[i])
}

func QuickBooksCSVTemplate(w io.Writer) error {
	_, err := fmt.Fprintln(w, "date,memo,account,amount_cents,source_id")
	return err
}

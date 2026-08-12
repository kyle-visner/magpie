package magpie

import (
	"encoding/hex"
	"math"
	"sort"
	"strings"
	"time"
)

type bankStatementPayload struct {
	Statement BankStatement `json:"statement"`
}

type bankTransactionPayload struct {
	Transaction BankTransaction `json:"transaction"`
}

type bankTransferPairPayload struct {
	From BankTransaction `json:"from_transaction"`
	To   BankTransaction `json:"to_transaction"`
}

func (s *Store) ImportBankStatement(ctx Context, statement BankStatement) (BankStatement, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return BankStatement{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return BankStatement{}, "", err
	}
	statement, err = normalizeBankStatement(st, statement)
	if err != nil {
		return BankStatement{}, "", err
	}
	if existingID, ok := bankStatementIDByExternalRefs(st, statement.ExternalRefs); ok {
		existing := st.BankStatements[existingID]
		if !bankStatementsEquivalent(existing, statement) {
			return BankStatement{}, "", appErr(ErrConflict, "bank statement external reference already belongs to statement %s with different details", existingID)
		}
		return existing, st.Root, nil
	}
	if existing, ok := st.BankStatements[statement.ID]; ok {
		if !bankStatementsEquivalent(existing, statement) {
			return BankStatement{}, "", appErr(ErrConflict, "bank statement %s already exists with different details", statement.ID)
		}
		return existing, st.Root, nil
	}
	statement.Status = ReconciliationOpen
	statement.CreatedAt = s.now().UTC()
	statement.CreatedBy = ctx.Actor
	root, err := s.appendEventAt(ctx, "bank.statement", statement.ID, "bank statement import", wrapEvent("bank.statement.create", bankStatementPayload{Statement: statement}), st.Root)
	return statement, root, err
}

func (s *Store) ImportBankTransaction(ctx Context, transaction BankTransaction) (BankTransaction, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return BankTransaction{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return BankTransaction{}, "", err
	}
	transaction, err = normalizeBankTransaction(st, transaction)
	if err != nil {
		return BankTransaction{}, "", err
	}
	if existingID, ok := bankTransactionIDByExternalRefs(st, transaction.ExternalRefs); ok {
		existing := st.BankTransactions[existingID]
		if !bankTransactionsEquivalent(existing, transaction) {
			return BankTransaction{}, "", appErr(ErrConflict, "bank transaction external reference already belongs to transaction %s with different details", existingID)
		}
		return existing, st.Root, nil
	}
	if existing, ok := st.BankTransactions[transaction.ID]; ok {
		if !bankTransactionsEquivalent(existing, transaction) {
			return BankTransaction{}, "", appErr(ErrConflict, "bank transaction %s already exists with different details", transaction.ID)
		}
		return existing, st.Root, nil
	}
	now := s.now().UTC()
	transaction.Status = BankTransactionStaged
	transaction.CreatedAt = now
	transaction.UpdatedAt = now
	transaction.CreatedBy = ctx.Actor
	transaction.UpdatedBy = ctx.Actor
	root, err := s.appendEventAt(ctx, "bank.transaction", transaction.ID, "bank transaction import", wrapEvent("bank.transaction.create", bankTransactionPayload{Transaction: transaction}), st.Root)
	return transaction, root, err
}

func (s *Store) PostBankTransaction(ctx Context, transactionID, classificationAccountID string) (BankTransaction, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return BankTransaction{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return BankTransaction{}, "", err
	}
	transaction, err := bankTransactionForWrite(st, transactionID)
	if err != nil {
		return BankTransaction{}, "", err
	}
	classificationAccountID = strings.TrimSpace(classificationAccountID)
	if transaction.Status == BankTransactionPosted && transaction.ClassificationAccount == classificationAccountID {
		return transaction, st.Root, nil
	}
	if transaction.Status != BankTransactionStaged {
		return BankTransaction{}, "", appErr(ErrConflict, "bank transaction %s is %s and cannot be posted", transaction.ID, transaction.Status)
	}
	if transaction.Pending {
		return BankTransaction{}, "", appErr(ErrValidation, "pending bank transaction %s cannot be posted", transaction.ID)
	}
	counter, err := validateBankClassification(st, transaction, classificationAccountID)
	if err != nil {
		return BankTransaction{}, "", err
	}
	bankAccount := st.Accounts[transaction.AccountID]
	bankPosting, counterPosting, err := transactionPostings(bankAccount, counter, transaction.AmountCents)
	if err != nil {
		return BankTransaction{}, "", err
	}
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               transaction.Date,
		Memo:               "Bank transaction " + transaction.ID,
		Workflow:           "bank.transaction.post",
		PostingSemantics:   bankPostingSemantics(bankPosting),
		SourceDocumentType: "bank_transaction",
		SourceDocumentID:   transaction.ID,
		Source:             "bank_transaction",
		SourceKey:          transaction.ID + ":post",
		Postings:           []Posting{bankPosting, counterPosting},
		Metadata: map[string]string{
			"bank_transaction_id": transaction.ID,
			"statement_id":        transaction.StatementID,
		},
	})
	if err != nil {
		return BankTransaction{}, "", err
	}
	transaction.Status = BankTransactionPosted
	transaction.ClassificationAccount = counter.ID
	transaction.JournalEntryIDs = []string{entry.ID}
	transaction.UpdatedAt = s.now().UTC()
	transaction.UpdatedBy = ctx.Actor
	root, err = s.appendEventAt(ctx, "bank.transaction", transaction.ID, "bank transaction post", wrapEvent("bank.transaction.update", bankTransactionPayload{Transaction: transaction}), root)
	return transaction, root, err
}

func (s *Store) ReverseBankTransaction(ctx Context, transactionID, reason, date string) (BankTransaction, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return BankTransaction{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return BankTransaction{}, "", err
	}
	transaction, err := bankTransactionForWrite(st, transactionID)
	if err != nil {
		return BankTransaction{}, "", err
	}
	reason = strings.TrimSpace(reason)
	if transaction.Status == BankTransactionReversed {
		if transaction.ReversalReason != reason {
			return BankTransaction{}, "", appErr(ErrConflict, "bank transaction %s was already reversed for a different reason", transaction.ID)
		}
		return transaction, st.Root, nil
	}
	if transaction.Status != BankTransactionPosted {
		return BankTransaction{}, "", appErr(ErrConflict, "only a posted bank transaction can be reversed")
	}
	if reason == "" {
		return BankTransaction{}, "", appErr(ErrValidation, "bank transaction reversal reason is required")
	}
	date, err = normalizeBankDate(date, s.now().UTC().Format("2006-01-02"), "reversal date")
	if err != nil {
		return BankTransaction{}, "", err
	}
	root := st.Root
	originalJournalIDs := append([]string(nil), transaction.JournalEntryIDs...)
	for _, journalID := range originalJournalIDs {
		original, ok := st.JournalEntries[journalID]
		if !ok {
			return BankTransaction{}, "", appErr(ErrValidation, "bank transaction journal entry %s not found", journalID)
		}
		entry, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
			Date:               date,
			Memo:               "Bank transaction reversed " + transaction.ID,
			Workflow:           "bank.transaction.reverse",
			PostingSemantics:   "bank_transaction_reversed",
			SourceDocumentType: "bank_transaction",
			SourceDocumentID:   transaction.ID,
			Source:             "bank_transaction",
			SourceKey:          transaction.ID + ":reverse:" + journalID,
			Postings:           reversePostings(original.Postings),
			Metadata: map[string]string{
				"original_journal_entry_id": journalID,
				"reason":                    reason,
			},
		})
		if err != nil {
			return BankTransaction{}, "", err
		}
		root = newRoot
		transaction.JournalEntryIDs = append(transaction.JournalEntryIDs, entry.ID)
	}
	transaction.Status = BankTransactionReversed
	transaction.ReversalReason = reason
	transaction.UpdatedAt = s.now().UTC()
	transaction.UpdatedBy = ctx.Actor
	root, err = s.appendEventAt(ctx, "bank.transaction", transaction.ID, "bank transaction reverse", wrapEvent("bank.transaction.update", bankTransactionPayload{Transaction: transaction}), root)
	return transaction, root, err
}

func (s *Store) ReclassifyBankTransaction(ctx Context, transactionID, accountID, reason string) (BankTransaction, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return BankTransaction{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return BankTransaction{}, "", err
	}
	transaction, err := bankTransactionForWrite(st, transactionID)
	if err != nil {
		return BankTransaction{}, "", err
	}
	if transaction.Status != BankTransactionPosted {
		return BankTransaction{}, "", appErr(ErrConflict, "only a posted bank transaction can be reclassified")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return BankTransaction{}, "", appErr(ErrValidation, "bank transaction reclassification reason is required")
	}
	accountID = strings.TrimSpace(accountID)
	for _, existing := range transaction.Reclassifications {
		if existing.ToAccountID == accountID && existing.Reason == reason {
			return transaction, st.Root, nil
		}
	}
	if accountID == transaction.ClassificationAccount {
		return BankTransaction{}, "", appErr(ErrValidation, "new classification account must differ from the current account")
	}
	newAccount, err := validateBankClassification(st, transaction, accountID)
	if err != nil {
		return BankTransaction{}, "", err
	}
	oldAccount, ok := st.Accounts[transaction.ClassificationAccount]
	if !ok {
		return BankTransaction{}, "", appErr(ErrValidation, "current classification account %s not found", transaction.ClassificationAccount)
	}
	bankPosting, _, err := transactionPostings(st.Accounts[transaction.AccountID], oldAccount, transaction.AmountCents)
	if err != nil {
		return BankTransaction{}, "", err
	}
	amount, err := absoluteCents(transaction.AmountCents)
	if err != nil {
		return BankTransaction{}, "", err
	}
	var postings []Posting
	if bankPosting.Debit > 0 {
		postings = []Posting{{AccountID: oldAccount.ID, Debit: amount, Memo: "Reverse prior classification"}, {AccountID: newAccount.ID, Credit: amount, Memo: "Apply new classification"}}
	} else {
		postings = []Posting{{AccountID: oldAccount.ID, Credit: amount, Memo: "Reverse prior classification"}, {AccountID: newAccount.ID, Debit: amount, Memo: "Apply new classification"}}
	}
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               transaction.Date,
		Memo:               "Bank transaction reclassified " + transaction.ID,
		Workflow:           "bank.transaction.reclassify",
		PostingSemantics:   "bank_transaction_reclassified",
		SourceDocumentType: "bank_transaction",
		SourceDocumentID:   transaction.ID,
		Source:             "bank_transaction",
		SourceKey:          transaction.ID + ":reclassify:" + oldAccount.ID + ":" + newAccount.ID + ":" + makeID("reason", reason),
		Postings:           postings,
		Metadata: map[string]string{
			"from_account_id": oldAccount.ID,
			"to_account_id":   newAccount.ID,
			"reason":          reason,
		},
	})
	if err != nil {
		return BankTransaction{}, "", err
	}
	transaction.Reclassifications = append(transaction.Reclassifications, BankReclassification{
		FromAccountID: oldAccount.ID,
		ToAccountID:   newAccount.ID,
		Reason:        reason,
		JournalID:     entry.ID,
		CreatedAt:     s.now().UTC(),
		CreatedBy:     ctx.Actor,
	})
	transaction.ClassificationAccount = newAccount.ID
	transaction.JournalEntryIDs = append(transaction.JournalEntryIDs, entry.ID)
	transaction.UpdatedAt = s.now().UTC()
	transaction.UpdatedBy = ctx.Actor
	root, err = s.appendEventAt(ctx, "bank.transaction", transaction.ID, "bank transaction reclassify", wrapEvent("bank.transaction.update", bankTransactionPayload{Transaction: transaction}), root)
	return transaction, root, err
}

func (s *Store) PairBankTransfer(ctx Context, firstID, secondID string) ([]BankTransaction, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return nil, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return nil, "", err
	}
	first, err := bankTransactionForWrite(st, firstID)
	if err != nil {
		return nil, "", err
	}
	second, err := bankTransactionForWrite(st, secondID)
	if err != nil {
		return nil, "", err
	}
	if first.ID == second.ID {
		return nil, "", appErr(ErrValidation, "a bank transaction cannot be paired with itself")
	}
	if first.Status == BankTransactionPaired && first.TransferTransactionID == second.ID && second.Status == BankTransactionPaired && second.TransferTransactionID == first.ID {
		return []BankTransaction{first, second}, st.Root, nil
	}
	if first.Status != BankTransactionStaged || second.Status != BankTransactionStaged {
		return nil, "", appErr(ErrConflict, "both transfer transactions must be staged and unposted")
	}
	if first.Pending || second.Pending {
		return nil, "", appErr(ErrValidation, "pending bank transactions cannot be paired")
	}
	if first.Currency != second.Currency {
		return nil, "", appErr(ErrValidation, "transfer transaction currencies must match")
	}
	firstAccount := st.Accounts[first.AccountID]
	secondAccount := st.Accounts[second.AccountID]
	if firstAccount.ID == secondAccount.ID {
		return nil, "", appErr(ErrValidation, "transfer transactions must belong to different book accounts")
	}
	firstPosting, err := bankAccountPosting(firstAccount, first.AmountCents)
	if err != nil {
		return nil, "", err
	}
	secondPosting, err := bankAccountPosting(secondAccount, second.AmountCents)
	if err != nil {
		return nil, "", err
	}
	firstAmount, _ := absoluteCents(first.AmountCents)
	secondAmount, _ := absoluteCents(second.AmountCents)
	if firstAmount != secondAmount || (firstPosting.Debit > 0) == (secondPosting.Debit > 0) {
		return nil, "", appErr(ErrValidation, "transfer transactions must have equal amounts and opposite ledger effects")
	}
	from, to := first, second
	fromPosting, toPosting := firstPosting, secondPosting
	if firstPosting.Debit > 0 {
		from, to = second, first
		fromPosting, toPosting = secondPosting, firstPosting
	}
	ids := []string{first.ID, second.ID}
	sort.Strings(ids)
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               laterDate(first.Date, second.Date),
		Memo:               "Bank transfer " + from.ID + " to " + to.ID,
		Workflow:           "bank.transfer.pair",
		PostingSemantics:   "book_account_transfer",
		SourceDocumentType: "bank_transfer",
		SourceDocumentID:   ids[0] + ":" + ids[1],
		Source:             "bank_transfer",
		SourceKey:          ids[0] + ":" + ids[1],
		Postings:           []Posting{fromPosting, toPosting},
		Metadata: map[string]string{
			"from_transaction_id": from.ID,
			"to_transaction_id":   to.ID,
		},
	})
	if err != nil {
		return nil, "", err
	}
	first.Status = BankTransactionPaired
	first.TransferTransactionID = second.ID
	first.JournalEntryIDs = []string{entry.ID}
	first.UpdatedAt = s.now().UTC()
	first.UpdatedBy = ctx.Actor
	second.Status = BankTransactionPaired
	second.TransferTransactionID = first.ID
	second.JournalEntryIDs = []string{entry.ID}
	second.UpdatedAt = s.now().UTC()
	second.UpdatedBy = ctx.Actor
	root, err = s.appendEventAt(ctx, "bank.transaction", ids[0]+":"+ids[1], "bank transfer pair", wrapEvent("bank.transfer.pair", bankTransferPairPayload{From: first, To: second}), root)
	return []BankTransaction{first, second}, root, err
}

func (s *Store) PreviewBankReconciliation(ctx Context, statementID string) (ReconciliationReport, error) {
	st, err := s.LoadState()
	if err != nil {
		return ReconciliationReport{}, err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerRead); err != nil {
		return ReconciliationReport{}, err
	}
	return buildReconciliationReport(st, strings.TrimSpace(statementID))
}

func (s *Store) CompleteBankReconciliation(ctx Context, statementID string) (ReconciliationReport, string, error) {
	st, err := s.LoadState()
	if err != nil {
		return ReconciliationReport{}, "", err
	}
	if err := EnsurePermission(st, ctx, PermissionLedgerWrite); err != nil {
		return ReconciliationReport{}, "", err
	}
	report, err := buildReconciliationReport(st, strings.TrimSpace(statementID))
	if err != nil {
		return ReconciliationReport{}, "", err
	}
	statement := st.BankStatements[report.StatementID]
	if statement.Status == ReconciliationCompleted {
		return report, st.Root, nil
	}
	if !report.CanComplete {
		return report, "", appErr(ErrValidation, "reconciliation cannot complete: difference_cents=%d activity_difference_cents=%d blockers=%s", report.DifferenceCents, report.ActivityDifferenceCents, strings.Join(report.Blockers, "; "))
	}
	statement.Status = ReconciliationCompleted
	statement.CompletedAt = s.now().UTC()
	statement.CompletedBy = ctx.Actor
	root, err := s.appendEventAt(ctx, "bank.statement", statement.ID, "bank reconciliation complete", wrapEvent("bank.statement.update", bankStatementPayload{Statement: statement}), st.Root)
	if err != nil {
		return ReconciliationReport{}, "", err
	}
	report.Status = ReconciliationCompleted
	return report, root, nil
}

func normalizeBankStatement(st State, statement BankStatement) (BankStatement, error) {
	statement.ID = strings.TrimSpace(statement.ID)
	statement.AccountID = strings.TrimSpace(statement.AccountID)
	account, ok := st.Accounts[statement.AccountID]
	if !ok {
		return BankStatement{}, appErr(ErrValidation, "bank statement account %s not found", statement.AccountID)
	}
	if !isStatementAccount(account) {
		return BankStatement{}, appErr(ErrValidation, "bank statement account must have role operating_cash, bank_account, or credit_card")
	}
	var err error
	statement.PeriodStart, err = normalizeBankDate(statement.PeriodStart, "", "statement period_start")
	if err != nil {
		return BankStatement{}, err
	}
	statement.PeriodEnd, err = normalizeBankDate(statement.PeriodEnd, "", "statement period_end")
	if err != nil {
		return BankStatement{}, err
	}
	if statement.PeriodStart > statement.PeriodEnd {
		return BankStatement{}, appErr(ErrValidation, "statement period_start must not be after period_end")
	}
	statement.Currency = strings.ToUpper(strings.TrimSpace(statement.Currency))
	if !validCurrency(statement.Currency) {
		return BankStatement{}, appErr(ErrValidation, "statement currency must be a three-letter code")
	}
	statement.ExternalRefs, err = normalizeBankExternalRefs(statement.ExternalRefs)
	if err != nil {
		return BankStatement{}, err
	}
	if len(statement.ExternalRefs) == 0 {
		return BankStatement{}, appErr(ErrValidation, "bank statement external_refs are required for idempotent import")
	}
	if err := validateSourceDocument(statement.SourceDocument); err != nil {
		return BankStatement{}, err
	}
	if statement.ID == "" {
		statement.ID = makeID("stmt", externalRefKey(statement.ExternalRefs[0]))
	}
	return statement, nil
}

func normalizeBankTransaction(st State, transaction BankTransaction) (BankTransaction, error) {
	transaction.ID = strings.TrimSpace(transaction.ID)
	transaction.StatementID = strings.TrimSpace(transaction.StatementID)
	statement, ok := st.BankStatements[transaction.StatementID]
	if !ok {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction statement %s not found", transaction.StatementID)
	}
	if statement.Status == ReconciliationCompleted {
		return BankTransaction{}, appErr(ErrConflict, "cannot import a transaction into completed statement %s", statement.ID)
	}
	transaction.AccountID = strings.TrimSpace(transaction.AccountID)
	if transaction.AccountID == "" {
		transaction.AccountID = statement.AccountID
	}
	if transaction.AccountID != statement.AccountID {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction account must match its statement account")
	}
	var err error
	transaction.Date, err = normalizeBankDate(transaction.Date, "", "bank transaction date")
	if err != nil {
		return BankTransaction{}, err
	}
	if transaction.AmountCents == 0 || transaction.AmountCents == math.MinInt64 {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction amount must be a nonzero supported cent amount")
	}
	transaction.Currency = strings.ToUpper(strings.TrimSpace(transaction.Currency))
	if transaction.Currency == "" {
		transaction.Currency = statement.Currency
	}
	if transaction.Currency != statement.Currency {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction currency must match its statement")
	}
	transaction.ExternalRefs, err = normalizeBankExternalRefs(transaction.ExternalRefs)
	if err != nil {
		return BankTransaction{}, err
	}
	if len(transaction.ExternalRefs) == 0 {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction external_refs are required for idempotent import")
	}
	if err := validateSourceDocument(transaction.SourceDocument); err != nil {
		return BankTransaction{}, err
	}
	if transaction.ID == "" {
		transaction.ID = makeID("btxn", externalRefKey(transaction.ExternalRefs[0]))
	}
	return transaction, nil
}

func validateBankClassification(st State, transaction BankTransaction, accountID string) (Account, error) {
	account, ok := st.Accounts[strings.TrimSpace(accountID)]
	if !ok {
		return Account{}, appErr(ErrValidation, "classification account %s not found", accountID)
	}
	if account.ID == transaction.AccountID {
		return Account{}, appErr(ErrValidation, "classification account must differ from the statement account")
	}
	if isStatementAccount(account) {
		return Account{}, appErr(ErrValidation, "book-account transfers must use bank transfer pair")
	}
	settings := st.effectiveSettings()
	if settings.AccountingBasis != AccountingBasisAccrual && (account.Role == AccountRoleAccountsReceivable || account.Role == AccountRoleAccountsPayable) {
		return Account{}, appErr(ErrValidation, "account role %q is incompatible with %s bank transaction posting", account.Role, settings.AccountingBasis)
	}
	if settings.AccountingBasis == AccountingBasisCash && (account.Role == AccountRoleFixedAsset || account.Role == AccountRoleInventory) {
		return Account{}, appErr(ErrValidation, "account role %q requires modified_cash or accrual accounting", account.Role)
	}
	if settings.AccountingBasis == AccountingBasisModifiedCash {
		if account.Role == AccountRoleFixedAsset && !settings.ModifiedCashPolicy.CapitalizeFixedAssets {
			return Account{}, appErr(ErrValidation, "fixed asset classification is disabled by modified-cash policy")
		}
		if account.Role == AccountRoleInventory && !settings.ModifiedCashPolicy.TrackInventory {
			return Account{}, appErr(ErrValidation, "inventory classification is disabled by modified-cash policy")
		}
	}
	return account, nil
}

func transactionPostings(bank, counter Account, amount int64) (Posting, Posting, error) {
	bankPosting, err := bankAccountPosting(bank, amount)
	if err != nil {
		return Posting{}, Posting{}, err
	}
	counterPosting := Posting{AccountID: counter.ID, Memo: "Bank transaction classification"}
	if bankPosting.Debit > 0 {
		counterPosting.Credit = bankPosting.Debit
	} else {
		counterPosting.Debit = bankPosting.Credit
	}
	return bankPosting, counterPosting, nil
}

func bankAccountPosting(account Account, amount int64) (Posting, error) {
	if !isStatementAccount(account) {
		return Posting{}, appErr(ErrValidation, "account %s is not a bank or card statement account", account.ID)
	}
	abs, err := absoluteCents(amount)
	if err != nil {
		return Posting{}, err
	}
	posting := Posting{AccountID: account.ID, Memo: "Statement transaction"}
	increaseIsDebit := account.Type == AccountAsset
	if (amount > 0) == increaseIsDebit {
		posting.Debit = abs
	} else {
		posting.Credit = abs
	}
	return posting, nil
}

func buildReconciliationReport(st State, statementID string) (ReconciliationReport, error) {
	statement, ok := st.BankStatements[statementID]
	if !ok {
		return ReconciliationReport{}, appErr(ErrNotFound, "bank statement %s not found", statementID)
	}
	report := ReconciliationReport{
		StatementID: statement.ID, AccountID: statement.AccountID,
		PeriodStart: statement.PeriodStart, PeriodEnd: statement.PeriodEnd,
		Currency: statement.Currency, OpeningBalanceCents: statement.OpeningBalanceCents,
		ClosingBalanceCents: statement.ClosingBalanceCents, Status: statement.Status,
		UnmatchedItems: []ReconciliationItem{}, DuplicateItems: []ReconciliationItem{},
		PendingItems: []ReconciliationItem{}, OutOfPeriodItems: []ReconciliationItem{}, Blockers: []string{},
	}
	matchedJournalIDs := map[string]bool{}
	seenRefs := map[string]string{}
	transactionIDs := make([]string, 0)
	for id, transaction := range st.BankTransactions {
		if transaction.StatementID == statement.ID {
			transactionIDs = append(transactionIDs, id)
		}
	}
	sort.Strings(transactionIDs)
	for _, id := range transactionIDs {
		transaction := st.BankTransactions[id]
		for _, ref := range transaction.ExternalRefs {
			key := externalRefKey(ref)
			if prior, exists := seenRefs[key]; exists && prior != transaction.ID {
				report.DuplicateItems = append(report.DuplicateItems, reconciliationTransactionItem(transaction, "duplicate external reference "+key))
			} else {
				seenRefs[key] = transaction.ID
			}
		}
		if transaction.Date < statement.PeriodStart || transaction.Date > statement.PeriodEnd {
			report.OutOfPeriodItems = append(report.OutOfPeriodItems, reconciliationTransactionItem(transaction, "transaction date is outside the statement period"))
			continue
		}
		if transaction.Pending {
			report.PendingItems = append(report.PendingItems, reconciliationTransactionItem(transaction, "pending transaction"))
			continue
		}
		var err error
		report.StatementActivityCents, err = checkedAddCents(report.StatementActivityCents, transaction.AmountCents, "statement activity")
		if err != nil {
			return ReconciliationReport{}, err
		}
		if transaction.Status == BankTransactionStaged || transaction.Status == BankTransactionReversed {
			reason := "transaction is not posted or paired"
			if transaction.Status == BankTransactionReversed {
				reason = "transaction posting was reversed"
			}
			report.UnmatchedItems = append(report.UnmatchedItems, reconciliationTransactionItem(transaction, reason))
		}
		for _, journalID := range transaction.JournalEntryIDs {
			matchedJournalIDs[journalID] = true
		}
	}
	account := st.Accounts[statement.AccountID]
	journalIDs := make([]string, 0, len(st.JournalEntries))
	for id := range st.JournalEntries {
		journalIDs = append(journalIDs, id)
	}
	sort.Strings(journalIDs)
	for _, journalID := range journalIDs {
		entry := st.JournalEntries[journalID]
		if entry.Date > statement.PeriodEnd {
			continue
		}
		var journalEffect int64
		for _, posting := range entry.Postings {
			if posting.AccountID != statement.AccountID {
				continue
			}
			effect, err := normalBalancePostingEffect(account, posting)
			if err != nil {
				return ReconciliationReport{}, err
			}
			journalEffect, err = checkedAddCents(journalEffect, effect, "journal account effect")
			if err != nil {
				return ReconciliationReport{}, err
			}
		}
		var err error
		report.LedgerBalanceCents, err = checkedAddCents(report.LedgerBalanceCents, journalEffect, "ledger balance")
		if err != nil {
			return ReconciliationReport{}, err
		}
		if journalEffect != 0 && entry.Date >= statement.PeriodStart && !matchedJournalIDs[journalID] {
			report.UnmatchedItems = append(report.UnmatchedItems, ReconciliationItem{Kind: "ledger_journal", ID: journalID, Date: entry.Date, AmountCents: journalEffect, BlockingReason: "ledger journal affecting the account is not matched to a statement transaction"})
		}
	}
	var err error
	report.DifferenceCents, err = checkedSubtractCents(report.ClosingBalanceCents, report.LedgerBalanceCents, "reconciliation difference")
	if err != nil {
		return ReconciliationReport{}, err
	}
	expectedClosing, err := checkedAddCents(report.OpeningBalanceCents, report.StatementActivityCents, "statement computed closing balance")
	if err != nil {
		return ReconciliationReport{}, err
	}
	report.ActivityDifferenceCents, err = checkedSubtractCents(report.ClosingBalanceCents, expectedClosing, "statement activity difference")
	if err != nil {
		return ReconciliationReport{}, err
	}
	if report.DifferenceCents != 0 {
		report.Blockers = append(report.Blockers, "ledger balance does not equal statement closing balance")
	}
	if report.ActivityDifferenceCents != 0 {
		report.Blockers = append(report.Blockers, "statement activity does not bridge opening and closing balances")
	}
	if len(report.UnmatchedItems) > 0 {
		report.Blockers = append(report.Blockers, "unmatched items remain")
	}
	if len(report.DuplicateItems) > 0 {
		report.Blockers = append(report.Blockers, "duplicate items remain")
	}
	if len(report.PendingItems) > 0 {
		report.Blockers = append(report.Blockers, "pending items remain")
	}
	if len(report.OutOfPeriodItems) > 0 {
		report.Blockers = append(report.Blockers, "out-of-period items remain")
	}
	report.CanComplete = report.DifferenceCents == 0 && report.ActivityDifferenceCents == 0 && len(report.Blockers) == 0
	return report, nil
}

func normalBalancePostingEffect(account Account, posting Posting) (int64, error) {
	if account.Type == AccountAsset {
		return checkedSubtractCents(posting.Debit, posting.Credit, "asset posting effect")
	}
	if account.Type == AccountLiability {
		return checkedSubtractCents(posting.Credit, posting.Debit, "liability posting effect")
	}
	return 0, appErr(ErrValidation, "statement account %s has invalid account type %s", account.ID, account.Type)
}

func normalizeBankExternalRefs(refs []ExternalSourceRef) ([]ExternalSourceRef, error) {
	normalized, err := normalizeExternalRefs(refs)
	if err != nil {
		return nil, err
	}
	for _, ref := range normalized {
		if ref.DisplayName != "" || ref.URL != "" || len(ref.Metadata) > 0 {
			return nil, appErr(ErrValidation, "bank import external_refs accept only source_system, external_id, and external_type; use opaque source-document IDs and hashes instead of PII-bearing metadata")
		}
	}
	return normalized, nil
}

func validateSourceDocument(ref *SourceDocumentReference) error {
	if ref == nil {
		return nil
	}
	ref.ID = strings.TrimSpace(ref.ID)
	ref.ContentSHA256 = strings.ToLower(strings.TrimSpace(ref.ContentSHA256))
	if ref.ID == "" || ref.ContentSHA256 == "" {
		return appErr(ErrValidation, "source_document requires an opaque id and content_sha256")
	}
	if strings.ContainsAny(ref.ID, " \t\r\n") || len(ref.ID) > 200 {
		return appErr(ErrValidation, "source_document id must be an opaque non-whitespace identifier of at most 200 characters")
	}
	digest, err := hex.DecodeString(ref.ContentSHA256)
	if err != nil || len(digest) != 32 {
		return appErr(ErrValidation, "source_document content_sha256 must be a 64-character hexadecimal SHA-256 digest")
	}
	return nil
}

func bankTransactionForWrite(st State, transactionID string) (BankTransaction, error) {
	transactionID = strings.TrimSpace(transactionID)
	if transactionID == "" {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction id is required")
	}
	transaction, ok := st.BankTransactions[transactionID]
	if !ok {
		return BankTransaction{}, appErr(ErrNotFound, "bank transaction %s not found", transactionID)
	}
	statement, ok := st.BankStatements[transaction.StatementID]
	if !ok {
		return BankTransaction{}, appErr(ErrValidation, "bank transaction references unknown statement %s", transaction.StatementID)
	}
	if statement.Status == ReconciliationCompleted {
		return BankTransaction{}, appErr(ErrConflict, "completed statement %s is immutable", statement.ID)
	}
	return transaction, nil
}

func isStatementAccount(account Account) bool {
	return (account.Type == AccountAsset && (account.Role == AccountRoleOperatingCash || account.Role == AccountRoleBankAccount)) ||
		(account.Type == AccountLiability && account.Role == AccountRoleCreditCard)
}

func bankPostingSemantics(posting Posting) string {
	if posting.Debit > 0 {
		return "statement_account_increase"
	}
	return "statement_account_decrease"
}

func normalizeBankDate(value, fallback, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	if value == "" {
		return "", appErr(ErrValidation, "%s is required", label)
	}
	if _, err := time.Parse("2006-01-02", value); err != nil {
		return "", appErr(ErrValidation, "%s must use YYYY-MM-DD", label)
	}
	return value, nil
}

func validCurrency(currency string) bool {
	if len(currency) != 3 {
		return false
	}
	for _, r := range currency {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func absoluteCents(value int64) (int64, error) {
	if value == math.MinInt64 {
		return 0, appErr(ErrValidation, "amount exceeds the supported signed 64-bit cent range")
	}
	if value < 0 {
		return -value, nil
	}
	return value, nil
}

func laterDate(first, second string) string {
	if first > second {
		return first
	}
	return second
}

func reconciliationTransactionItem(transaction BankTransaction, reason string) ReconciliationItem {
	return ReconciliationItem{Kind: "bank_transaction", ID: transaction.ID, Date: transaction.Date, AmountCents: transaction.AmountCents, BlockingReason: reason}
}

func bankStatementIDByExternalRefs(st State, refs []ExternalSourceRef) (string, bool) {
	for _, ref := range refs {
		for id, statement := range st.BankStatements {
			for _, existingRef := range statement.ExternalRefs {
				if externalRefKey(ref) == externalRefKey(existingRef) {
					return id, true
				}
			}
		}
	}
	return "", false
}

func bankTransactionIDByExternalRefs(st State, refs []ExternalSourceRef) (string, bool) {
	for _, ref := range refs {
		for id, transaction := range st.BankTransactions {
			for _, existingRef := range transaction.ExternalRefs {
				if externalRefKey(ref) == externalRefKey(existingRef) {
					return id, true
				}
			}
		}
	}
	return "", false
}

func bankStatementsEquivalent(existing, candidate BankStatement) bool {
	return existing.AccountID == candidate.AccountID && existing.PeriodStart == candidate.PeriodStart &&
		existing.PeriodEnd == candidate.PeriodEnd && existing.OpeningBalanceCents == candidate.OpeningBalanceCents &&
		existing.ClosingBalanceCents == candidate.ClosingBalanceCents && existing.Currency == candidate.Currency &&
		externalRefsEqual(existing.ExternalRefs, candidate.ExternalRefs) && sourceDocumentsEqual(existing.SourceDocument, candidate.SourceDocument)
}

func bankTransactionsEquivalent(existing, candidate BankTransaction) bool {
	return existing.StatementID == candidate.StatementID && existing.AccountID == candidate.AccountID && existing.Date == candidate.Date &&
		existing.AmountCents == candidate.AmountCents && existing.Currency == candidate.Currency && existing.Pending == candidate.Pending &&
		externalRefsEqual(existing.ExternalRefs, candidate.ExternalRefs) && sourceDocumentsEqual(existing.SourceDocument, candidate.SourceDocument)
}

func sourceDocumentsEqual(first, second *SourceDocumentReference) bool {
	if first == nil || second == nil {
		return first == nil && second == nil
	}
	return *first == *second
}

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
	root := st.Root
	openingSourceKey := "bank_statement:" + statement.ID + ":opening_balance"
	if journalID, ok := st.SourceKeys[openingSourceKey]; ok {
		statement.OpeningJournalEntryID = journalID
	} else {
		balance, hasActivity, err := accountBalanceBefore(st, st.Accounts[statement.AccountID], statement.PeriodStart)
		if err != nil {
			return BankStatement{}, "", err
		}
		if balance != statement.OpeningBalanceCents && !hasActivity && !hasStatementForAccount(st, statement.AccountID) {
			openingEquityID, err := accountIDByRole(st, AccountRoleOpeningBalanceEquity)
			if err != nil {
				return BankStatement{}, "", appErr(ErrValidation, "first statement with nonzero opening balance requires account role %q", AccountRoleOpeningBalanceEquity)
			}
			postings, err := openingBalancePostings(st.Accounts[statement.AccountID], openingEquityID, statement.OpeningBalanceCents)
			if err != nil {
				return BankStatement{}, "", err
			}
			openingDate, err := dayBefore(statement.PeriodStart)
			if err != nil {
				return BankStatement{}, "", err
			}
			entry, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
				Date:               openingDate,
				Memo:               "Statement opening balance " + statement.ID,
				Workflow:           "bank.statement.opening_balance",
				PostingSemantics:   "statement_opening_balance",
				SourceDocumentType: "bank_statement",
				SourceDocumentID:   statement.ID,
				Source:             "bank_statement",
				SourceKey:          statement.ID + ":opening_balance",
				Postings:           postings,
			})
			if err != nil {
				return BankStatement{}, "", err
			}
			statement.OpeningJournalEntryID = entry.ID
			root = newRoot
		}
	}
	statement.Status = ReconciliationOpen
	statement.CreatedAt = s.now().UTC()
	statement.CreatedBy = ctx.Actor
	root, err = s.appendEventAt(ctx, "bank.statement", statement.ID, "bank statement import", wrapEvent("bank.statement.create", bankStatementPayload{Statement: statement}), root)
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
	nextPostingVersion := transaction.PostingVersion + 1
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               transaction.Date,
		Memo:               "Bank transaction " + transaction.ID,
		Workflow:           "bank.transaction.post",
		PostingSemantics:   bankPostingSemantics(bankPosting),
		SourceDocumentType: "bank_transaction",
		SourceDocumentID:   transaction.ID,
		Source:             "bank_transaction",
		SourceKey:          transaction.ID + ":post:" + int64String(int64(nextPostingVersion)),
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
	transaction.PostingVersion = nextPostingVersion
	transaction.JournalEntryIDs = appendUniqueString(transaction.JournalEntryIDs, entry.ID)
	transaction.ActiveJournalEntryIDs = []string{entry.ID}
	transaction.ReversalReason = ""
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
	if reason == "" {
		return BankTransaction{}, "", appErr(ErrValidation, "bank transaction reversal reason is required")
	}
	date, err = normalizeBankDate(date, transaction.Date, "reversal date")
	if err != nil {
		return BankTransaction{}, "", err
	}
	if date != transaction.Date {
		return BankTransaction{}, "", appErr(ErrValidation, "bank transaction reversal date must equal the source transaction date %s", transaction.Date)
	}
	if transaction.Status == BankTransactionStaged && len(transaction.Reversals) > 0 {
		last := transaction.Reversals[len(transaction.Reversals)-1]
		if last.Reason == reason && last.Date == date {
			return transaction, st.Root, nil
		}
	}
	if transaction.Status != BankTransactionPosted {
		return BankTransaction{}, "", appErr(ErrConflict, "only a posted bank transaction can be reversed")
	}
	root := st.Root
	originalJournalIDs := append([]string(nil), transaction.ActiveJournalEntryIDs...)
	if len(originalJournalIDs) == 0 {
		originalJournalIDs = append([]string(nil), transaction.JournalEntryIDs...)
	}
	reversalJournalIDs := make([]string, 0, len(originalJournalIDs))
	for _, journalID := range originalJournalIDs {
		original, ok := st.JournalEntries[journalID]
		if !ok {
			return BankTransaction{}, "", appErr(ErrValidation, "bank transaction journal entry %s not found", journalID)
		}
		entry, newRoot, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
			Date:               date,
			Memo:               "Bank transaction reversed " + transaction.ID + ": " + reason,
			Workflow:           "bank.transaction.reverse",
			PostingSemantics:   "bank_transaction_reversed",
			SourceDocumentType: "bank_transaction",
			SourceDocumentID:   transaction.ID,
			Source:             "bank_transaction",
			SourceKey:          transaction.ID + ":reverse:" + int64String(int64(transaction.PostingVersion)) + ":" + journalID,
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
		transaction.JournalEntryIDs = appendUniqueString(transaction.JournalEntryIDs, entry.ID)
		reversalJournalIDs = append(reversalJournalIDs, entry.ID)
	}
	transaction.Status = BankTransactionStaged
	transaction.ClassificationAccount = ""
	transaction.ActiveJournalEntryIDs = nil
	transaction.ReversalReason = reason
	transaction.Reversals = append(transaction.Reversals, BankTransactionReversal{
		Reason: reason, Date: date,
		OriginalJournalEntryIDs: originalJournalIDs, ReversalJournalEntryIDs: reversalJournalIDs,
		CreatedAt: s.now().UTC(), CreatedBy: ctx.Actor,
	})
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
	if accountID == transaction.ClassificationAccount {
		if len(transaction.Reclassifications) > 0 {
			last := transaction.Reclassifications[len(transaction.Reclassifications)-1]
			if last.ToAccountID == accountID && last.Reason == reason {
				return transaction, st.Root, nil
			}
		}
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
		Memo:               "Bank transaction reclassified " + transaction.ID + ": " + reason,
		Workflow:           "bank.transaction.reclassify",
		PostingSemantics:   "bank_transaction_reclassified",
		SourceDocumentType: "bank_transaction",
		SourceDocumentID:   transaction.ID,
		Source:             "bank_transaction",
		SourceKey:          transaction.ID + ":reclassify:" + int64String(int64(transaction.PostingVersion)) + ":" + int64String(int64(len(transaction.Reclassifications)+1)),
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
	transaction.JournalEntryIDs = appendUniqueString(transaction.JournalEntryIDs, entry.ID)
	transaction.ActiveJournalEntryIDs = appendUniqueString(transaction.ActiveJournalEntryIDs, entry.ID)
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
	firstStatement := st.BankStatements[first.StatementID]
	secondStatement := st.BankStatements[second.StatementID]
	if first.Date < firstStatement.PeriodStart || first.Date > firstStatement.PeriodEnd ||
		second.Date < secondStatement.PeriodStart || second.Date > secondStatement.PeriodEnd {
		return nil, "", appErr(ErrValidation, "transfer transactions must fall within their own statement periods")
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
	firstAmount, err := absoluteCents(first.AmountCents)
	if err != nil {
		return nil, "", err
	}
	secondAmount, err := absoluteCents(second.AmountCents)
	if err != nil {
		return nil, "", err
	}
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
	postingDate := laterDate(first.Date, second.Date)
	if postingDate < firstStatement.PeriodStart || postingDate > firstStatement.PeriodEnd ||
		postingDate < secondStatement.PeriodStart || postingDate > secondStatement.PeriodEnd {
		return nil, "", appErr(ErrValidation, "transfer posting date must fall within both statement periods")
	}
	nextTransferVersion := first.TransferVersion
	if second.TransferVersion > nextTransferVersion {
		nextTransferVersion = second.TransferVersion
	}
	nextTransferVersion++
	pairID := ids[0] + ":" + ids[1] + ":" + int64String(int64(nextTransferVersion))
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               postingDate,
		Memo:               "Bank transfer " + from.ID + " to " + to.ID,
		Workflow:           "bank.transfer.pair",
		PostingSemantics:   "book_account_transfer",
		SourceDocumentType: "bank_transfer",
		SourceDocumentID:   pairID,
		Source:             "bank_transfer",
		SourceKey:          pairID,
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
	first.TransferVersion = nextTransferVersion
	first.TransferDirection = transferDirection(first.ID, from.ID)
	first.JournalEntryIDs = appendUniqueString(first.JournalEntryIDs, entry.ID)
	first.ActiveJournalEntryIDs = []string{entry.ID}
	first.TransferHistory = append(first.TransferHistory, BankTransferHistory{
		PairID: pairID, OtherTransactionID: second.ID, EconomicDirection: first.TransferDirection,
		JournalEntryID: entry.ID, CreatedAt: s.now().UTC(), CreatedBy: ctx.Actor,
	})
	first.UpdatedAt = s.now().UTC()
	first.UpdatedBy = ctx.Actor
	second.Status = BankTransactionPaired
	second.TransferTransactionID = first.ID
	second.TransferVersion = nextTransferVersion
	second.TransferDirection = transferDirection(second.ID, from.ID)
	second.JournalEntryIDs = appendUniqueString(second.JournalEntryIDs, entry.ID)
	second.ActiveJournalEntryIDs = []string{entry.ID}
	second.TransferHistory = append(second.TransferHistory, BankTransferHistory{
		PairID: pairID, OtherTransactionID: first.ID, EconomicDirection: second.TransferDirection,
		JournalEntryID: entry.ID, CreatedAt: s.now().UTC(), CreatedBy: ctx.Actor,
	})
	second.UpdatedAt = s.now().UTC()
	second.UpdatedBy = ctx.Actor
	root, err = s.appendEventAt(ctx, "bank.transaction", ids[0]+":"+ids[1], "bank transfer pair", wrapEvent("bank.transfer.pair", bankTransferPairPayload{From: first, To: second}), root)
	return []BankTransaction{first, second}, root, err
}

func (s *Store) ReverseBankTransfer(ctx Context, firstID, secondID, reason, date string) ([]BankTransaction, string, error) {
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
		return nil, "", appErr(ErrValidation, "a bank transfer requires two different transactions")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, "", appErr(ErrValidation, "bank transfer reversal reason is required")
	}
	firstHistory, firstIndex, firstOK := currentTransferHistory(first, second.ID)
	secondHistory, secondIndex, secondOK := currentTransferHistory(second, first.ID)
	if !firstOK || !secondOK || firstHistory.PairID != secondHistory.PairID || firstHistory.JournalEntryID != secondHistory.JournalEntryID {
		return nil, "", appErr(ErrConflict, "transactions do not share the same auditable transfer pair")
	}
	original, ok := st.JournalEntries[firstHistory.JournalEntryID]
	if !ok {
		return nil, "", appErr(ErrValidation, "bank transfer journal entry %s not found", firstHistory.JournalEntryID)
	}
	date, err = normalizeBankDate(date, original.Date, "transfer reversal date")
	if err != nil {
		return nil, "", err
	}
	if date != original.Date {
		return nil, "", appErr(ErrValidation, "bank transfer reversal date must equal the original transfer journal date %s", original.Date)
	}
	if first.Status == BankTransactionStaged && second.Status == BankTransactionStaged &&
		firstHistory.ReversalJournalEntryID != "" && secondHistory.ReversalJournalEntryID == firstHistory.ReversalJournalEntryID {
		if firstHistory.ReversalReason == reason && firstHistory.ReversalDate == date {
			return []BankTransaction{first, second}, st.Root, nil
		}
		return nil, "", appErr(ErrConflict, "bank transfer was already reversed with different audit details")
	}
	if first.Status != BankTransactionPaired || second.Status != BankTransactionPaired ||
		first.TransferTransactionID != second.ID || second.TransferTransactionID != first.ID {
		return nil, "", appErr(ErrConflict, "both transactions must be the currently paired transfer legs")
	}
	entry, root, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date:               date,
		Memo:               "Bank transfer reversed " + firstHistory.PairID + ": " + reason,
		Workflow:           "bank.transfer.reverse",
		PostingSemantics:   "book_account_transfer_reversed",
		SourceDocumentType: "bank_transfer",
		SourceDocumentID:   firstHistory.PairID,
		Source:             "bank_transfer",
		SourceKey:          firstHistory.PairID + ":reverse",
		Postings:           reversePostings(original.Postings),
		Metadata: map[string]string{
			"original_journal_entry_id": original.ID,
			"reason":                    reason,
			"economic_from_transaction": economicTransferLeg(first, second, "from"),
			"economic_to_transaction":   economicTransferLeg(first, second, "to"),
		},
	})
	if err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	first.TransferHistory[firstIndex].ReversalJournalEntryID = entry.ID
	first.TransferHistory[firstIndex].ReversalReason = reason
	first.TransferHistory[firstIndex].ReversalDate = date
	second.TransferHistory[secondIndex].ReversalJournalEntryID = entry.ID
	second.TransferHistory[secondIndex].ReversalReason = reason
	second.TransferHistory[secondIndex].ReversalDate = date
	for _, transaction := range []*BankTransaction{&first, &second} {
		transaction.Status = BankTransactionStaged
		transaction.JournalEntryIDs = appendUniqueString(transaction.JournalEntryIDs, entry.ID)
		transaction.ActiveJournalEntryIDs = nil
		transaction.TransferTransactionID = ""
		transaction.TransferDirection = ""
		transaction.UpdatedAt = now
		transaction.UpdatedBy = ctx.Actor
	}
	ids := []string{first.ID, second.ID}
	sort.Strings(ids)
	root, err = s.appendEventAt(ctx, "bank.transaction", ids[0]+":"+ids[1], "bank transfer reverse", wrapEvent("bank.transfer.reverse", bankTransferPairPayload{From: first, To: second}), root)
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
		switch transaction.Status {
		case BankTransactionStaged:
			report.UnmatchedItems = append(report.UnmatchedItems, reconciliationTransactionItem(transaction, "transaction is not posted or paired"))
		case BankTransactionPosted, BankTransactionPaired:
		default:
			report.UnmatchedItems = append(report.UnmatchedItems, reconciliationTransactionItem(transaction, "transaction has invalid workflow status"))
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

func accountBalanceBefore(st State, account Account, beforeDate string) (int64, bool, error) {
	var balance int64
	hasActivity := false
	for _, entry := range st.JournalEntries {
		for _, posting := range entry.Postings {
			if posting.AccountID != account.ID {
				continue
			}
			hasActivity = true
			if entry.Date >= beforeDate {
				continue
			}
			effect, err := normalBalancePostingEffect(account, posting)
			if err != nil {
				return 0, false, err
			}
			balance, err = checkedAddCents(balance, effect, "opening ledger balance")
			if err != nil {
				return 0, false, err
			}
		}
	}
	return balance, hasActivity, nil
}

func openingBalancePostings(account Account, equityAccountID string, balance int64) ([]Posting, error) {
	amount, err := absoluteCents(balance)
	if err != nil {
		return nil, err
	}
	if amount == 0 {
		return nil, nil
	}
	statementPosting, err := bankAccountPosting(account, balance)
	if err != nil {
		return nil, err
	}
	equityPosting := Posting{AccountID: equityAccountID, Memo: "Opening balance equity"}
	if statementPosting.Debit > 0 {
		equityPosting.Credit = amount
	} else {
		equityPosting.Debit = amount
	}
	return []Posting{statementPosting, equityPosting}, nil
}

func hasStatementForAccount(st State, accountID string) bool {
	for _, statement := range st.BankStatements {
		if statement.AccountID == accountID {
			return true
		}
	}
	return false
}

func dayBefore(date string) (string, error) {
	parsed, err := time.Parse("2006-01-02", date)
	if err != nil {
		return "", appErr(ErrValidation, "date must use YYYY-MM-DD")
	}
	return parsed.AddDate(0, 0, -1).Format("2006-01-02"), nil
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

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func transferDirection(transactionID, fromID string) string {
	if transactionID == fromID {
		return "from"
	}
	return "to"
}

func currentTransferHistory(transaction BankTransaction, otherID string) (BankTransferHistory, int, bool) {
	for i := len(transaction.TransferHistory) - 1; i >= 0; i-- {
		history := transaction.TransferHistory[i]
		if history.OtherTransactionID == otherID {
			return history, i, true
		}
	}
	return BankTransferHistory{}, -1, false
}

func economicTransferLeg(first, second BankTransaction, direction string) string {
	if first.TransferDirection == direction {
		return first.ID
	}
	if second.TransferDirection == direction {
		return second.ID
	}
	return ""
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

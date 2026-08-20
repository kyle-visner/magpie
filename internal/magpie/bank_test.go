package magpie

import (
	"errors"
	"strings"
	"testing"

	"github.com/kyle-visner/jaybase"
)

type failBankDocumentAppendOnceBackend struct {
	storageBackend
	command string
	failed  bool
}

func (b *failBankDocumentAppendOnceBackend) AppendAt(ctx jaybase.Context, options jaybase.AppendOptions, expectedRoot string) (string, error) {
	if !b.failed && options.Command == b.command {
		b.failed = true
		if _, err := b.storageBackend.AppendAt(ctx, jaybase.AppendOptions{
			Type: "martin.concurrent", Command: "injected concurrent foreign append",
			Payload: map[string]string{"during": b.command}, CreatedAt: options.CreatedAt,
		}, expectedRoot); err != nil {
			return "", err
		}
		return "", &jaybase.AppError{Code: jaybase.ErrConflict, Message: "injected stale root after workflow journal append"}
	}
	return b.storageBackend.AppendAt(ctx, options, expectedRoot)
}

func bankStatementFixture(account Account, id string, opening, closing int64) BankStatement {
	return BankStatement{
		AccountID: account.ID, PeriodStart: "2026-06-01", PeriodEnd: "2026-06-30",
		OpeningBalanceCents: opening, ClosingBalanceCents: closing, Currency: "USD",
		ExternalRefs:   []ExternalSourceRef{{SourceSystem: "statement_feed", ExternalID: id, ExternalType: "statement"}},
		SourceDocument: &SourceDocumentReference{ID: "doc-" + id, ContentSHA256: strings.Repeat("a", 64)},
	}
}

func bankTransactionFixture(statement BankStatement, id, date string, amount int64) BankTransaction {
	return BankTransaction{
		StatementID: statement.ID, AccountID: statement.AccountID, Date: date, AmountCents: amount, Currency: "USD",
		ExternalRefs: []ExternalSourceRef{{SourceSystem: "transaction_feed", ExternalID: id, ExternalType: "transaction"}},
	}
}

func TestBankTransactionsPostIdempotentlyWithoutJournalAdjustAndReconcile(t *testing.T) {
	s, owner := newTestStore(t)
	bank := mustRoleAccount(t, s, owner, "1010", "Operating Bank", AccountAsset, AccountRoleBankAccount)
	revenue := mustRoleAccount(t, s, owner, "4000", "Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	expense := mustRoleAccount(t, s, owner, "6000", "Expense", AccountExpense, AccountRoleDefaultExpense)
	if _, err := s.UpsertUser(owner, User{ID: "bookkeeper", Role: "Accountant"}); err != nil {
		t.Fatal(err)
	}
	bookkeeper := Context{Actor: "bookkeeper"}

	statement, _, err := s.ImportBankStatement(bookkeeper, bankStatementFixture(bank, "june", 0, 500))
	if err != nil {
		t.Fatal(err)
	}
	retry, retryRoot, err := s.ImportBankStatement(bookkeeper, bankStatementFixture(bank, "june", 0, 500))
	if err != nil || retry.ID != statement.ID || retryRoot == "" {
		t.Fatalf("statement retry was not idempotent: statement=%#v root=%q err=%v", retry, retryRoot, err)
	}

	income, _, err := s.ImportBankTransaction(bookkeeper, bankTransactionFixture(statement, "income", "2026-06-10", 1000))
	if err != nil {
		t.Fatal(err)
	}
	if retryIncome, _, err := s.ImportBankTransaction(bookkeeper, bankTransactionFixture(statement, "income", "2026-06-10", 1000)); err != nil || retryIncome.ID != income.ID {
		t.Fatalf("transaction retry was not idempotent: %#v err=%v", retryIncome, err)
	}
	expenseTxn, _, err := s.ImportBankTransaction(bookkeeper, bankTransactionFixture(statement, "expense", "2026-06-12", -500))
	if err != nil {
		t.Fatal(err)
	}
	postedIncome, _, err := s.PostBankTransaction(bookkeeper, income.ID, revenue.ID)
	if err != nil {
		t.Fatalf("Accountant role should post without journal:adjust: %v", err)
	}
	postedExpense, _, err := s.PostBankTransaction(bookkeeper, expenseTxn.ID, expense.ID)
	if err != nil {
		t.Fatal(err)
	}
	if postedIncome.Status != BankTransactionPosted || postedExpense.Status != BankTransactionPosted {
		t.Fatalf("unexpected posting statuses: %#v %#v", postedIncome, postedExpense)
	}
	retryPost, _, err := s.PostBankTransaction(bookkeeper, income.ID, revenue.ID)
	if err != nil || len(retryPost.JournalEntryIDs) != 1 {
		t.Fatalf("posting retry was not idempotent: %#v err=%v", retryPost, err)
	}

	report, err := s.PreviewBankReconciliation(bookkeeper, statement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.OpeningBalanceCents != 0 || report.StatementActivityCents != 500 || report.ClosingBalanceCents != 500 ||
		report.LedgerBalanceCents != 500 || report.DifferenceCents != 0 || report.ActivityDifferenceCents != 0 || !report.CanComplete {
		t.Fatalf("unexpected reconciliation report: %#v", report)
	}
	completed, _, err := s.CompleteBankReconciliation(bookkeeper, statement.ID)
	if err != nil || completed.Status != ReconciliationCompleted {
		t.Fatalf("reconciliation completion failed: %#v err=%v", completed, err)
	}
	if _, _, err := s.ImportBankTransaction(bookkeeper, bankTransactionFixture(statement, "late", "2026-06-20", 1)); err == nil {
		t.Fatal("expected completed statement to reject later imports")
	}

	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range st.JournalEntries {
		if entry.Origin != JournalOriginWorkflow || entry.Workflow != "bank.transaction.post" || entry.SourceDocumentType != "bank_transaction" {
			t.Fatalf("bank posting did not stamp workflow semantics: %#v", entry)
		}
	}
}

func TestFirstStatementCreatesGuardedOpeningBalanceWithoutJournalAdjust(t *testing.T) {
	s, owner := newTestStore(t)
	bank := mustRoleAccount(t, s, owner, "1010", "Opening Bank", AccountAsset, AccountRoleBankAccount)
	equity := mustRoleAccount(t, s, owner, "3900", "Opening Balance Equity", AccountEquity, AccountRoleOpeningBalanceEquity)
	if _, err := s.UpsertUser(owner, User{ID: "bookkeeper", Role: "Accountant"}); err != nil {
		t.Fatal(err)
	}
	ctx := Context{Actor: "bookkeeper"}
	input := bankStatementFixture(bank, "first-opening", 12500, 12500)
	backend := &failBankDocumentAppendOnceBackend{storageBackend: s.db, command: "bank statement import"}
	s.db = backend
	if _, _, err := s.ImportBankStatement(ctx, input); err == nil {
		t.Fatal("expected injected statement append failure after opening journal")
	}
	statement, _, err := s.ImportBankStatement(ctx, input)
	if err != nil {
		t.Fatalf("ledger:write actor could not establish guarded opening balance: %v", err)
	}
	if statement.OpeningJournalEntryID == "" {
		t.Fatalf("statement did not retain opening journal: %#v", statement)
	}
	retry, _, err := s.ImportBankStatement(ctx, input)
	if err != nil || retry.OpeningJournalEntryID != statement.OpeningJournalEntryID {
		t.Fatalf("opening balance retry was not idempotent: %#v err=%v", retry, err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	entry := st.JournalEntries[statement.OpeningJournalEntryID]
	if entry.Workflow != "bank.statement.opening_balance" || entry.Origin != JournalOriginWorkflow || entry.Date != "2026-05-31" {
		t.Fatalf("unexpected opening workflow journal: %#v", entry)
	}
	if len(entry.Postings) != 2 || entry.Postings[0].AccountID != bank.ID || entry.Postings[1].AccountID != equity.ID {
		t.Fatalf("unexpected opening balance postings: %#v", entry.Postings)
	}
	report, err := s.PreviewBankReconciliation(ctx, statement.ID)
	if err != nil || !report.CanComplete || report.LedgerBalanceCents != 12500 {
		t.Fatalf("opening statement did not reconcile: %#v err=%v", report, err)
	}
}

func TestFirstStatementPostsOnlyOpeningDeltaAndIgnoresLaterActivity(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	equity := mustRoleAccount(t, s, ctx, "3900", "Opening Balance Equity", AccountEquity, AccountRoleOpeningBalanceEquity)
	revenue := mustRoleAccount(t, s, ctx, "4000", "Revenue", AccountRevenue, AccountRoleDefaultServiceRevenue)
	if _, _, err := s.CreateJournalEntry(ctx, JournalEntry{
		Date: "2026-05-15", Memo: "Prior ledger balance", ManualReason: "test setup",
		Postings: []Posting{{AccountID: bank.ID, Debit: 4000}, {AccountID: equity.ID, Credit: 4000}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateJournalEntry(ctx, JournalEntry{
		Date: "2026-06-10", Memo: "Later-period activity", ManualReason: "test setup",
		Postings: []Posting{{AccountID: bank.ID, Debit: 1000}, {AccountID: revenue.ID, Credit: 1000}},
	}); err != nil {
		t.Fatal(err)
	}
	statement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "opening-delta", 10000, 11000))
	if err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	entry := st.JournalEntries[statement.OpeningJournalEntryID]
	if len(entry.Postings) != 2 || entry.Postings[0].Debit != 6000 || entry.Postings[1].Credit != 6000 {
		t.Fatalf("opening workflow did not post only the balance delta: %#v", entry)
	}
}

func TestOpeningSourceKeyRecoveryRejectsMismatchedJournal(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	equity := mustRoleAccount(t, s, ctx, "3900", "Opening Balance Equity", AccountEquity, AccountRoleOpeningBalanceEquity)
	input := bankStatementFixture(bank, "opening-mismatch", 100, 100)
	input.ID = "stmt:opening-mismatch"
	wrongPostings, err := openingBalancePostings(bank, equity.ID, 90)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.createWorkflowJournalEntry(ctx, workflowJournalRequest{
		Date: "2026-05-30", Memo: "Statement opening balance " + input.ID,
		Workflow: "bank.statement.opening_balance", PostingSemantics: "statement_opening_balance",
		SourceDocumentType: "bank_statement", SourceDocumentID: input.ID,
		Source: "bank_statement", SourceKey: input.ID + ":opening_balance", Postings: wrongPostings,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ImportBankStatement(ctx, input)
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrConflict {
		t.Fatalf("expected mismatched recovered opening journal to conflict, got %v", err)
	}
}

func TestBankMultiEventWorkflowRecoversAfterStaleRootWithoutDuplicateJournal(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	expense := mustRoleAccount(t, s, ctx, "6000", "Expense", AccountExpense, AccountRoleDefaultExpense)
	statement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "stale-root", 0, -500))
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, "stale-root-txn", "2026-06-10", -500))
	if err != nil {
		t.Fatal(err)
	}
	backend := &failBankDocumentAppendOnceBackend{storageBackend: s.db, command: "bank transaction post"}
	s.db = backend
	if _, _, err := s.PostBankTransaction(ctx, txn.ID, expense.ID); err == nil {
		t.Fatal("expected injected stale-root failure after journal append")
	}
	partial, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.JournalEntries) != 1 || partial.BankTransactions[txn.ID].Status != BankTransactionStaged {
		t.Fatalf("unexpected partial workflow state: %#v", partial)
	}
	posted, _, err := s.PostBankTransaction(ctx, txn.ID, expense.ID)
	if err != nil {
		t.Fatalf("retry did not recover partial workflow: %v", err)
	}
	final, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if posted.Status != BankTransactionPosted || len(final.JournalEntries) != 1 || len(posted.JournalEntryIDs) != 1 {
		t.Fatalf("recovery duplicated or failed to link the workflow journal: transaction=%#v journals=%#v", posted, final.JournalEntries)
	}
}

func TestBankTransferPairAndReverseRecoverPartialWorkflowWrites(t *testing.T) {
	s, ctx := newTestStore(t)
	fromAccount := mustRoleAccount(t, s, ctx, "1010", "From Bank", AccountAsset, AccountRoleBankAccount)
	toAccount := mustAccount(t, s, ctx, "Savings", AccountAsset)
	if _, _, err := s.SetAccountRole(ctx, toAccount.ID, AccountRoleBankAccount); err != nil {
		t.Fatal(err)
	}
	fromStatement, _, _ := s.ImportBankStatement(ctx, bankStatementFixture(fromAccount, "partial-from", 0, -100))
	toInput := bankStatementFixture(toAccount, "partial-to", 0, 100)
	toStatement, _, err := s.ImportBankStatement(ctx, toInput)
	if err != nil {
		t.Fatal(err)
	}
	fromTxn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(fromStatement, "partial-from-txn", "2026-06-10", -100))
	toTxn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(toStatement, "partial-to-txn", "2026-06-10", 100))
	backend := &failBankDocumentAppendOnceBackend{storageBackend: s.db, command: "bank transfer pair"}
	s.db = backend
	if _, _, err := s.PairBankTransfer(ctx, fromTxn.ID, toTxn.ID); err == nil {
		t.Fatal("expected injected transfer-pair document append failure")
	}
	paired, _, err := s.PairBankTransfer(ctx, fromTxn.ID, toTxn.ID)
	if err != nil {
		t.Fatalf("transfer pair retry did not recover: %v", err)
	}
	if len(paired) != 2 || paired[0].Status != BankTransactionPaired {
		t.Fatalf("unexpected recovered transfer: %#v", paired)
	}
	backend.command = "bank transfer reverse"
	backend.failed = false
	if _, _, err := s.ReverseBankTransfer(ctx, fromTxn.ID, toTxn.ID, "wrong match", ""); err == nil {
		t.Fatal("expected injected transfer-reverse document append failure")
	}
	reversed, _, err := s.ReverseBankTransfer(ctx, fromTxn.ID, toTxn.ID, "wrong match", "")
	if err != nil {
		t.Fatalf("transfer reverse retry did not recover: %v", err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if reversed[0].Status != BankTransactionStaged || reversed[1].Status != BankTransactionStaged || len(st.JournalEntries) != 2 {
		t.Fatalf("partial transfer recovery duplicated journals or state: reversed=%#v journals=%#v", reversed, st.JournalEntries)
	}
}

func TestBankTransactionCorrectionsPreserveHistoryAndExactlyReverse(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	oldExpense := mustRoleAccount(t, s, ctx, "6000", "Supplies", AccountExpense, AccountRoleDefaultExpense)
	newExpense := mustAccount(t, s, ctx, "Travel", AccountExpense)
	statement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "corrections", 0, -2500))
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, "purchase", "2026-06-10", -2500))
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err = s.PostBankTransaction(ctx, txn.ID, oldExpense.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalJournalID := txn.JournalEntryIDs[0]
	txn, _, err = s.ReclassifyBankTransaction(ctx, txn.ID, newExpense.ID, "receipt shows travel")
	if err != nil {
		t.Fatal(err)
	}
	if txn.ClassificationAccount != newExpense.ID || len(txn.Reclassifications) != 1 || txn.JournalEntryIDs[0] != originalJournalID {
		t.Fatalf("reclassification did not preserve original decision: %#v", txn)
	}
	if len(txn.ActiveJournalEntryIDs) != 2 || txn.ActiveJournalEntryIDs[0] != originalJournalID {
		t.Fatalf("post/reclass sequence lost the original active post: %#v", txn.ActiveJournalEntryIDs)
	}
	if retry, _, err := s.ReclassifyBankTransaction(ctx, txn.ID, newExpense.ID, "receipt shows travel"); err != nil || len(retry.Reclassifications) != 1 {
		t.Fatalf("reclassification retry was not idempotent: %#v err=%v", retry, err)
	}
	reclassJournalID := txn.Reclassifications[0].JournalID
	if _, _, err := s.ReverseBankTransaction(ctx, txn.ID, "classification needs to be redone", "2026-06-15"); err == nil {
		t.Fatal("expected out-of-period-risk reversal date to fail closed")
	}
	before, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	backend := &failBankDocumentAppendOnceBackend{storageBackend: s.db, command: "bank transaction reverse"}
	s.db = backend
	if _, _, err := s.ReverseBankTransaction(ctx, txn.ID, "classification needs to be redone", ""); err == nil {
		t.Fatal("expected injected transaction-reversal state append failure")
	}
	partial, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.BankTransactions[txn.ID].Reversals) != 0 || len(partial.JournalEntries) != 4 {
		t.Fatalf("unexpected transaction-reversal partial state: %#v", partial)
	}
	txn, _, err = s.ReverseBankTransaction(ctx, txn.ID, "classification needs to be redone", "")
	if err != nil {
		t.Fatal(err)
	}
	if txn.Status != BankTransactionStaged || txn.ClassificationAccount != "" || len(txn.JournalEntryIDs) != 4 {
		t.Fatalf("expected exact offsets for original and correction journals: %#v", txn)
	}
	if len(txn.Reversals) != 1 {
		t.Fatalf("transaction reversal retry duplicated audit rows: %#v", txn.Reversals)
	}
	if retry, _, err := s.ReverseBankTransaction(ctx, txn.ID, "classification needs to be redone", ""); err != nil || len(retry.JournalEntryIDs) != 4 {
		t.Fatalf("reversal retry was not idempotent: %#v err=%v", retry, err)
	}
	after, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after.JournalEntries[originalJournalID]; !ok {
		t.Fatal("original journal was not preserved")
	}
	if _, ok := after.JournalEntries[reclassJournalID]; !ok {
		t.Fatal("reclassification journal was not preserved")
	}
	for i, originalID := range []string{originalJournalID, reclassJournalID} {
		offset := after.JournalEntries[txn.JournalEntryIDs[i+2]]
		original := before.JournalEntries[originalID]
		if len(offset.Postings) != len(original.Postings) {
			t.Fatalf("offset posting count differs: original=%#v offset=%#v", original, offset)
		}
		for j := range original.Postings {
			if offset.Postings[j].Debit != original.Postings[j].Credit || offset.Postings[j].Credit != original.Postings[j].Debit {
				t.Fatalf("journal was not exactly offset: original=%#v offset=%#v", original, offset)
			}
		}
	}
	if _, _, err := s.PostBankTransaction(ctx, txn.ID, newExpense.ID); err != nil {
		t.Fatalf("reversed transaction could not be corrected and reposted: %v", err)
	}
	report, err := s.PreviewBankReconciliation(ctx, statement.ID)
	if err != nil || !report.CanComplete {
		t.Fatalf("corrected transaction did not reconcile cleanly: %#v err=%v", report, err)
	}
}

func TestBankReclassificationDoesNotTreatHistoricalStateAsCurrentIdempotency(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	firstExpense := mustRoleAccount(t, s, ctx, "6000", "Supplies", AccountExpense, AccountRoleDefaultExpense)
	secondExpense := mustAccount(t, s, ctx, "Travel", AccountExpense)
	statement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "reclass-cycle", 0, -100))
	if err != nil {
		t.Fatal(err)
	}
	txn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, "cycle", "2026-06-10", -100))
	txn, _, err = s.PostBankTransaction(ctx, txn.ID, firstExpense.ID)
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err = s.ReclassifyBankTransaction(ctx, txn.ID, secondExpense.ID, "receipt review")
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err = s.ReclassifyBankTransaction(ctx, txn.ID, firstExpense.ID, "manager correction")
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err = s.ReclassifyBankTransaction(ctx, txn.ID, secondExpense.ID, "receipt review")
	if err != nil {
		t.Fatalf("historical target/reason should execute again when it is not current: %v", err)
	}
	if txn.ClassificationAccount != secondExpense.ID || len(txn.Reclassifications) != 3 {
		t.Fatalf("historical reclassification was incorrectly treated as a no-op: %#v", txn)
	}
	retry, _, err := s.ReclassifyBankTransaction(ctx, txn.ID, secondExpense.ID, "receipt review")
	if err != nil || len(retry.Reclassifications) != 3 {
		t.Fatalf("last exact current operation was not idempotent: %#v err=%v", retry, err)
	}
}

func TestBankTransactionReverseFailsWithoutResolvableLiveJournals(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	expense := mustRoleAccount(t, s, ctx, "6000", "Expense", AccountExpense, AccountRoleDefaultExpense)
	statement, _, _ := s.ImportBankStatement(ctx, bankStatementFixture(bank, "missing-live", 0, -100))
	txn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, "missing-live-txn", "2026-06-10", -100))
	txn, _, _ = s.PostBankTransaction(ctx, txn.ID, expense.ID)
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	txn.ActiveJournalEntryIDs = nil
	root, err := s.appendEventAt(ctx, "bank.transaction", txn.ID, "test corrupt live links", wrapEvent("bank.transaction.update", bankTransactionPayload{Transaction: txn}), st.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReverseBankTransaction(ctx, txn.ID, "must fail closed", ""); err == nil {
		t.Fatal("expected reversal without live journal links to fail")
	}
	txn.ActiveJournalEntryIDs = []string{txn.JournalEntryIDs[0], "jrnl:missing"}
	if _, err := s.appendEventAt(ctx, "bank.transaction", txn.ID, "test missing live journal", wrapEvent("bank.transaction.update", bankTransactionPayload{Transaction: txn}), root); err != nil {
		t.Fatal(err)
	}
	before, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ReverseBankTransaction(ctx, txn.ID, "must fail closed", ""); err == nil {
		t.Fatal("expected reversal with an unresolvable live journal to fail")
	}
	after, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(after.JournalEntries) != len(before.JournalEntries) {
		t.Fatalf("reversal wrote an offset before verifying all live journals: before=%d after=%d", len(before.JournalEntries), len(after.JournalEntries))
	}
}

func TestBankTransferPairingHandlesBankAndCreditCardWithoutIncomeOrExpense(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	card := mustRoleAccount(t, s, ctx, "2010", "Credit Card", AccountLiability, AccountRoleCreditCard)
	mustRoleAccount(t, s, ctx, "3900", "Opening Balance Equity", AccountEquity, AccountRoleOpeningBalanceEquity)
	bankStatement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "bank-transfer", 10000, 5000))
	if err != nil {
		t.Fatal(err)
	}
	cardStatement := bankStatementFixture(card, "card-transfer", 5000, 0)
	cardStatement.PeriodStart = bankStatement.PeriodStart
	cardStatement.PeriodEnd = bankStatement.PeriodEnd
	cardStatement, _, err = s.ImportBankStatement(ctx, cardStatement)
	if err != nil {
		t.Fatal(err)
	}
	bankTxn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(bankStatement, "bank-payment", "2026-06-20", -5000))
	cardTxn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(cardStatement, "card-payment", "2026-06-21", -5000))
	paired, _, err := s.PairBankTransfer(ctx, bankTxn.ID, cardTxn.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(paired) != 2 || paired[0].Status != BankTransactionPaired || paired[1].Status != BankTransactionPaired {
		t.Fatalf("unexpected transfer pair: %#v", paired)
	}
	if paired[0].TransferDirection == paired[1].TransferDirection || paired[0].TransferDirection == "" || paired[1].TransferDirection == "" {
		t.Fatalf("economic transfer direction was not persisted: %#v", paired)
	}
	retry, _, err := s.PairBankTransfer(ctx, bankTxn.ID, cardTxn.ID)
	if err != nil || len(retry) != 2 {
		t.Fatalf("transfer retry was not idempotent: %#v err=%v", retry, err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	pairJournals := 0
	for _, entry := range st.JournalEntries {
		if entry.Workflow != "bank.transfer.pair" {
			continue
		}
		pairJournals++
		for _, posting := range entry.Postings {
			if posting.AccountID != bank.ID && posting.AccountID != card.ID {
				t.Fatalf("transfer journal used income/expense account: %#v", entry)
			}
		}
	}
	if pairJournals != 1 {
		t.Fatalf("transfer retry created duplicate pair journals: %#v", st.JournalEntries)
	}
	if _, _, err := s.ReverseBankTransfer(ctx, bankTxn.ID, cardTxn.ID, "paired the wrong source rows", "2026-06-30"); err == nil {
		t.Fatal("expected transfer reversal on a different accounting date to fail closed")
	}
	reversed, _, err := s.ReverseBankTransfer(ctx, bankTxn.ID, cardTxn.ID, "paired the wrong source rows", "")
	if err != nil {
		t.Fatal(err)
	}
	if reversed[0].Status != BankTransactionStaged || reversed[1].Status != BankTransactionStaged ||
		reversed[0].TransferTransactionID != "" || reversed[1].TransferTransactionID != "" {
		t.Fatalf("transfer reversal did not return both legs to staged state: %#v", reversed)
	}
	if retry, _, err := s.ReverseBankTransfer(ctx, bankTxn.ID, cardTxn.ID, "paired the wrong source rows", ""); err != nil || len(retry) != 2 {
		t.Fatalf("transfer reversal retry was not idempotent: %#v err=%v", retry, err)
	}
	repaired, _, err := s.PairBankTransfer(ctx, bankTxn.ID, cardTxn.ID)
	if err != nil || repaired[0].TransferVersion != 2 || repaired[1].TransferVersion != 2 {
		t.Fatalf("reversed transfer could not be safely re-paired: %#v err=%v", repaired, err)
	}
	for _, statementID := range []string{bankStatement.ID, cardStatement.ID} {
		report, err := s.PreviewBankReconciliation(ctx, statementID)
		if err != nil || !report.CanComplete {
			t.Fatalf("repaired transfer did not reconcile statement %s: %#v err=%v", statementID, report, err)
		}
	}
}

func TestBankTransferPairsAndReversesAcrossAdjacentStatementPeriods(t *testing.T) {
	s, ctx := newTestStore(t)
	fromAccount := mustRoleAccount(t, s, ctx, "1010", "Checking", AccountAsset, AccountRoleBankAccount)
	toAccount := mustAccount(t, s, ctx, "Savings", AccountAsset)
	if _, _, err := s.SetAccountRole(ctx, toAccount.ID, AccountRoleBankAccount); err != nil {
		t.Fatal(err)
	}
	clearing := mustRoleAccount(t, s, ctx, "1090", "Transfer Clearing", AccountAsset, AccountRoleTransferClearing)
	fromInput := bankStatementFixture(fromAccount, "jan-transfer", 0, -100)
	fromInput.PeriodStart, fromInput.PeriodEnd = "2026-01-01", "2026-01-31"
	toInput := bankStatementFixture(toAccount, "feb-transfer", 0, 100)
	toInput.PeriodStart, toInput.PeriodEnd = "2026-02-01", "2026-02-28"
	fromStatement, _, err := s.ImportBankStatement(ctx, fromInput)
	if err != nil {
		t.Fatal(err)
	}
	toStatement, _, err := s.ImportBankStatement(ctx, toInput)
	if err != nil {
		t.Fatal(err)
	}
	fromTxn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(fromStatement, "jan-31-out", "2026-01-31", -100))
	toTxn, _, _ := s.ImportBankTransaction(ctx, bankTransactionFixture(toStatement, "feb-1-in", "2026-02-01", 100))
	paired, _, err := s.PairBankTransfer(ctx, fromTxn.ID, toTxn.ID)
	if err != nil {
		t.Fatalf("ordinary adjacent-period transfer failed: %v", err)
	}
	if len(paired[0].ActiveJournalEntryIDs) != 2 || len(paired[0].TransferHistory[0].JournalEntryIDs) != 2 {
		t.Fatalf("cross-period transfer did not retain both leg journals: %#v", paired)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	dates := map[string]bool{}
	semantics := map[string]bool{}
	for _, journalID := range paired[0].ActiveJournalEntryIDs {
		entry := st.JournalEntries[journalID]
		dates[entry.Date] = true
		semantics[entry.PostingSemantics] = true
		foundClearing := false
		for _, posting := range entry.Postings {
			foundClearing = foundClearing || posting.AccountID == clearing.ID
		}
		if !foundClearing {
			t.Fatalf("cross-period transfer leg did not use clearing: %#v", entry)
		}
	}
	if !dates["2026-01-31"] || !dates["2026-02-01"] || !semantics["book_account_transfer_from"] || !semantics["book_account_transfer_to"] {
		t.Fatalf("cross-period transfer legs used unsafe dates or semantics: dates=%#v semantics=%#v", dates, semantics)
	}
	for _, statementID := range []string{fromStatement.ID, toStatement.ID} {
		report, err := s.PreviewBankReconciliation(ctx, statementID)
		if err != nil || !report.CanComplete {
			t.Fatalf("cross-period transfer did not reconcile statement %s: %#v err=%v", statementID, report, err)
		}
	}
	reversed, _, err := s.ReverseBankTransfer(ctx, fromTxn.ID, toTxn.ID, "wrong pair", "")
	if err != nil || len(reversed[0].TransferHistory[0].ReversalJournalEntryIDs) != 2 {
		t.Fatalf("cross-period transfer reversal failed: %#v err=%v", reversed, err)
	}
	repaired, _, err := s.PairBankTransfer(ctx, fromTxn.ID, toTxn.ID)
	if err != nil || repaired[0].TransferVersion != 2 {
		t.Fatalf("cross-period transfer could not be re-paired: %#v err=%v", repaired, err)
	}
	for _, statementID := range []string{fromStatement.ID, toStatement.ID} {
		report, err := s.PreviewBankReconciliation(ctx, statementID)
		if err != nil || !report.CanComplete {
			t.Fatalf("re-paired cross-period transfer did not reconcile statement %s: %#v err=%v", statementID, report, err)
		}
	}
}

func TestBankPostingSupportsRefundsAndOwnerEquityFlows(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	expense := mustRoleAccount(t, s, ctx, "6000", "Expense", AccountExpense, AccountRoleDefaultExpense)
	contribution := mustRoleAccount(t, s, ctx, "3000", "Owner Contribution", AccountEquity, AccountRoleOwnerContribution)
	draw := mustRoleAccount(t, s, ctx, "3100", "Owner Draw", AccountEquity, AccountRoleOwnerDraw)
	statement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "equity", 0, 1000))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id      string
		amount  int64
		account Account
	}{
		{"expense-refund", 500, expense},
		{"owner-contribution", 1000, contribution},
		{"owner-draw", -500, draw},
	}
	for _, tc := range cases {
		txn, _, err := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, tc.id, "2026-06-10", tc.amount))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := s.PostBankTransaction(ctx, txn.ID, tc.account.ID); err != nil {
			t.Fatalf("%s posting failed: %v", tc.id, err)
		}
	}
	report, err := s.PreviewBankReconciliation(ctx, statement.ID)
	if err != nil || !report.CanComplete {
		t.Fatalf("refund and owner flows did not reconcile: %#v err=%v", report, err)
	}
}

func TestBankReconciliationFailsClosedOnPendingOutOfPeriodAndUnmatchedItems(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	statement, _, err := s.ImportBankStatement(ctx, bankStatementFixture(bank, "blocked", 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	pending := bankTransactionFixture(statement, "pending", "2026-06-15", -100)
	pending.Pending = true
	if _, _, err := s.ImportBankTransaction(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, "outside", "2026-07-01", 100)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ImportBankTransaction(ctx, bankTransactionFixture(statement, "staged", "2026-06-20", 50)); err != nil {
		t.Fatal(err)
	}
	report, err := s.PreviewBankReconciliation(ctx, statement.ID)
	if err != nil {
		t.Fatal(err)
	}
	if report.CanComplete || len(report.PendingItems) != 1 || len(report.OutOfPeriodItems) != 1 || len(report.UnmatchedItems) != 1 {
		t.Fatalf("expected all blocker categories: %#v", report)
	}
	if _, _, err := s.CompleteBankReconciliation(ctx, statement.ID); err == nil {
		t.Fatal("expected blocked reconciliation completion to fail")
	}
}

func TestBankImportsRejectDuplicatesPIIMetadataAndUnauthorizedRoles(t *testing.T) {
	s, owner := newTestStore(t)
	bank := mustRoleAccount(t, s, owner, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	statementInput := bankStatementFixture(bank, "secure", 0, 100)
	statement, _, err := s.ImportBankStatement(owner, statementInput)
	if err != nil {
		t.Fatal(err)
	}
	conflict := statementInput
	conflict.ClosingBalanceCents = 200
	if _, _, err := s.ImportBankStatement(owner, conflict); err == nil {
		t.Fatal("expected reused external statement reference with different details to fail")
	}
	pii := bankTransactionFixture(statement, "pii", "2026-06-10", 100)
	pii.ExternalRefs[0].Metadata = map[string]string{"counterparty": "Sensitive Person"}
	if _, _, err := s.ImportBankTransaction(owner, pii); err == nil {
		t.Fatal("expected PII-bearing bank external metadata to fail")
	}
	invalidHash := bankTransactionFixture(statement, "bad-hash", "2026-06-10", 100)
	invalidHash.SourceDocument = &SourceDocumentReference{ID: "opaque", ContentSHA256: "not-a-hash"}
	if _, _, err := s.ImportBankTransaction(owner, invalidHash); err == nil {
		t.Fatal("expected invalid source document hash to fail")
	}
	if _, err := s.UpsertUser(owner, User{ID: "ops", Role: "Operations"}); err != nil {
		t.Fatal(err)
	}
	_, _, err = s.ImportBankTransaction(Context{Actor: "ops"}, bankTransactionFixture(statement, "unauthorized", "2026-06-10", 100))
	var app *AppError
	if !errors.As(err, &app) || app.Code != ErrPermission {
		t.Fatalf("expected fail-closed permission error, got %v", err)
	}
}

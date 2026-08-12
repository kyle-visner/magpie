package magpie

import (
	"errors"
	"strings"
	"testing"
)

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
	if retry, _, err := s.ReclassifyBankTransaction(ctx, txn.ID, newExpense.ID, "receipt shows travel"); err != nil || len(retry.Reclassifications) != 1 {
		t.Fatalf("reclassification retry was not idempotent: %#v err=%v", retry, err)
	}
	reclassJournalID := txn.Reclassifications[0].JournalID
	before, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	txn, _, err = s.ReverseBankTransaction(ctx, txn.ID, "card purchase was voided", "2026-06-15")
	if err != nil {
		t.Fatal(err)
	}
	if txn.Status != BankTransactionReversed || len(txn.JournalEntryIDs) != 4 {
		t.Fatalf("expected exact offsets for original and correction journals: %#v", txn)
	}
	if retry, _, err := s.ReverseBankTransaction(ctx, txn.ID, "card purchase was voided", "2026-06-15"); err != nil || len(retry.JournalEntryIDs) != 4 {
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
}

func TestBankTransferPairingHandlesBankAndCreditCardWithoutIncomeOrExpense(t *testing.T) {
	s, ctx := newTestStore(t)
	bank := mustRoleAccount(t, s, ctx, "1010", "Bank", AccountAsset, AccountRoleBankAccount)
	card := mustRoleAccount(t, s, ctx, "2010", "Credit Card", AccountLiability, AccountRoleCreditCard)
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
	retry, _, err := s.PairBankTransfer(ctx, bankTxn.ID, cardTxn.ID)
	if err != nil || len(retry) != 2 {
		t.Fatalf("transfer retry was not idempotent: %#v err=%v", retry, err)
	}
	st, err := s.LoadState()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.JournalEntries) != 1 {
		t.Fatalf("transfer created duplicate or extra journals: %#v", st.JournalEntries)
	}
	for _, entry := range st.JournalEntries {
		if entry.Workflow != "bank.transfer.pair" || len(entry.Postings) != 2 {
			t.Fatalf("unexpected transfer journal: %#v", entry)
		}
		for _, posting := range entry.Postings {
			if posting.AccountID != bank.ID && posting.AccountID != card.ID {
				t.Fatalf("transfer journal used income/expense account: %#v", entry)
			}
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

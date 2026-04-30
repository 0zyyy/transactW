package persistence

import (
	"context"
	"time"
)

type StoredTransaction struct {
	ID              string
	Type            string
	Amount          int64
	Currency        string
	TransactionDate time.Time
	Description     string
	CategoryName    string
}

func (s *Store) ExpenseTotal(ctx context.Context, userID string, startDate, endDate time.Time) (int64, error) {
	var total int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT transactw_expense_total($1, $2::date, $3::date)`,
		userID,
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"),
	).Scan(&total)
	return total, err
}

func (s *Store) RecentTransactions(ctx context.Context, userID string, startDate, endDate time.Time, limit int) ([]StoredTransaction, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT id, type, amount, currency, transaction_date, description, category_name
		 FROM transactw_recent_transactions($1, $2::date, $3::date, $4)`,
		userID,
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"),
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transactions []StoredTransaction
	for rows.Next() {
		var tx StoredTransaction
		if err := rows.Scan(&tx.ID, &tx.Type, &tx.Amount, &tx.Currency, &tx.TransactionDate, &tx.Description, &tx.CategoryName); err != nil {
			return nil, err
		}
		transactions = append(transactions, tx)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return transactions, nil
}

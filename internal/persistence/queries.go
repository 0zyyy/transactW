package persistence

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"transactw/internal/conversation"
	"transactw/internal/inference"
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
	return s.TotalByType(ctx, userID, "expense", startDate, endDate)
}

func (s *Store) TotalByType(ctx context.Context, userID, transactionType string, startDate, endDate time.Time) (int64, error) {
	transactionType = normalizedTransactionType(transactionType)
	var total int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(amount), 0)::BIGINT
		 FROM transactions
		 WHERE user_id = $1
		   AND ($2 = '' OR type = $2)
		   AND transaction_date::DATE BETWEEN $3::date AND $4::date`,
		userID,
		transactionType,
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"),
	).Scan(&total)
	return total, err
}

func (s *Store) RecentTransactions(ctx context.Context, userID string, startDate, endDate time.Time, limit int) ([]StoredTransaction, error) {
	return s.RecentTransactionsByType(ctx, userID, "", startDate, endDate, limit)
}

func (s *Store) RecentTransactionsByType(ctx context.Context, userID, transactionType string, startDate, endDate time.Time, limit int) ([]StoredTransaction, error) {
	transactionType = normalizedTransactionType(transactionType)
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT
			t.id,
			t.type,
			t.amount::BIGINT,
			t.currency,
			t.transaction_date::DATE,
			COALESCE(t.description, '') AS description,
			COALESCE(c.name, '') AS category_name
		 FROM transactions t
		 LEFT JOIN categories c ON c.id = t.category_id
		 WHERE t.user_id = $1
		   AND t.transaction_date::DATE BETWEEN $2::date AND $3::date
		   AND ($5 = '' OR t.type = $5)
		 ORDER BY t.transaction_date DESC, t.created_at DESC
		 LIMIT GREATEST(1, LEAST(COALESCE($4, 10), 50))`,
		userID,
		startDate.Format("2006-01-02"),
		endDate.Format("2006-01-02"),
		limit,
		transactionType,
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

func (s *Store) RunQuery(conversationKey string, query inference.QueryDraft) (conversation.QueryResult, error) {
	ctx := context.Background()
	startDate, err := time.Parse("2006-01-02", query.DateRange.StartDate)
	if err != nil {
		return conversation.QueryResult{}, err
	}
	endDate, err := time.Parse("2006-01-02", query.DateRange.EndDate)
	if err != nil {
		return conversation.QueryResult{}, err
	}
	userID, err := s.userIDForConversation(ctx, conversationKey)
	if err != nil {
		return conversation.QueryResult{}, err
	}

	metric := strings.TrimSpace(query.Metric)
	originalTransactionType := strings.ToLower(strings.TrimSpace(query.Type))
	transactionType := normalizedTransactionType(query.Type)
	if transactionType == "" {
		if strings.TrimSpace(query.Type) == "" {
			transactionType = "expense"
		}
	}
	if metric == "" {
		metric = "expense_total"
		if transactionType == "income" {
			metric = "income_total"
		}
	}

	result := conversation.QueryResult{
		Metric:    metric,
		Type:      transactionType,
		StartDate: query.DateRange.StartDate,
		EndDate:   query.DateRange.EndDate,
	}
	if originalTransactionType == "all" {
		result.Type = "all"
	}
	if metric == "transaction_list" {
		transactions, err := s.RecentTransactionsByType(ctx, userID, transactionType, startDate, endDate, 10)
		if err != nil {
			return conversation.QueryResult{}, err
		}
		for _, tx := range transactions {
			result.Transactions = append(result.Transactions, conversation.QueryTransaction{
				Type:            tx.Type,
				Amount:          tx.Amount,
				Currency:        tx.Currency,
				TransactionDate: tx.TransactionDate.Format("2006-01-02"),
				Description:     tx.Description,
				CategoryName:    tx.CategoryName,
			})
			result.Total += tx.Amount
		}
		return result, nil
	}

	total, err := s.TotalByType(ctx, userID, transactionType, startDate, endDate)
	if err != nil {
		return conversation.QueryResult{}, err
	}
	result.Total = total
	return result, nil
}

func normalizedTransactionType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "expense", "income":
		return strings.ToLower(strings.TrimSpace(value))
	case "", "all":
		return ""
	default:
		return "expense"
	}
}

func (s *Store) userIDForConversation(ctx context.Context, conversationKey string) (string, error) {
	var userID string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(user_id, '') FROM whatsapp_conversations WHERE conversation_key = $1`, conversationKey).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) || userID == "" {
		return defaultUserID, nil
	}
	return userID, err
}

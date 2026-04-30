package persistence

import (
	"context"
	"strings"
	"time"
)

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			default_currency TEXT NOT NULL DEFAULT 'IDR',
			timezone TEXT NOT NULL DEFAULT 'Asia/Jakarta',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id),
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			currency TEXT NOT NULL DEFAULT 'IDR',
			is_default BOOLEAN NOT NULL DEFAULT false,
			archived_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS categories (
			id TEXT PRIMARY KEY,
			user_id TEXT REFERENCES users(id),
			name TEXT NOT NULL,
			type TEXT NOT NULL,
			parent_id TEXT REFERENCES categories(id),
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS whatsapp_gateways (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			session_name TEXT NOT NULL,
			display_name TEXT,
			phone_number TEXT,
			provider_account_id TEXT,
			provider_phone_number_id TEXT,
			status TEXT NOT NULL DEFAULT 'active',
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (provider, session_name)
		)`,
		`CREATE TABLE IF NOT EXISTS whatsapp_identities (
			id TEXT PRIMARY KEY,
			user_id TEXT REFERENCES users(id),
			provider TEXT NOT NULL,
			wa_id TEXT NOT NULL,
			display_name TEXT,
			phone_number TEXT,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL,
			UNIQUE (provider, wa_id)
		)`,
		`CREATE TABLE IF NOT EXISTS whatsapp_conversations (
			conversation_key TEXT PRIMARY KEY,
			gateway_id TEXT NOT NULL REFERENCES whatsapp_gateways(id),
			chat_id TEXT NOT NULL,
			user_id TEXT REFERENCES users(id),
			sender_id TEXT,
			state TEXT NOT NULL DEFAULT 'idle',
			state_expires_at TIMESTAMPTZ,
			last_message_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS whatsapp_messages (
			id TEXT PRIMARY KEY,
			conversation_key TEXT NOT NULL REFERENCES whatsapp_conversations(conversation_key),
			provider_message_id TEXT,
			direction TEXT NOT NULL,
			message_type TEXT NOT NULL,
			body TEXT,
			media_id TEXT,
			status TEXT NOT NULL DEFAULT 'received',
			provider_timestamp TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL,
			UNIQUE (conversation_key, provider_message_id, direction)
		)`,
		`CREATE TABLE IF NOT EXISTS parser_runs (
			id TEXT PRIMARY KEY,
			conversation_key TEXT REFERENCES whatsapp_conversations(conversation_key),
			provider_message_id TEXT,
			provider TEXT NOT NULL,
			model TEXT,
			parser_version TEXT,
			input_text TEXT NOT NULL,
			output_json JSONB NOT NULL,
			intent TEXT,
			confidence REAL,
			status TEXT NOT NULL,
			error_message TEXT,
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transaction_drafts (
			id TEXT PRIMARY KEY,
			conversation_key TEXT NOT NULL REFERENCES whatsapp_conversations(conversation_key),
			user_id TEXT REFERENCES users(id),
			status TEXT NOT NULL,
			intent TEXT NOT NULL,
			type TEXT,
			amount INTEGER,
			currency TEXT NOT NULL DEFAULT 'IDR',
			transaction_date TEXT,
			merchant_name TEXT,
			description TEXT,
			category_hint TEXT,
			account_hint TEXT,
			confidence REAL,
			raw_json JSONB NOT NULL,
			expires_at TIMESTAMPTZ NOT NULL,
			confirmed_at TIMESTAMPTZ,
			cancelled_at TIMESTAMPTZ,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transaction_draft_items (
			id TEXT PRIMARY KEY,
			draft_id TEXT NOT NULL REFERENCES transaction_drafts(id) ON DELETE CASCADE,
			type TEXT NOT NULL,
			amount INTEGER NOT NULL,
			currency TEXT NOT NULL DEFAULT 'IDR',
			transaction_date TEXT,
			merchant_name TEXT,
			description TEXT,
			category_hint TEXT,
			account_hint TEXT,
			sort_order INTEGER NOT NULL DEFAULT 0,
			created_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS transactions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL REFERENCES users(id),
			account_id TEXT NOT NULL REFERENCES accounts(id),
			category_id TEXT REFERENCES categories(id),
			source_draft_id TEXT REFERENCES transaction_drafts(id),
			type TEXT NOT NULL,
			amount INTEGER NOT NULL,
			currency TEXT NOT NULL DEFAULT 'IDR',
			transaction_date TEXT NOT NULL,
			merchant_name TEXT,
			description TEXT,
			source TEXT NOT NULL,
			confirmed_at TIMESTAMPTZ NOT NULL,
			created_at TIMESTAMPTZ NOT NULL,
			updated_at TIMESTAMPTZ NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_transactions_user_date ON transactions (user_id, transaction_date DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_whatsapp_identities_user ON whatsapp_identities (user_id)`,
		`CREATE INDEX IF NOT EXISTS idx_whatsapp_messages_conversation_created ON whatsapp_messages (conversation_key, created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_transaction_drafts_conversation_status ON transaction_drafts (conversation_key, status, expires_at)`,
		`CREATE INDEX IF NOT EXISTS idx_parser_runs_conversation_created ON parser_runs (conversation_key, created_at DESC)`,
		`CREATE OR REPLACE FUNCTION transactw_expense_total(p_user_id TEXT, p_start_date DATE, p_end_date DATE)
		 RETURNS BIGINT
		 LANGUAGE SQL
		 STABLE
		 AS $$
			SELECT COALESCE(SUM(amount), 0)::BIGINT
			FROM transactions
			WHERE user_id = p_user_id
			  AND type = 'expense'
			  AND transaction_date::DATE BETWEEN p_start_date AND p_end_date
		 $$`,
		`CREATE OR REPLACE FUNCTION transactw_recent_transactions(p_user_id TEXT, p_start_date DATE, p_end_date DATE, p_limit INTEGER DEFAULT 10)
		 RETURNS TABLE (
			id TEXT,
			type TEXT,
			amount BIGINT,
			currency TEXT,
			transaction_date DATE,
			description TEXT,
			category_name TEXT
		 )
		 LANGUAGE SQL
		 STABLE
		 AS $$
			SELECT
				t.id,
				t.type,
				t.amount::BIGINT,
				t.currency,
				t.transaction_date::DATE,
				COALESCE(t.description, '') AS description,
				COALESCE(c.name, '') AS category_name
			FROM transactions t
			LEFT JOIN categories c ON c.id = t.category_id
			WHERE t.user_id = p_user_id
			  AND t.transaction_date::DATE BETWEEN p_start_date AND p_end_date
			ORDER BY t.transaction_date DESC, t.created_at DESC
			LIMIT GREATEST(1, LEAST(COALESCE(p_limit, 10), 50))
		 $$`,
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) seed(ctx context.Context) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackUnlessDone(tx)

	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO users (id, display_name, default_currency, timezone, created_at, updated_at)
		 VALUES ($1, 'Default WhatsApp User', 'IDR', 'Asia/Jakarta', $2, $3)
		 ON CONFLICT (id) DO NOTHING`,
		defaultUserID,
		now,
		now,
	); err != nil {
		return err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO accounts (id, user_id, name, type, currency, is_default, created_at, updated_at)
		 VALUES ($1, $2, 'Cash', 'cash', 'IDR', true, $3, $4)
		 ON CONFLICT (id) DO NOTHING`,
		defaultAccountID,
		defaultUserID,
		now,
		now,
	); err != nil {
		return err
	}
	for _, category := range seedCategories() {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO categories (id, user_id, name, type, created_at, updated_at)
			 VALUES ($1, NULL, $2, $3, $4, $5)
			 ON CONFLICT (id) DO NOTHING`,
			categoryID(category.name),
			category.name,
			category.kind,
			now,
			now,
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type seedCategory struct {
	name string
	kind string
}

func seedCategories() []seedCategory {
	return []seedCategory{
		{"Makan & Minum", "expense"},
		{"Transport", "expense"},
		{"Belanja Harian", "expense"},
		{"Tagihan", "expense"},
		{"Hiburan", "expense"},
		{"Kesehatan", "expense"},
		{"Pendidikan", "expense"},
		{"Income", "income"},
		{"Transfer", "transfer"},
		{"Lainnya", "expense"},
	}
}

func categoryID(name string) string {
	value := strings.ToLower(name)
	value = strings.ReplaceAll(value, "&", "and")
	value = strings.ReplaceAll(value, " ", "_")
	return "cat_" + strings.Trim(value, "_")
}

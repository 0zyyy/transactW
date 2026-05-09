package persistence

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"transactw/internal/conversation"
	"transactw/internal/inference"
)

const (
	defaultUserID    = "usr_default"
	defaultAccountID = "acct_cash_default"
)

type Store struct {
	db  *sql.DB
	ttl time.Duration
}

type InboundMessage struct {
	Provider        string
	SessionName     string
	ConversationKey string
	ChatID          string
	SenderID        string
	MessageID       string
	MessageType     string
	Body            string
	ProviderTime    time.Time
}

type OutboundMessage struct {
	Provider        string
	SessionName     string
	ConversationKey string
	ChatID          string
	MessageID       string
	Body            string
}

type ReceiptUpload struct {
	ID                string
	UserID            string
	ConversationKey   string
	ProviderMessageID string
	ImageHash         string
	MimeType          string
	DraftID           string
	TransactionID     string
	Status            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func Open(ctx context.Context, dsn string, ttl time.Duration) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	store := &Store{db: db, ttl: ttl}
	if err := store.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := store.seed(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) RecordInbound(ctx context.Context, msg InboundMessage) (bool, error) {
	if msg.MessageType == "" {
		msg.MessageType = "text"
	}
	if err := s.ensureConversation(ctx, msg.Provider, msg.SessionName, msg.ConversationKey, msg.ChatID, msg.SenderID); err != nil {
		return false, err
	}
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_messages
			(id, conversation_key, provider_message_id, direction, message_type, body, status, provider_timestamp, created_at)
		 VALUES ($1, $2, $3, 'inbound', $4, $5, 'received', $6, $7)
		 ON CONFLICT (conversation_key, provider_message_id, direction) DO NOTHING`,
		newID("wam"),
		msg.ConversationKey,
		msg.MessageID,
		msg.MessageType,
		msg.Body,
		nullTime(msg.ProviderTime),
		time.Now().UTC(),
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 0, nil
}

func (s *Store) RecordOutbound(ctx context.Context, msg OutboundMessage) error {
	if err := s.ensureConversation(ctx, msg.Provider, msg.SessionName, msg.ConversationKey, msg.ChatID, ""); err != nil {
		return err
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_messages
			(id, conversation_key, provider_message_id, direction, message_type, body, status, created_at)
		 VALUES ($1, $2, $3, 'outbound', 'text', $4, 'sent', $5)
		 ON CONFLICT (conversation_key, provider_message_id, direction)
		 DO UPDATE SET body = excluded.body, status = excluded.status, created_at = excluded.created_at`,
		newID("wam"),
		msg.ConversationKey,
		nullableString(msg.MessageID),
		msg.Body,
		time.Now().UTC(),
	)
	return err
}

func (s *Store) RecordParserRun(ctx context.Context, conversationKey, providerMessageID string, parsed inference.ParseTextResponse) error {
	raw, err := json.Marshal(parsed)
	if err != nil {
		return err
	}
	provider := "unknown"
	model := ""
	if parsed.Raw != nil {
		if value, ok := parsed.Raw["provider"].(string); ok {
			provider = value
		}
		if value, ok := parsed.Raw["model"].(string); ok {
			model = value
		}
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO parser_runs
			(id, conversation_key, provider_message_id, provider, model, parser_version, input_text, output_json, intent, confidence, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, 'succeeded', $11)`,
		newID("prs"),
		conversationKey,
		providerMessageID,
		provider,
		model,
		parserVersion(parsed),
		sourceText(parsed),
		string(raw),
		parsed.Intent,
		parsed.Confidence,
		time.Now().UTC(),
	)
	return err
}

func (s *Store) StartReceiptProcessing(ctx context.Context, conversationKey, providerMessageID, imageHash, mimeType string) (ReceiptUpload, bool, error) {
	if strings.TrimSpace(imageHash) == "" {
		return ReceiptUpload{}, false, errors.New("image hash is required")
	}
	userID, err := s.userIDForConversation(ctx, conversationKey)
	if err != nil {
		return ReceiptUpload{}, false, err
	}
	now := time.Now().UTC()
	receiptID := stableID("rcp", userID+":"+imageHash)
	result, err := s.db.ExecContext(
		ctx,
		`INSERT INTO receipt_uploads
			(id, user_id, conversation_key, provider_message_id, image_hash, mime_type, status, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'processing', $7, $8)
		 ON CONFLICT (user_id, image_hash) DO NOTHING`,
		receiptID,
		userID,
		conversationKey,
		nullableString(providerMessageID),
		imageHash,
		mimeType,
		now,
		now,
	)
	if err != nil {
		return ReceiptUpload{}, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return ReceiptUpload{}, false, err
	}
	if affected > 0 {
		return ReceiptUpload{ID: receiptID, UserID: userID, ConversationKey: conversationKey, ProviderMessageID: providerMessageID, ImageHash: imageHash, MimeType: mimeType, Status: "processing", CreatedAt: now, UpdatedAt: now}, false, nil
	}

	existing, err := s.receiptByUserHash(ctx, userID, imageHash)
	if err != nil {
		return ReceiptUpload{}, false, err
	}
	switch existing.Status {
	case "failed", "cancelled":
		_, err := s.db.ExecContext(
			ctx,
			`UPDATE receipt_uploads
			 SET conversation_key = $1, provider_message_id = $2, mime_type = $3, status = 'processing', updated_at = $4
			 WHERE id = $5`,
			conversationKey,
			nullableString(providerMessageID),
			mimeType,
			now,
			existing.ID,
		)
		if err != nil {
			return ReceiptUpload{}, false, err
		}
		existing.ConversationKey = conversationKey
		existing.ProviderMessageID = providerMessageID
		existing.MimeType = mimeType
		existing.Status = "processing"
		existing.UpdatedAt = now
		return existing, false, nil
	default:
		return existing, true, nil
	}
}

func (s *Store) MarkReceiptFailed(ctx context.Context, conversationKey, imageHash string) error {
	userID, err := s.userIDForConversation(ctx, conversationKey)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(
		ctx,
		`UPDATE receipt_uploads SET status = 'failed', updated_at = $1 WHERE user_id = $2 AND image_hash = $3 AND status = 'processing'`,
		time.Now().UTC(),
		userID,
		imageHash,
	)
	return err
}

func (s *Store) receiptByUserHash(ctx context.Context, userID, imageHash string) (ReceiptUpload, error) {
	var receipt ReceiptUpload
	var providerMessageID sql.NullString
	var mimeType sql.NullString
	var draftID sql.NullString
	var transactionID sql.NullString
	err := s.db.QueryRowContext(
		ctx,
		`SELECT id, user_id, conversation_key, provider_message_id, image_hash, mime_type, draft_id, transaction_id, status, created_at, updated_at
		 FROM receipt_uploads WHERE user_id = $1 AND image_hash = $2`,
		userID,
		imageHash,
	).Scan(&receipt.ID, &receipt.UserID, &receipt.ConversationKey, &providerMessageID, &receipt.ImageHash, &mimeType, &draftID, &transactionID, &receipt.Status, &receipt.CreatedAt, &receipt.UpdatedAt)
	if err != nil {
		return ReceiptUpload{}, err
	}
	receipt.ProviderMessageID = providerMessageID.String
	receipt.MimeType = mimeType.String
	receipt.DraftID = draftID.String
	receipt.TransactionID = transactionID.String
	return receipt, nil
}

func (s *Store) Save(conversationKey string, parsed inference.ParseTextResponse) (conversation.PendingDraft, error) {
	ctx := context.Background()
	now := time.Now().UTC()
	expiresAt := now.Add(s.ttl)
	raw, err := json.Marshal(parsed)
	if err != nil {
		return conversation.PendingDraft{}, err
	}
	draftID := newID("tdf")
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.PendingDraft{}, err
	}
	defer rollbackUnlessDone(tx)

	userID, err := conversationUserID(ctx, tx, conversationKey)
	if err != nil {
		return conversation.PendingDraft{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transaction_drafts SET status = 'expired', updated_at = $1 WHERE conversation_key = $2 AND status = 'pending_confirmation'`, now, conversationKey); err != nil {
		return conversation.PendingDraft{}, err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO transaction_drafts
			(id, conversation_key, user_id, status, intent, type, amount, currency, transaction_date, merchant_name, description, category_hint, account_hint, confidence, raw_json, expires_at, created_at, updated_at)
		 VALUES ($1, $2, $3, 'pending_confirmation', $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)`,
		draftID,
		conversationKey,
		userID,
		parsed.Intent,
		draftType(parsed),
		nullableInt64(parsed.Amount),
		defaultCurrency(parsed.Currency),
		nullableString(parsed.TransactionDate),
		parsed.MerchantName,
		parsed.Description,
		parsed.CategoryHint,
		parsed.AccountHint,
		parsed.Confidence,
		string(raw),
		expiresAt,
		now,
		now,
	); err != nil {
		return conversation.PendingDraft{}, err
	}
	for index, item := range draftItems(parsed) {
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO transaction_draft_items
				(id, draft_id, type, amount, currency, transaction_date, merchant_name, description, category_hint, account_hint, sort_order, created_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
			newID("tdi"),
			draftID,
			item.Type,
			item.Amount,
			defaultCurrency(item.Currency),
			item.TransactionDate,
			item.MerchantName,
			item.Description,
			item.CategoryHint,
			item.AccountHint,
			index,
			now,
		); err != nil {
			return conversation.PendingDraft{}, err
		}
	}
	if imageHash := receiptImageHash(parsed); imageHash != "" {
		if _, err := tx.ExecContext(
			ctx,
			`UPDATE receipt_uploads
			 SET draft_id = $1, status = 'pending_confirmation', parsed_json = $2, updated_at = $3
			 WHERE user_id = $4 AND image_hash = $5 AND status = 'processing'`,
			draftID,
			string(raw),
			now,
			userID,
			imageHash,
		); err != nil {
			return conversation.PendingDraft{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return conversation.PendingDraft{}, err
	}
	return conversation.PendingDraft{ConversationKey: conversationKey, Parsed: parsed, CreatedAt: now, ExpiresAt: expiresAt}, nil
}

func (s *Store) Get(conversationKey string) (conversation.PendingDraft, bool, error) {
	ctx := context.Background()
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.PendingDraft{}, false, err
	}
	defer rollbackUnlessDone(tx)
	draft, _, _, ok, err := loadPendingDraft(ctx, tx, conversationKey, now)
	if err != nil || !ok {
		return draft, ok, err
	}
	if err := tx.Commit(); err != nil {
		return conversation.PendingDraft{}, false, err
	}
	return draft, true, nil
}

func (s *Store) Confirm(conversationKey string) (conversation.PendingDraft, bool, error) {
	ctx := context.Background()
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return conversation.PendingDraft{}, false, err
	}
	defer rollbackUnlessDone(tx)

	draft, draftID, userID, ok, err := loadPendingDraft(ctx, tx, conversationKey, now)
	if err != nil || !ok {
		return conversation.PendingDraft{}, ok, err
	}
	accountID, err := defaultAccountIDFor(ctx, tx, userID)
	if err != nil {
		return conversation.PendingDraft{}, false, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT type, amount, currency, transaction_date, merchant_name, description, category_hint FROM transaction_draft_items WHERE draft_id = $1 ORDER BY sort_order`, draftID)
	if err != nil {
		return conversation.PendingDraft{}, false, err
	}

	var items []inference.TransactionDraft
	for rows.Next() {
		var item inference.TransactionDraft
		if err := rows.Scan(&item.Type, &item.Amount, &item.Currency, &item.TransactionDate, &item.MerchantName, &item.Description, &item.CategoryHint); err != nil {
			rows.Close()
			return conversation.PendingDraft{}, false, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return conversation.PendingDraft{}, false, err
	}
	rows.Close()

	var firstTransactionID string
	for _, item := range items {
		transactionID := newID("txn")
		categoryID, err := categoryIDFor(ctx, tx, item.CategoryHint)
		if err != nil {
			return conversation.PendingDraft{}, false, err
		}
		if _, err := tx.ExecContext(
			ctx,
			`INSERT INTO transactions
				(id, user_id, account_id, category_id, source_draft_id, type, amount, currency, transaction_date, merchant_name, description, source, confirmed_at, created_at, updated_at)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'whatsapp_text', $12, $13, $14)`,
			transactionID,
			userID,
			accountID,
			categoryID,
			draftID,
			defaultType(item.Type),
			item.Amount,
			defaultCurrency(item.Currency),
			item.TransactionDate,
			item.MerchantName,
			item.Description,
			now,
			now,
			now,
		); err != nil {
			return conversation.PendingDraft{}, false, err
		}
		if firstTransactionID == "" {
			firstTransactionID = transactionID
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE transaction_drafts SET status = 'confirmed', confirmed_at = $1, updated_at = $2 WHERE id = $3`, now, now, draftID); err != nil {
		return conversation.PendingDraft{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE receipt_uploads SET status = 'confirmed', transaction_id = $1, updated_at = $2 WHERE draft_id = $3`, nullableString(firstTransactionID), now, draftID); err != nil {
		return conversation.PendingDraft{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return conversation.PendingDraft{}, false, err
	}
	return draft, true, nil
}

func (s *Store) Cancel(conversationKey string) (bool, error) {
	now := time.Now().UTC()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer rollbackUnlessDone(tx)
	result, err := tx.Exec(
		`UPDATE transaction_drafts SET status = 'cancelled', cancelled_at = $1, updated_at = $2 WHERE conversation_key = $3 AND status = 'pending_confirmation' AND expires_at > $4`,
		now,
		now,
		conversationKey,
		now,
	)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if affected > 0 {
		if _, err := tx.Exec(`UPDATE receipt_uploads SET status = 'cancelled', updated_at = $1 WHERE conversation_key = $2 AND status = 'pending_confirmation'`, now, conversationKey); err != nil {
			return false, err
		}
	}
	return affected > 0, tx.Commit()
}

func loadPendingDraft(ctx context.Context, tx *sql.Tx, conversationKey string, now time.Time) (conversation.PendingDraft, string, string, bool, error) {
	var id string
	var userID string
	var raw string
	var createdAt time.Time
	var expiresAt time.Time
	err := tx.QueryRowContext(
		ctx,
		`SELECT id, COALESCE(user_id, ''), raw_json, created_at, expires_at
		 FROM transaction_drafts
		 WHERE conversation_key = $1 AND status = 'pending_confirmation' AND expires_at > $2
		 ORDER BY created_at DESC
		 LIMIT 1
		 FOR UPDATE`,
		conversationKey,
		now,
	).Scan(&id, &userID, &raw, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return conversation.PendingDraft{}, "", "", false, nil
	}
	if err != nil {
		return conversation.PendingDraft{}, "", "", false, err
	}
	var parsed inference.ParseTextResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return conversation.PendingDraft{}, "", "", false, err
	}
	return conversation.PendingDraft{ConversationKey: conversationKey, Parsed: parsed, CreatedAt: createdAt, ExpiresAt: expiresAt}, id, userID, true, nil
}

func (s *Store) ensureConversation(ctx context.Context, provider, sessionName, conversationKey, chatID, senderID string) error {
	if provider == "" {
		provider = "unknown"
	}
	if sessionName == "" {
		sessionName = "default"
	}
	gatewayID := provider + ":" + sessionName
	now := time.Now().UTC()
	userID := defaultUserID
	if senderID != "" {
		resolvedUserID, err := s.ensureIdentityUser(ctx, provider, senderID)
		if err != nil {
			return err
		}
		userID = resolvedUserID
	}
	_, err := s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_gateways (id, provider, session_name, status, created_at, updated_at)
		 VALUES ($1, $2, $3, 'active', $4, $5)
		 ON CONFLICT(id) DO UPDATE SET updated_at = excluded.updated_at`,
		gatewayID,
		provider,
		sessionName,
		now,
		now,
	)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(
		ctx,
		`INSERT INTO whatsapp_conversations (conversation_key, gateway_id, chat_id, user_id, sender_id, state, last_message_at, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, 'idle', $6, $7, $8)
		 ON CONFLICT(conversation_key) DO UPDATE SET last_message_at = excluded.last_message_at, sender_id = COALESCE(NULLIF(excluded.sender_id, ''), whatsapp_conversations.sender_id), updated_at = excluded.updated_at`,
		conversationKey,
		gatewayID,
		chatID,
		userID,
		senderID,
		now,
		now,
		now,
	)
	return err
}

func (s *Store) ensureIdentityUser(ctx context.Context, provider, senderID string) (string, error) {
	now := time.Now().UTC()
	identityID := stableID("wai", provider+":"+senderID)
	userID := stableID("usr", provider+":"+senderID)
	accountID := stableID("acct_cash", userID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer rollbackUnlessDone(tx)
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO users (id, display_name, default_currency, timezone, created_at, updated_at)
		 VALUES ($1, $2, 'IDR', 'Asia/Jakarta', $3, $4)
		 ON CONFLICT (id) DO UPDATE SET updated_at = excluded.updated_at`,
		userID,
		"WhatsApp "+senderID,
		now,
		now,
	); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO accounts (id, user_id, name, type, currency, is_default, created_at, updated_at)
		 VALUES ($1, $2, 'Cash', 'cash', 'IDR', true, $3, $4)
		 ON CONFLICT (id) DO UPDATE SET updated_at = excluded.updated_at`,
		accountID,
		userID,
		now,
		now,
	); err != nil {
		return "", err
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO whatsapp_identities (id, user_id, provider, wa_id, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (provider, wa_id) DO UPDATE SET user_id = excluded.user_id, updated_at = excluded.updated_at`,
		identityID,
		userID,
		provider,
		senderID,
		now,
		now,
	); err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}
	return userID, nil
}

func conversationUserID(ctx context.Context, tx *sql.Tx, conversationKey string) (string, error) {
	var userID string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(user_id, '') FROM whatsapp_conversations WHERE conversation_key = $1`, conversationKey).Scan(&userID)
	if errors.Is(err, sql.ErrNoRows) || userID == "" {
		return defaultUserID, nil
	}
	return userID, err
}

func defaultAccountIDFor(ctx context.Context, tx *sql.Tx, userID string) (string, error) {
	var accountID string
	err := tx.QueryRowContext(ctx, `SELECT id FROM accounts WHERE user_id = $1 AND is_default = true AND archived_at IS NULL ORDER BY created_at LIMIT 1`, userID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return defaultAccountID, nil
	}
	return accountID, err
}

func categoryIDFor(ctx context.Context, tx *sql.Tx, name string) (sql.NullString, error) {
	if strings.TrimSpace(name) == "" {
		name = "Lainnya"
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM categories WHERE user_id IS NULL AND name = $1 LIMIT 1`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return sql.NullString{}, nil
	}
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: id, Valid: true}, nil
}

func rollbackUnlessDone(tx *sql.Tx) {
	_ = tx.Rollback()
}

func parserVersion(parsed inference.ParseTextResponse) string {
	if parsed.Raw == nil {
		return ""
	}
	if value, ok := parsed.Raw["parser_version"].(string); ok {
		return value
	}
	return ""
}

func sourceText(parsed inference.ParseTextResponse) string {
	if parsed.Raw == nil {
		return ""
	}
	if value, ok := parsed.Raw["normalized_text"].(string); ok {
		return value
	}
	if value, ok := parsed.Raw["original_text"].(string); ok {
		return value
	}
	return parsed.Description
}

func receiptImageHash(parsed inference.ParseTextResponse) string {
	if parsed.Raw == nil {
		return ""
	}
	if value, ok := parsed.Raw["image_hash"].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func draftType(parsed inference.ParseTextResponse) string {
	if parsed.Intent == "create_income" {
		return "income"
	}
	return "expense"
}

func draftItems(parsed inference.ParseTextResponse) []inference.TransactionDraft {
	if len(parsed.Transactions) > 0 {
		return parsed.Transactions
	}
	amount := int64(0)
	if parsed.Amount != nil {
		amount = *parsed.Amount
	}
	return []inference.TransactionDraft{{
		Type:            draftType(parsed),
		Amount:          amount,
		Currency:        defaultCurrency(parsed.Currency),
		MerchantName:    parsed.MerchantName,
		Description:     parsed.Description,
		CategoryHint:    parsed.CategoryHint,
		AccountHint:     parsed.AccountHint,
		TransactionDate: parsed.TransactionDate,
	}}
}

func nullableInt64(value *int64) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func defaultCurrency(value string) string {
	if value == "" {
		return "IDR"
	}
	return value
}

func defaultType(value string) string {
	if value == "" {
		return "expense"
	}
	return value
}

func newID(prefix string) string {
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func stableID(prefix, value string) string {
	sum := sha1.Sum([]byte(value))
	return fmt.Sprintf("%s_%x", prefix, sum[:8])
}

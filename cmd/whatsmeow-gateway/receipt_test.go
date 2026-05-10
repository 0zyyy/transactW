//go:build whatsmeow

package main

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	waProto "go.mau.fi/whatsmeow/binary/proto"
	"go.mau.fi/whatsmeow/types"

	"transactw/internal/config"
	"transactw/internal/inference"
	"transactw/internal/persistence"
)

type fakeWhatsmeowClient struct {
	sent []string
}

func (f *fakeWhatsmeowClient) Download(context.Context, whatsmeow.DownloadableMessage) ([]byte, error) {
	return nil, errors.New("download should not be called by receipt worker tests")
}

func (f *fakeWhatsmeowClient) SendMessage(_ context.Context, _ types.JID, message *waProto.Message, _ ...whatsmeow.SendRequestExtra) (whatsmeow.SendResponse, error) {
	f.sent = append(f.sent, message.GetConversation())
	return whatsmeow.SendResponse{}, nil
}

type fakeInferenceClient struct {
	receipt inference.ParseTextResponse
	err     error
}

func (f fakeInferenceClient) ParseText(context.Context, inference.ParseTextRequest) (inference.ParseTextResponse, error) {
	return inference.ParseTextResponse{}, errors.New("ParseText should not be called by receipt worker tests")
}

func (f fakeInferenceClient) ParseReceipt(context.Context, inference.ParseReceiptRequest) (inference.ParseTextResponse, error) {
	if f.err != nil {
		return inference.ParseTextResponse{}, f.err
	}
	return f.receipt, nil
}

func (f fakeInferenceClient) TranscribeAudio(context.Context, inference.TranscribeAudioRequest) (inference.TranscribeAudioResponse, error) {
	return inference.TranscribeAudioResponse{}, errors.New("TranscribeAudio should not be called by receipt worker tests")
}

func TestProcessReceiptJobCreatesDraftAndCleansTempFile(t *testing.T) {
	g, fakeClient, db, sqlDB, conversationKey, job := setupReceiptWorkerTest(t, "success", "receipt-worker-success")
	amount := int64(25000)
	g.inference = fakeInferenceClient{receipt: inference.ParseTextResponse{
		Intent:          "create_expense",
		Action:          "create_draft",
		Amount:          &amount,
		Currency:        "IDR",
		Description:     "receipt worker nasi padang",
		CategoryHint:    "Makan & Minum",
		TransactionDate: "2026-04-29",
		Confidence:      0.92,
	}}

	g.processReceiptJob(context.Background(), 1, job)

	if status := mediaJobStatus(t, sqlDB, job.ID); status != "succeeded" {
		t.Fatalf("media job status = %q, want succeeded", status)
	}
	if _, err := os.Stat(job.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file should be removed, stat err = %v", err)
	}
	if len(fakeClient.sent) != 1 || !strings.Contains(fakeClient.sent[0], "*Draft pengeluaran*") {
		t.Fatalf("sent replies = %#v, want formatted draft reply", fakeClient.sent)
	}
	draft, ok, err := db.Get(conversationKey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || draft.Parsed.Description != "receipt worker nasi padang" {
		t.Fatalf("draft = (%v, %#v), want saved receipt draft", ok, draft)
	}
}

func TestReceiptReplyFormatting(t *testing.T) {
	if got := duplicateReceiptReply(persistence.ReceiptUpload{Status: "pending_confirmation"}); !strings.Contains(got, "*Struk sudah jadi draft*") || !strings.Contains(got, "*simpan*") {
		t.Fatalf("pending duplicate reply = %q", got)
	}
	if got := duplicateReceiptReply(persistence.ReceiptUpload{Status: "processing"}); !strings.Contains(got, "*Struk sedang diproses*") {
		t.Fatalf("processing duplicate reply = %q", got)
	}
	if got := unreadableReceiptReply(); !strings.Contains(got, "*Struk belum kebaca jelas*") {
		t.Fatalf("unreadable reply = %q", got)
	}
}

func TestProcessReceiptJobRetriesThenFailsTerminally(t *testing.T) {
	g, fakeClient, _, sqlDB, _, job := setupReceiptWorkerTest(t, "failure", "receipt-worker-failure")
	g.inference = fakeInferenceClient{err: errors.New("vision unavailable")}

	g.processReceiptJob(context.Background(), 1, job)
	if status := mediaJobStatus(t, sqlDB, job.ID); status != "queued" {
		t.Fatalf("first failure status = %q, want queued", status)
	}
	if _, err := os.Stat(job.StoragePath); err != nil {
		t.Fatalf("temp file should remain for retry: %v", err)
	}
	if len(fakeClient.sent) != 0 {
		t.Fatalf("retryable failure should not send reply: %#v", fakeClient.sent)
	}

	retried, ok, err := g.db.NextMediaJob(context.Background(), receiptImageMediaType)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected queued retry job")
	}
	g.processReceiptJob(context.Background(), 1, retried)
	if status := mediaJobStatus(t, sqlDB, job.ID); status != "failed" {
		t.Fatalf("terminal failure status = %q, want failed", status)
	}
	if _, err := os.Stat(job.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file should be removed after terminal failure, stat err = %v", err)
	}
	if len(fakeClient.sent) != 1 || !strings.Contains(fakeClient.sent[0], "*Struk belum kebaca jelas*") {
		t.Fatalf("sent replies = %#v, want unreadable receipt reply", fakeClient.sent)
	}
}

func TestProcessReceiptJobSendsDuplicateReceiptReply(t *testing.T) {
	g, fakeClient, db, sqlDB, conversationKey, job := setupReceiptWorkerTest(t, "duplicate", "receipt-worker-duplicate")
	if _, duplicate, err := db.StartReceiptProcessing(context.Background(), conversationKey, "wamid.existing.duplicate", job.MediaHash, job.MimeType); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("existing receipt setup should not be duplicate")
	}
	amount := int64(50000)
	if _, err := db.Save(conversationKey, inference.ParseTextResponse{
		Intent:          "create_expense",
		Action:          "create_draft",
		Amount:          &amount,
		Currency:        "IDR",
		Description:     "existing duplicate receipt",
		TransactionDate: "2026-04-29",
		Raw:             map[string]any{"image_hash": job.MediaHash},
	}); err != nil {
		t.Fatal(err)
	}

	g.processReceiptJob(context.Background(), 1, job)

	if status := mediaJobStatus(t, sqlDB, job.ID); status != "succeeded" {
		t.Fatalf("duplicate job status = %q, want succeeded", status)
	}
	if len(fakeClient.sent) != 1 || !strings.Contains(fakeClient.sent[0], "*Struk sudah jadi draft*") {
		t.Fatalf("sent replies = %#v, want duplicate draft reply", fakeClient.sent)
	}
	if _, err := os.Stat(job.StoragePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temp file should be removed, stat err = %v", err)
	}
}

func TestProcessReceiptJobHandlesNonReceiptImage(t *testing.T) {
	g, fakeClient, _, sqlDB, _, job := setupReceiptWorkerTest(t, "nonreceipt", "receipt-worker-nonreceipt")
	g.inference = fakeInferenceClient{receipt: inference.ParseTextResponse{Intent: "unknown", Action: "none", Confidence: 0.1}}

	g.processReceiptJob(context.Background(), 1, job)

	if status := mediaJobStatus(t, sqlDB, job.ID); status != "succeeded" {
		t.Fatalf("non-receipt job status = %q, want succeeded", status)
	}
	if len(fakeClient.sent) != 1 || !strings.Contains(fakeClient.sent[0], "*Struk belum kebaca jelas*") {
		t.Fatalf("sent replies = %#v, want unreadable receipt reply", fakeClient.sent)
	}
	if receiptStatus := receiptStatus(t, sqlDB, job.MediaHash); receiptStatus != "failed" {
		t.Fatalf("receipt status = %q, want failed", receiptStatus)
	}
}

func setupReceiptWorkerTest(t *testing.T, name, messagePrefix string) (gateway, *fakeWhatsmeowClient, *persistence.Store, *sql.DB, string, persistence.MediaJob) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	db, err := persistence.Open(ctx, dsn, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sqlDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	stamp := time.Unix(0, time.Now().UnixNano()).Format("150405.000000000")
	chatID := "628" + strings.ReplaceAll(stamp, ".", "") + "@s.whatsapp.net"
	conversationKey := "whatsmeow:test:" + chatID
	messageID := "wamid." + messagePrefix + "." + stamp
	if duplicate, err := db.RecordInbound(ctx, persistence.InboundMessage{
		Provider:        "whatsmeow",
		SessionName:     "test",
		ConversationKey: conversationKey,
		ChatID:          chatID,
		SenderID:        chatID,
		MessageID:       messageID,
		MessageType:     "image",
	}); err != nil {
		t.Fatal(err)
	} else if duplicate {
		t.Fatal("first inbound should not be duplicate")
	}

	storagePath, mediaHash, err := writeTempMedia(t.TempDir(), receiptImageMediaType, messageID, []byte("receipt image "+name))
	if err != nil {
		t.Fatal(err)
	}
	created, duplicate, err := db.CreateMediaJob(ctx, persistence.CreateMediaJobInput{
		ConversationKey:   conversationKey,
		ProviderMessageID: messageID,
		ChatID:            chatID,
		MediaType:         receiptImageMediaType,
		MimeType:          "image/jpeg",
		StoragePath:       storagePath,
		MediaHash:         mediaHash,
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate {
		t.Fatal("first media job should not be duplicate")
	}
	claimed, ok, err := db.NextMediaJob(ctx, receiptImageMediaType)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || claimed.ID != created.ID {
		t.Fatalf("claimed job = (%v, %q), want %q", ok, claimed.ID, created.ID)
	}

	fakeClient := &fakeWhatsmeowClient{}
	g := gateway{
		client: fakeClient,
		cfg: config.Config{
			MediaJobMaxAttempts: 2,
		},
		inference: fakeInferenceClient{},
		store:     db,
		db:        db,
		logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	return g, fakeClient, db, sqlDB, conversationKey, claimed
}

func mediaJobStatus(t *testing.T, db *sql.DB, jobID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM media_jobs WHERE id = $1`, jobID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

func receiptStatus(t *testing.T, db *sql.DB, imageHash string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM receipt_uploads WHERE image_hash = $1 ORDER BY updated_at DESC LIMIT 1`, imageHash).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

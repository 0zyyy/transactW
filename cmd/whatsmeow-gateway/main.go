//go:build whatsmeow

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store/sqlstore"
	waLog "go.mau.fi/whatsmeow/util/log"
	_ "modernc.org/sqlite"

	"transactw/internal/config"
	"transactw/internal/conversation"
	"transactw/internal/inference"
	"transactw/internal/persistence"
)

type gateway struct {
	client    *whatsmeow.Client
	cfg       config.Config
	inference inference.Client
	store     conversation.DraftStore
	db        *persistence.Store
	logger    *slog.Logger
}

func main() {
	cfg := config.Load()
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	ctx := context.Background()
	db, err := persistence.Open(ctx, cfg.DatabaseDSN, 30*time.Minute)
	if err != nil {
		logger.Error("failed to open persistence database", "dsn", cfg.DatabaseDSN, "error", err)
		os.Exit(1)
	}
	defer db.Close()

	storePath := getenv("WHATSMEOW_STORE_PATH", "file:whatsmeow-session.db?_pragma=foreign_keys(1)")
	dbLog := waLog.Stdout("WhatsmeowDB", getenv("WHATSMEOW_LOG_LEVEL", "INFO"), true)
	container, err := sqlstore.New(ctx, "sqlite", storePath, dbLog)
	if err != nil {
		logger.Error("failed to open whatsmeow store", "error", err)
		os.Exit(1)
	}

	deviceStore, err := container.GetFirstDevice(ctx)
	if err != nil {
		logger.Error("failed to load whatsmeow device", "error", err)
		os.Exit(1)
	}

	clientLog := waLog.Stdout("Whatsmeow", getenv("WHATSMEOW_LOG_LEVEL", "INFO"), true)
	client := whatsmeow.NewClient(deviceStore, clientLog)
	gw := gateway{
		client:    client,
		cfg:       cfg,
		inference: inference.NewClient(cfg.InferenceURL, cfg.InferenceTimeout),
		store:     db,
		db:        db,
		logger:    logger,
	}
	client.AddEventHandler(gw.handleEvent)

	if client.Store.ID == nil {
		qrChan, err := client.GetQRChannel(ctx)
		if err != nil {
			logger.Error("failed to create QR channel", "error", err)
			os.Exit(1)
		}
		if err := client.Connect(); err != nil {
			logger.Error("failed to connect whatsmeow", "error", err)
			os.Exit(1)
		}
		for evt := range qrChan {
			if evt.Event == "code" {
				logger.Info("scan this QR with WhatsApp linked devices")
				qrterminal.GenerateHalfBlock(evt.Code, qrterminal.L, os.Stdout)
			} else {
				logger.Info("whatsmeow login event", "event", evt.Event)
			}
		}
	} else {
		if err := client.Connect(); err != nil {
			logger.Error("failed to connect whatsmeow", "error", err)
			os.Exit(1)
		}
	}

	logger.Info("whatsmeow gateway started", "inference_url", cfg.InferenceURL)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	logger.Info("stopping whatsmeow gateway")
	client.Disconnect()
}

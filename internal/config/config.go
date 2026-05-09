package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Port                   string
	WhatsAppVerifyToken    string
	WhatsAppAccessToken    string
	WhatsAppPhoneID        string
	WhatsAppGraphAPI       string
	DebugJSONReplies       bool
	InferenceURL           string
	InferenceTimeout       time.Duration
	DatabaseDSN            string
	MediaTempDir           string
	MediaQueueMaxPending   int
	MediaJobMaxAttempts    int
	VoiceNoteEnabled       bool
	VoiceWorkerConcurrency int
}

func Load() Config {
	return Config{
		Port:                   getenv("PORT", "8080"),
		WhatsAppVerifyToken:    os.Getenv("WHATSAPP_VERIFY_TOKEN"),
		WhatsAppAccessToken:    os.Getenv("WHATSAPP_ACCESS_TOKEN"),
		WhatsAppPhoneID:        os.Getenv("WHATSAPP_PHONE_NUMBER_ID"),
		WhatsAppGraphAPI:       getenv("WHATSAPP_GRAPH_API_VERSION", "v21.0"),
		DebugJSONReplies:       getenvBool("DEBUG_JSON_REPLIES", false),
		InferenceURL:           getenv("INFERENCE_URL", "http://127.0.0.1:8090"),
		InferenceTimeout:       time.Duration(getenvInt("INFERENCE_TIMEOUT_SECONDS", 15)) * time.Second,
		DatabaseDSN:            getenvAny([]string{"DATABASE_URL", "DATABASE_DSN"}, "postgres://postgres:postgres@127.0.0.1:5432/db_transactw?sslmode=disable"),
		MediaTempDir:           getenv("MEDIA_TEMP_DIR", "tmp/media"),
		MediaQueueMaxPending:   getenvInt("MEDIA_QUEUE_MAX_PENDING", 100),
		MediaJobMaxAttempts:    getenvInt("MEDIA_JOB_MAX_ATTEMPTS", 3),
		VoiceNoteEnabled:       getenvBool("VOICE_NOTE_ENABLED", true),
		VoiceWorkerConcurrency: getenvInt("VOICE_WORKER_CONCURRENCY", 2),
	}
}

func getenvAny(keys []string, fallback string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return fallback
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getenvBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	switch value {
	case "1", "true", "TRUE", "yes", "YES", "on", "ON":
		return true
	case "0", "false", "FALSE", "no", "NO", "off", "OFF":
		return false
	default:
		return fallback
	}
}

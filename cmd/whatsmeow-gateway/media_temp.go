//go:build whatsmeow

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func writeTempMedia(dir, mediaType, providerMessageID string, data []byte) (string, string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(data)
	mediaHash := hex.EncodeToString(sum[:])
	safeMessageID := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "@", "_").Replace(providerMessageID)
	if safeMessageID == "" {
		safeMessageID = mediaHash[:16]
	}
	filename := fmt.Sprintf("%s-%s-%s.bin", mediaType, safeMessageID, mediaHash[:16])
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", "", err
	}
	return path, mediaHash, nil
}

func removeTempMedia(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	_ = os.Remove(path)
}

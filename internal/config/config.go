package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

type Config struct {
	DataDir       string
	MasterKeyFile string
	STRMDir       string
	PendingDir    string
	UploadDir     string
	ListenAddr    string
	GatewayAddr   string
	CMSLegacyAddr string
	AdminUser     string
	AdminPassword string
	SessionTTL    time.Duration
	PublicURL     string
	LogLevel      string
}

func Load() Config {
	dataDir := env("DATA_DIR", "./data")
	return Config{
		DataDir:       dataDir,
		MasterKeyFile: env("MASTER_KEY_PATH", filepath.Join(dataDir, "master.key")),
		STRMDir:       env("STRM_PATH", filepath.Join(dataDir, "strm")),
		PendingDir:    env("PENDING_PATH", filepath.Join(dataDir, "pending")),
		UploadDir:     env("UPLOAD_PATH", filepath.Join(dataDir, "uploads")),
		ListenAddr:    env("LISTEN_ADDR", ":9527"),
		GatewayAddr:   env("GATEWAY_ADDR", ":9096"),
		CMSLegacyAddr: env("CMS_LEGACY_ADDR", ":9528"),
		AdminUser:     env("ADMIN_USERNAME", "admin"),
		AdminPassword: env("ADMIN_PASSWORD", ""),
		SessionTTL:    time.Duration(envInt("SESSION_TTL_HOURS", 12)) * time.Hour,
		PublicURL:     env("PUBLIC_GATEWAY_URL", "http://127.0.0.1:9096"),
		LogLevel:      env("LOG_LEVEL", "info"),
	}
}

func (c Config) Prepare() error {
	for _, dir := range []string{c.DataDir, filepath.Dir(c.MasterKeyPath()), c.STRMDir, c.PendingDir, c.UploadDir} {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, "115-direct.db") }
func (c Config) MasterKeyPath() string {
	if c.MasterKeyFile != "" {
		return c.MasterKeyFile
	}
	return filepath.Join(c.DataDir, "master.key")
}
func (c Config) STRMPath() string { return c.STRMDir }

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(os.Getenv(key))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

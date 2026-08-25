// Package config reads the service settings from the environment.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	HTTPAddr        string
	MongoURI        string
	MongoDatabase   string
	CSVPath         string
	StartupTimeout  time.Duration
	ImportTimeout   time.Duration
	RequestTimeout  time.Duration
	ShutdownTimeout time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:      envOrDefault("HTTP_ADDR", ":8080"),
		MongoURI:      envOrDefault("MONGO_URI", "mongodb://localhost:27017"),
		MongoDatabase: envOrDefault("MONGO_DB", "billing-test-assignment"),
		CSVPath:       strings.TrimSpace(os.Getenv("CSV_PATH")),
	}

	var err error
	cfg.StartupTimeout, err = envDuration("STARTUP_TIMEOUT", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.ImportTimeout, err = envDuration("IMPORT_TIMEOUT", 10*time.Minute)
	if err != nil {
		return Config{}, err
	}
	cfg.RequestTimeout, err = envDuration("REQUEST_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	cfg.ShutdownTimeout, err = envDuration("SHUTDOWN_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func envOrDefault(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}

	value, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s=%q as duration: %w", key, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%s must be positive, got %q", key, raw)
	}
	return value, nil
}

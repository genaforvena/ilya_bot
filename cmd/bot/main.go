package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/genaforvena/ilya_bot/internal/application"
	"github.com/genaforvena/ilya_bot/internal/infrastructure"
	"github.com/genaforvena/ilya_bot/internal/transport"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg := loadConfig()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	db, err := infrastructure.NewDB(ctx, cfg.databaseURL)
	if err != nil {
		slog.Error("failed to connect to database", "err", err)
		os.Exit(1)
	}
	defer db.Close()

	tg := infrastructure.NewTelegramClient(cfg.telegramToken)
	llm := infrastructure.NewLLMClient(cfg.deepSeekAPIKey, cfg.llmEnabled)

	appHandler := application.NewHandler(db, llm, tg, cfg.candidateTelegramID)
	webhookHandler := transport.NewWebhookHandler(cfg.telegramSecret, appHandler)

	mux := http.NewServeMux()
	mux.Handle("/webhook", webhookHandler)
	mux.HandleFunc("/health", transport.HealthHandler)

	srv := &http.Server{
		Addr:         ":" + cfg.port,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("starting server", "port", cfg.port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("server error", "err", err)
			os.Exit(1)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutting down server...")
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("server shutdown error", "err", err)
	}
	slog.Info("server stopped")
}

type config struct {
	telegramToken       string
	telegramSecret      string
	databaseURL         string
	deepSeekAPIKey      string
	candidateTelegramID int64
	llmEnabled          bool
	port                string
}

func loadConfig() config {
	cfg := config{
		telegramToken:  mustEnv("TELEGRAM_BOT_TOKEN"),
		telegramSecret: getEnv("TELEGRAM_SECRET", ""),
		databaseURL:    mustEnv("DATABASE_URL"),
		deepSeekAPIKey:  getEnv("DEEPSEA_API_KEY", ""),
		llmEnabled:     getEnvBool("LLM_ENABLED", true),
		port:           getEnv("PORT", "8080"),
	}

	candidateIDStr := mustEnv("CANDIDATE_TELEGRAM_ID")
	id, err := strconv.ParseInt(candidateIDStr, 10, 64)
	if err != nil {
		slog.Error("invalid CANDIDATE_TELEGRAM_ID", "err", err)
		os.Exit(1)
	}
	cfg.candidateTelegramID = id
	return cfg
}

func mustEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "key", key)
		os.Exit(1)
	}
	return v
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return fallback
	}
	return b
}

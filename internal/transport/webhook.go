package transport

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/genaforvena/ilya_bot/internal/domain"
)

// MessageHandler processes a Telegram message.
type MessageHandler interface {
	HandleMessage(ctx context.Context, msg *domain.TelegramMessage)
}

// WebhookHandler is the HTTP handler for the Telegram webhook.
type WebhookHandler struct {
	secret  string
	handler MessageHandler
}

// NewWebhookHandler creates a new WebhookHandler.
func NewWebhookHandler(secret string, handler MessageHandler) *WebhookHandler {
	return &WebhookHandler{secret: secret, handler: handler}
}

// ServeHTTP handles POST /webhook requests from Telegram.
func (wh *WebhookHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate secret token
	if wh.secret != "" {
		token := r.Header.Get("X-Telegram-Bot-Api-Secret-Token")
		if token != wh.secret {
			slog.Warn("invalid webhook secret", "remote", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	var update domain.TelegramUpdate
	if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
		slog.Error("decode update", "err", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	// Respond 200 immediately; process asynchronously.
	w.WriteHeader(http.StatusOK)

	if update.Message == nil || update.Message.Text == "" || update.Message.From == nil {
		return
	}

	msg := update.Message
	go func() {
		wh.handler.HandleMessage(context.Background(), msg)
	}()
}

// HealthHandler returns 200 OK for liveness checks.
func HealthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

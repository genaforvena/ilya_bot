package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const telegramBaseURL = "https://api.telegram.org"

// TelegramClient sends messages via the Telegram Bot API.
type TelegramClient struct {
	token      string
	httpClient *http.Client
}

// NewTelegramClient creates a new TelegramClient.
func NewTelegramClient(token string) *TelegramClient {
	return &TelegramClient{
		token: token,
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
		},
	}
}

type sendMessageRequest struct {
	ChatID    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode,omitempty"`
}

// SendMessage sends a text message to the given chat ID.
func (c *TelegramClient) SendMessage(ctx context.Context, chatID int64, text string) error {
	payload := sendMessageRequest{
		ChatID: chatID,
		Text:   text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal sendMessage: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramBaseURL, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("sendMessage HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("telegram sendMessage non-200", "status", resp.StatusCode, "chat_id", chatID)
		return fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}
	return nil
}

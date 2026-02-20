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

type sendMessageResponse struct {
	OK     bool `json:"ok"`
	Result struct {
		MessageID int `json:"message_id"`
	} `json:"result"`
}

// SendMessage sends a text message and returns the Telegram message_id.
func (c *TelegramClient) SendMessage(ctx context.Context, chatID int64, text string) (int, error) {
	payload := sendMessageRequest{
		ChatID: chatID,
		Text:   text,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal sendMessage: %w", err)
	}

	url := fmt.Sprintf("%s/bot%s/sendMessage", telegramBaseURL, c.token)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("create sendMessage request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("sendMessage HTTP: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		slog.Error("telegram sendMessage non-200", "status", resp.StatusCode, "chat_id", chatID)
		return 0, fmt.Errorf("telegram API returned status %d", resp.StatusCode)
	}

	var tgResp sendMessageResponse
	if err := json.NewDecoder(resp.Body).Decode(&tgResp); err != nil {
		return 0, fmt.Errorf("decode sendMessage response: %w", err)
	}
	return tgResp.Result.MessageID, nil
}

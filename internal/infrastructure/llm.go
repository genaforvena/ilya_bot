package infrastructure

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/genaforvena/ilya_bot/internal/domain"
)

const deepSeekURL = "https://api.deepseek.com/v1/chat/completions"
const deepSeekModel = "deepseek-chat"

const intentSystemPrompt = `You are an intent classifier for a Telegram recruitment bot. Analyze the recruiter's message and respond ONLY with valid JSON matching this exact schema:
{"intent":"schedule|question|smalltalk|unknown","confidence":0.0,"proposed_time_window":{"start":null,"end":null},"question_topic":null}
intent must be one of: schedule, question, smalltalk, unknown
confidence must be a float between 0 and 1
proposed_time_window.start and end must be RFC3339 strings or null
question_topic must be one of: experience, tech_stack, availability, salary, relocation, other, null
Respond ONLY with JSON. No explanation.`

const responseSystemPrompt = `You are Ilya's scheduling assistant bot. You help recruiters schedule interviews with Ilya.
Candidate profile: Ilya is a senior Go developer with 8+ years of experience.
Available slots will be provided. Be professional and concise.`

// LLMClient calls the DeepSeek API for intent classification and response generation.
type LLMClient struct {
	apiKey     string
	httpClient *http.Client
	enabled    bool
}

// NewLLMClient creates a new LLMClient.
func NewLLMClient(apiKey string, enabled bool) *LLMClient {
	return &LLMClient{
		apiKey:  apiKey,
		enabled: enabled,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
}

func (c *LLMClient) chat(ctx context.Context, systemPrompt, userMessage string) (string, error) {
	req := chatRequest{
		Model: deepSeekModel,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userMessage},
		},
	}
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal chat request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, deepSeekURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("create LLM request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("LLM HTTP call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("LLM API status %d", resp.StatusCode)
	}

	var chatResp chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&chatResp); err != nil {
		return "", fmt.Errorf("decode LLM response: %w", err)
	}
	if len(chatResp.Choices) == 0 {
		return "", fmt.Errorf("LLM returned no choices")
	}
	return chatResp.Choices[0].Message.Content, nil
}

// ClassifyIntent calls the LLM to classify recruiter intent.
// Returns nil intent and an error if LLM is disabled or fails.
func (c *LLMClient) ClassifyIntent(ctx context.Context, message string) (*domain.Intent, error) {
	if !c.enabled {
		return nil, fmt.Errorf("LLM disabled")
	}

	raw, err := c.chat(ctx, intentSystemPrompt, message)
	if err != nil {
		return nil, fmt.Errorf("classify intent chat: %w", err)
	}

	// Strip markdown code fences if present
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) >= 3 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}

	// Unmarshal into a raw map first to handle nullable fields
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &rawMap); err != nil {
		slog.Error("intent JSON parse failed", "raw", raw, "err", err)
		return nil, fmt.Errorf("parse intent JSON: %w", err)
	}

	intent := &domain.Intent{}
	if v, ok := rawMap["intent"]; ok {
		_ = json.Unmarshal(v, &intent.Intent)
	}
	if v, ok := rawMap["confidence"]; ok {
		_ = json.Unmarshal(v, &intent.Confidence)
	}
	if v, ok := rawMap["question_topic"]; ok {
		var topic *string
		if err := json.Unmarshal(v, &topic); err == nil {
			intent.QuestionTopic = topic
		}
	}
	if v, ok := rawMap["proposed_time_window"]; ok {
		var tw struct {
			Start *string `json:"start"`
			End   *string `json:"end"`
		}
		if err := json.Unmarshal(v, &tw); err == nil {
			if tw.Start != nil {
				if t, err := time.Parse(time.RFC3339, *tw.Start); err == nil {
					intent.ProposedTimeWindow.Start = &t
				}
			}
			if tw.End != nil {
				if t, err := time.Parse(time.RFC3339, *tw.End); err == nil {
					intent.ProposedTimeWindow.End = &t
				}
			}
		}
	}

	return intent, nil
}

// GenerateResponse calls the LLM to produce a natural language reply.
func (c *LLMClient) GenerateResponse(ctx context.Context, userMessage, context_ string) (string, error) {
	if !c.enabled {
		return "", fmt.Errorf("LLM disabled")
	}
	prompt := userMessage
	if context_ != "" {
		prompt = context_ + "\n\nRecruiter message: " + userMessage
	}
	return c.chat(ctx, responseSystemPrompt, prompt)
}

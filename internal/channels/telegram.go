package channels

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"smithly.dev/internal/agent"
)

const (
	telegramMaxMessage = 4096
	telegramPollTimeout = 30
)

// Telegram is a channel adapter that receives messages via Telegram Bot API long polling.
type Telegram struct {
	Token       string
	Agent       *agent.Agent
	AutoApprove bool
	BaseURL     string // override for testing (default "https://api.telegram.org/bot")
	client      *http.Client
	offset      int
	cancel      context.CancelFunc // set by Start, called by Stop
}

// NewTelegram creates a Telegram channel adapter for the given agent.
func NewTelegram(token string, a *agent.Agent, autoApprove bool) *Telegram {
	return &Telegram{
		Token:       token,
		Agent:       a,
		AutoApprove: autoApprove,
	}
}

// Start implements Channel. It verifies the bot token and polls for updates until ctx is cancelled or Stop is called.
func (t *Telegram) Start(ctx context.Context) error {
	ctx, t.cancel = context.WithCancel(ctx)

	if t.BaseURL == "" {
		t.BaseURL = "https://api.telegram.org/bot"
	}
	if t.client == nil {
		t.client = &http.Client{Timeout: time.Duration(telegramPollTimeout+10) * time.Second}
	}

	// Verify token with getMe
	me, err := t.getMe(ctx)
	if err != nil {
		return fmt.Errorf("telegram getMe: %w", err)
	}
	slog.Info("telegram bot connected", "username", me)

	// Long-polling loop
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		updates, err := t.getUpdates(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			slog.Error("telegram getUpdates failed, retrying", "err", err)
			select {
			case <-time.After(5 * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}

		for _, u := range updates {
			if u.UpdateID >= t.offset {
				t.offset = u.UpdateID + 1
			}
			if u.Message == nil || u.Message.Text == "" {
				continue
			}
			t.handleMessage(ctx, u.Message)
		}
	}
}

// Stop implements Channel. It cancels the polling loop started by Start.
func (t *Telegram) Stop() error {
	if t.cancel != nil {
		t.cancel()
	}
	return nil
}

func (t *Telegram) handleMessage(ctx context.Context, msg *tgMessage) {
	chatID := msg.Chat.ID

	// Send typing indicator
	t.sendChatAction(ctx, chatID, "typing")

	cb := &agent.Callbacks{
		Source: "channel:telegram",
		Approve: func(toolName string, description string) bool {
			return t.AutoApprove
		},
	}

	response, err := t.Agent.Chat(ctx, msg.Text, cb)
	if err != nil {
		slog.Error("telegram chat error", "chat_id", chatID, "err", err)
		t.sendMessage(ctx, chatID, fmt.Sprintf("Error: %v", err))
		return
	}

	t.sendLongMessage(ctx, chatID, response)
}

// sendLongMessage splits messages exceeding Telegram's 4096 char limit,
// preferring to split at newline boundaries.
func (t *Telegram) sendLongMessage(ctx context.Context, chatID int64, text string) {
	for text != "" {
		chunk := text
		if len(chunk) > telegramMaxMessage {
			chunk = text[:telegramMaxMessage]
			// Try to split at last newline
			if idx := strings.LastIndex(chunk, "\n"); idx > 0 {
				chunk = text[:idx]
			}
		}
		t.sendMessage(ctx, chatID, chunk)
		text = text[len(chunk):]
	}
}

// --- Telegram Bot API types ---

type tgUpdate struct {
	UpdateID int        `json:"update_id"`
	Message  *tgMessage `json:"message"`
}

type tgMessage struct {
	MessageID int    `json:"message_id"`
	Chat      tgChat `json:"chat"`
	Text      string `json:"text"`
}

type tgChat struct {
	ID int64 `json:"id"`
}

type tgAPIResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
}

type tgUser struct {
	Username string `json:"username"`
}

// --- Telegram Bot API calls ---

func (t *Telegram) apiURL(method string) string {
	return t.BaseURL + t.Token + "/" + method
}

func (t *Telegram) getMe(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", t.apiURL("getMe"), http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var apiResp tgAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}
	if !apiResp.OK {
		return "", fmt.Errorf("API error: %s", apiResp.Description)
	}

	var user tgUser
	if err := json.Unmarshal(apiResp.Result, &user); err != nil {
		return "", err
	}
	return user.Username, nil
}

func (t *Telegram) getUpdates(ctx context.Context) ([]tgUpdate, error) {
	body, _ := json.Marshal(map[string]any{
		"offset":  t.offset,
		"timeout": telegramPollTimeout,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.apiURL("getUpdates"), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var apiResp tgAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !apiResp.OK {
		return nil, fmt.Errorf("API error: %s", apiResp.Description)
	}

	var updates []tgUpdate
	if err := json.Unmarshal(apiResp.Result, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

func (t *Telegram) sendMessage(ctx context.Context, chatID int64, text string) {
	body, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"text":    text,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.apiURL("sendMessage"), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		slog.Error("telegram sendMessage failed", "err", err)
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

func (t *Telegram) sendChatAction(ctx context.Context, chatID int64, action string) {
	body, _ := json.Marshal(map[string]any{
		"chat_id": chatID,
		"action":  action,
	})
	req, err := http.NewRequestWithContext(ctx, "POST", t.apiURL("sendChatAction"), bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

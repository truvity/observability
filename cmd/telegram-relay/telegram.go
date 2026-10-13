package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Sender delivers one message. An interface so the handler's tests need no
// Telegram.
type Sender interface {
	Send(ctx context.Context, token string, chatID int64, html string) error
}

// TelegramClient calls the Bot API's sendMessage.
type TelegramClient struct {
	BaseURL string
	HTTP    *http.Client
}

// TelegramError is a refusal from the Bot API itself (ok: false), as
// opposed to a transport failure.
type TelegramError struct {
	Status      int
	Description string
}

func (e *TelegramError) Error() string {
	return fmt.Sprintf("telegram answered %d: %s", e.Status, e.Description)
}

type sendMessage struct {
	ChatID             int64              `json:"chat_id"`
	Text               string             `json:"text"`
	ParseMode          string             `json:"parse_mode"`
	LinkPreviewOptions linkPreviewOptions `json:"link_preview_options"`
}

type linkPreviewOptions struct {
	IsDisabled bool `json:"is_disabled"`
}

type apiResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Send posts html to chatID. The token is part of the Bot API's URL, so
// no error returned here may carry that URL: net/http's *url.Error prints
// it, and an error is logged.
func (c *TelegramClient) Send(ctx context.Context, token string, chatID int64, html string) error {
	body, err := json.Marshal(sendMessage{
		ChatID:             chatID,
		Text:               html,
		ParseMode:          "HTML",
		LinkPreviewOptions: linkPreviewOptions{IsDisabled: true},
	})
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/bot" + token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return errors.New("building the Telegram request failed")
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			// Op and the cause, never ue.URL.
			return fmt.Errorf("telegram request: %s: %w", ue.Op, ue.Err)
		}
		return errors.New("telegram request failed")
	}
	defer func() { _ = resp.Body.Close() }()

	var r apiResponse
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if jerr := json.Unmarshal(raw, &r); jerr != nil || !r.OK || resp.StatusCode != http.StatusOK {
		desc := r.Description
		if desc == "" {
			desc = http.StatusText(resp.StatusCode)
		}
		return &TelegramError{Status: resp.StatusCode, Description: desc}
	}
	return nil
}

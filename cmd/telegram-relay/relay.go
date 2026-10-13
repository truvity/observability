package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// maxBody bounds one webhook document. GoAlert's are well under a kilobyte
// plus the alert's details.
const maxBody = 256 << 10

// Relay is the webhook handler: authenticate, resolve the recipient,
// format, send.
type Relay struct {
	Secrets  Secrets
	Telegram Sender
	Timeout  time.Duration
	Metrics  *Metrics
	Logger   *slog.Logger
}

func (rl *Relay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	recipient := r.PathValue("recipient")

	sec, err := rl.Secrets.Load()
	if err != nil {
		rl.Logger.Error("loading the mounted secrets", "error", err.Error())
		rl.done(w, "unknown", resultConfigError, http.StatusServiceUnavailable)
		return
	}

	// Authenticate before anything else is looked at, so an unauthenticated
	// caller learns nothing about which recipients exist.
	if !authorized(r, sec.SharedSecret) {
		rl.done(w, "unknown", resultUnauthorized, http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		rl.done(w, "unknown", resultBadRequest, http.StatusRequestEntityTooLarge)
		return
	}
	p, err := DecodePayload(body)
	if err != nil {
		rl.Logger.Warn("refused a webhook document", "recipient", recipient, "error", err.Error())
		rl.done(w, typeLabel(p.Type), resultBadRequest, http.StatusBadRequest)
		return
	}

	chatID, ok := sec.Recipients[recipient]
	if !ok {
		rl.Logger.Warn("no chat id for recipient", "recipient", recipient, "type", p.Type)
		rl.done(w, p.Type, resultUnknownRecipient, http.StatusNotFound)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), rl.Timeout)
	defer cancel()
	start := time.Now()
	err = rl.Telegram.Send(ctx, sec.BotToken, chatID, Format(p))
	rl.Metrics.SendDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		var te *TelegramError
		attrs := []any{"recipient", recipient, "type", p.Type, "alert_id", p.AlertID, "error", err.Error()}
		if errors.As(err, &te) && te.Status == http.StatusForbidden {
			// The person blocked the bot, or never pressed Start: Telegram
			// lets a bot write only to someone who wrote to it first.
			attrs = append(attrs, "hint", "the recipient must open a chat with the bot and press Start")
		}
		rl.Logger.Error("telegram delivery failed", attrs...)
		rl.done(w, p.Type, resultTelegramError, http.StatusBadGateway)
		return
	}
	rl.Logger.Info("delivered", "recipient", recipient, "type", p.Type, "alert_id", p.AlertID)
	rl.done(w, p.Type, resultDelivered, http.StatusOK)
}

func (rl *Relay) done(w http.ResponseWriter, typ, result string, status int) {
	rl.Metrics.Notifications.WithLabelValues(typ, result).Inc()
	if result == resultDelivered {
		rl.Metrics.LastDelivered.SetToCurrentTime()
	}
	http.Error(w, result, status)
}

// authorized accepts the shared secret in the three places a GoAlert
// webhook URL can carry it: `?token=`, the password of Basic auth written
// into the URL (`https://relay:<secret>@host/...`, which Go's HTTP client
// turns into an Authorization header), or a bearer token for any other
// caller. GoAlert itself can set no header.
func authorized(r *http.Request, secret string) bool {
	var candidates []string
	if t := r.URL.Query().Get("token"); t != "" {
		candidates = append(candidates, t)
	}
	if _, pass, ok := r.BasicAuth(); ok {
		candidates = append(candidates, pass)
	}
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		candidates = append(candidates, strings.TrimPrefix(h, "Bearer "))
	}
	for _, c := range candidates {
		if subtle.ConstantTimeCompare([]byte(c), []byte(secret)) == 1 {
			return true
		}
	}
	return false
}

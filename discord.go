package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	discordContentLimit          = 2000
	discordEmbedTitleLimit       = 256
	discordEmbedDescriptionLimit = 4096
	discordEmbedFieldNameLimit   = 256
	discordEmbedFieldValueLimit  = 1024
	discordEmbedFooterTextLimit  = 2048
	discordEmbedFieldLimit       = 25

	discordMaxAttempts = 3
	discordMaxBackoff  = 5 * time.Second

	// Discord's REST API requires a User-Agent in this shape, and its WAF
	// answers a missing or generic one with a Cloudflare 1010 -- an HTML-ish
	// "error code: 1010" body rather than a Discord error, so it does not even
	// look like Discord rejecting the call. Go's default Go-http-client/2.0 is
	// exactly the kind of value that gets caught.
	discordUserAgent = "DiscordBot (https://github.com/tamura09/giteki-notify, 1.0)"

	discordUsername = "giteki-notify"
)

type webhookPayload struct {
	Content         string          `json:"content,omitempty"`
	Username        string          `json:"username,omitempty"`
	Embeds          []embed         `json:"embeds,omitempty"`
	AllowedMentions allowedMentions `json:"allowed_mentions"`
}

type allowedMentions struct {
	Parse []string `json:"parse"`
	Roles []string `json:"roles,omitempty"`
}

type embed struct {
	Title       string       `json:"title,omitempty"`
	URL         string       `json:"url,omitempty"`
	Description string       `json:"description,omitempty"`
	Color       int          `json:"color,omitempty"`
	Fields      []embedField `json:"fields,omitempty"`
	Footer      *embedFooter `json:"footer,omitempty"`
	Timestamp   string       `json:"timestamp,omitempty"`
}

type embedField struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

type embedFooter struct {
	Text string `json:"text"`
}

type rateLimitResponse struct {
	RetryAfter float64 `json:"retry_after"`
}

type discordError struct {
	StatusCode int
	Body       string
}

func (e *discordError) Error() string {
	return fmt.Sprintf("Discord returned %d: %s", e.StatusCode, e.Body)
}

func (a *app) postWebhook(ctx context.Context, webhookURL string, payload webhookPayload) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= discordMaxAttempts; attempt++ {
		retryAfter, err := a.postWebhookOnce(ctx, webhookURL, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if retryAfter <= 0 || attempt == discordMaxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(retryAfter):
		}
	}

	return lastErr
}

// postWebhookOnce returns a positive retryAfter when the failure is worth
// retrying: a 429 from Discord's own rate limiter, or a 5xx.
func (a *app) postWebhookOnce(ctx context.Context, webhookURL string, body []byte) (time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", discordUserAgent)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return time.Second, err
	}
	defer resp.Body.Close()

	responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return 0, fmt.Errorf("read Discord response: %w", readErr)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return 0, nil
	}

	failure := &discordError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(responseBody))}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return retryAfterDelay(responseBody), failure
	case resp.StatusCode >= 500:
		return time.Second, failure
	}

	return 0, failure
}

func retryAfterDelay(body []byte) time.Duration {
	var limited rateLimitResponse
	if err := json.Unmarshal(body, &limited); err != nil || limited.RetryAfter <= 0 {
		return time.Second
	}
	delay := time.Duration(limited.RetryAfter * float64(time.Second))
	if delay > discordMaxBackoff {
		return discordMaxBackoff
	}
	return delay
}

// parseWebhookURL rejects a parameter holding something other than a Discord
// webhook. Without it a parameter left at its Terraform placeholder would be
// POSTed to on every run, and the failure would look like Discord being down.
func parseWebhookURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("webhook URL is empty")
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", fmt.Errorf("parse webhook URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" {
		return "", fmt.Errorf("webhook URL must be an absolute https URL")
	}

	return trimmed, nil
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit <= 1 {
		return string(runes[:limit])
	}
	return string(runes[:limit-1]) + "…"
}

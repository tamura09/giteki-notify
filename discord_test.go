package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestPostWebhookRetriesARateLimit(t *testing.T) {
	var attempts int32

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			// Discord answers in seconds, as a float.
			_, _ = w.Write([]byte(`{"retry_after":0.01}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestApp()
	client.httpClient = server.Client()

	if err := client.postWebhook(context.Background(), server.URL, webhookPayload{Username: discordUsername}); err != nil {
		t.Fatalf("postWebhook: %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Errorf("made %d attempts, want 2", got)
	}
}

func TestPostWebhookDoesNotRetryARejection(t *testing.T) {
	var attempts int32

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"message":"Invalid Form Body"}`))
	}))
	defer server.Close()

	client := newTestApp()
	client.httpClient = server.Client()

	err := client.postWebhook(context.Background(), server.URL, webhookPayload{Username: discordUsername})
	if err == nil {
		t.Fatal("postWebhook succeeded on a 400")
	}
	// A payload Discord refuses is refused the same way every time.
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Errorf("made %d attempts, want 1", got)
	}
	if !strings.Contains(err.Error(), "Invalid Form Body") {
		t.Errorf("error = %q, want Discord's own message", err)
	}
}

func TestPostWebhookRetriesAServerError(t *testing.T) {
	var attempts int32

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&attempts, 1) < int32(discordMaxAttempts) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestApp()
	client.httpClient = server.Client()

	if err := client.postWebhook(context.Background(), server.URL, webhookPayload{}); err != nil {
		t.Fatalf("postWebhook: %v", err)
	}
}

func TestPostWebhookSendsADiscordShapedUserAgent(t *testing.T) {
	var seen string

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("User-Agent")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := newTestApp()
	client.httpClient = server.Client()

	if err := client.postWebhook(context.Background(), server.URL, webhookPayload{}); err != nil {
		t.Fatalf("postWebhook: %v", err)
	}
	// Discord's WAF answers a generic User-Agent with a Cloudflare 1010, which
	// does not even look like Discord rejecting the call.
	if !strings.HasPrefix(seen, "DiscordBot (") {
		t.Errorf("User-Agent = %q, want the DiscordBot shape", seen)
	}
}

func TestParseWebhookURL(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		given   string
		wantErr string
	}{
		{name: "valid", given: "https://discord.com/api/webhooks/1/abc"},
		{name: "padded", given: "  https://discord.com/api/webhooks/1/abc\n"},
		{name: "empty", given: "   ", wantErr: "empty"},
		{name: "terraform placeholder", given: "MANAGED_OUTSIDE_TERRAFORM", wantErr: "absolute https URL"},
		{name: "http", given: "http://discord.com/api/webhooks/1/abc", wantErr: "absolute https URL"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := parseWebhookURL(testCase.given)
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseWebhookURL: %v", err)
			}
			if got != strings.TrimSpace(testCase.given) {
				t.Errorf("URL = %q, want it trimmed but otherwise unchanged", got)
			}
		})
	}
}

func TestTruncateCountsRunesNotBytes(t *testing.T) {
	// Byte-counting would cut a multi-byte character in half and produce invalid
	// UTF-8, which Discord rejects the whole message for.
	if got := truncate("第2条第19号に規定する特定無線設備", 5); got != "第2条第…" {
		t.Errorf("truncate = %q, want 第2条第…", got)
	}
	if got := truncate("short", 10); got != "short" {
		t.Errorf("truncate = %q, want the input unchanged", got)
	}
	if got := truncate("abc", 1); got != "a" {
		t.Errorf("truncate = %q, want a single rune with no ellipsis", got)
	}
}

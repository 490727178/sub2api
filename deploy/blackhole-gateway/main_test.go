package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGatewayOnlyForwardsV1(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()
	gateway := newGateway(upstream.URL, "fake reply")

	for _, path := range []string{"/", "/login", "/api/v1/admin/users", "/v1beta/models"} {
		rec := httptest.NewRecorder()
		gateway.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("path %s: got %d, want 404", path, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	gateway.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("/v1/messages: got %d, want 204", rec.Code)
	}
}

func TestGatewayConvertsOnlyInvalidUserKeyErrors(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		status     int
		body       string
		wantStatus int
		wantText   string
	}{
		{"anthropic invalid key", "/v1/messages", 401, `{"code":"INVALID_API_KEY","message":"Invalid API key"}`, 200, `"type":"message"`},
		{"openai missing key", "/v1/chat/completions", 401, `{"code":"API_KEY_REQUIRED","message":"required"}`, 200, `"object":"chat.completion"`},
		{"responses invalid key", "/v1/responses", 401, `{"code":"INVALID_API_KEY","message":"invalid"}`, 200, `"object":"response"`},
		{"upstream unauthorized untouched", "/v1/messages", 401, `{"error":{"message":"Unauthorized","type":"upstream_error"}}`, 401, "Unauthorized"},
		{"internal error untouched", "/v1/messages", 500, `{"code":"INTERNAL_ERROR"}`, 500, "INTERNAL_ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer upstream.Close()

			rec := httptest.NewRecorder()
			newGateway(upstream.URL, "fake reply").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(`{"secret":"do not echo"}`)))
			if rec.Code != tt.wantStatus {
				t.Fatalf("got status %d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantText) {
				t.Fatalf("body %q does not contain %q", rec.Body.String(), tt.wantText)
			}
			if strings.Contains(rec.Body.String(), "do not echo") {
				t.Fatal("response echoed request body")
			}
		})
	}
}

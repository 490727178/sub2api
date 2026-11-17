package middleware

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPayloadLogger_RecordsRequestAndResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tmpDir := t.TempDir()
	t.Setenv("DATA_DIR", tmpDir)

	r := gin.New()
	r.Use(PayloadLogger())
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		ctx := context.WithValue(c.Request.Context(), ctxkey.Model, "gpt-4o")
		ctx = context.WithValue(ctx, ctxkey.RequestID, "test-req-12345")
		c.Request = c.Request.WithContext(ctx)

		var req map[string]any
		err := c.BindJSON(&req)
		assert.NoError(t, err)

		c.JSON(http.StatusOK, gin.H{
			"id":      "chatcmpl-123",
			"message": "hello world",
		})
	})

	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"ping"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewBufferString(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)

	// 等待异步落盘完成
	payloadsDir := filepath.Join(tmpDir, "logs", "payloads")
	var foundFile string
	require.Eventually(t, func() bool {
		entries, err := os.ReadDir(payloadsDir)
		if err != nil || len(entries) == 0 {
			return false
		}
		for _, entry := range entries {
			if entry.IsDir() {
				files, err := os.ReadDir(filepath.Join(payloadsDir, entry.Name()))
				if err == nil && len(files) > 0 {
					foundFile = filepath.Join(payloadsDir, entry.Name(), files[0].Name())
					return true
				}
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond)

	content, err := os.ReadFile(foundFile)
	require.NoError(t, err)
	text := string(content)

	assert.Contains(t, text, "=== METADATA ===")
	assert.Contains(t, text, "Model: gpt-4o")
	assert.Contains(t, text, "Request-ID: test-req-12345")
	assert.Contains(t, text, "Status: 200")
	assert.Contains(t, text, "=== REQUEST BODY ===")
	assert.Contains(t, text, `"ping"`)
	assert.Contains(t, text, "=== RESPONSE BODY ===")
	assert.Contains(t, text, `"hello world"`)
}

func TestSanitizeFilenamePart(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "unknown"},
		{"gpt-4o", "gpt-4o"},
		{"anthropic/claude-3-5-sonnet", "anthropic_claude-3-5-sonnet"},
		{"models/gemini-1.5-pro:generateContent", "models_gemini-1.5-pro_generateContent"},
		{"invalid:*?\"<>|chars", "invalid_______chars"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizeFilenamePart(tt.input)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestPayloadResponseWriter_Truncation(t *testing.T) {
	base := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(base)

	writer := &payloadResponseWriter{
		ResponseWriter: c.Writer,
		maxCapture:     10,
	}

	_, err := writer.Write([]byte("0123456789extra_data"))
	require.NoError(t, err)

	captured := string(writer.getCapturedBody())
	assert.Contains(t, captured, "0123456789")
	assert.Contains(t, captured, "[...TRUNCATED...]")
}

func TestIsGatewayRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		method   string
		path     string
		auth     string
		expected bool
	}{
		{"GET", "/health", "", false},
		{"GET", "/ping", "", false},
		{"GET", "/setup/status", "", false},
		{"GET", "/api/v1/admin/settings", "", false},
		{"GET", "/assets/index.js", "", false},
		{"OPTIONS", "/v1/chat/completions", "", false},
		{"POST", "/v1/chat/completions", "", true},
		{"POST", "/v1/messages", "", true},
		{"POST", "/v1beta/models/gemini-1.5-pro:generateContent", "", true},
		{"POST", "/backend-api/codex/responses", "", true},
		{"POST", "/antigravity/v1/messages", "", true},
		{"POST", "/responses", "", true},
		{"POST", "/custom-path", "Bearer sk-123456", true},
		{"POST", "/custom-path", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.method+"_"+tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			if tt.auth != "" {
				req.Header.Set("Authorization", tt.auth)
			}
			c.Request = req

			got := isGatewayRequest(c)
			assert.Equal(t, tt.expected, got)
		})
	}
}

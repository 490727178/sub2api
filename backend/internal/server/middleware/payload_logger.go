package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ip"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const (
	maxPayloadCaptureBytes = 10 * 1024 * 1024 // 10MB 单边捕获上限
)

type payloadResponseWriter struct {
	gin.ResponseWriter
	body       bytes.Buffer
	mu         sync.Mutex
	maxCapture int
}

func (w *payloadResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *payloadResponseWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	if w.body.Len() < w.maxCapture {
		remaining := w.maxCapture - w.body.Len()
		if len(data) > remaining {
			w.body.Write(data[:remaining])
			w.body.WriteString("\n[...TRUNCATED...]")
		} else {
			w.body.Write(data)
		}
	}
	w.mu.Unlock()
	return w.ResponseWriter.Write(data)
}

func (w *payloadResponseWriter) WriteString(data string) (int, error) {
	w.mu.Lock()
	if w.body.Len() < w.maxCapture {
		remaining := w.maxCapture - w.body.Len()
		if len(data) > remaining {
			w.body.WriteString(data[:remaining])
			w.body.WriteString("\n[...TRUNCATED...]")
		} else {
			w.body.WriteString(data)
		}
	}
	w.mu.Unlock()
	return w.ResponseWriter.WriteString(data)
}

func (w *payloadResponseWriter) Flush() {
	w.ResponseWriter.Flush()
}

func (w *payloadResponseWriter) getCapturedBody() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]byte(nil), w.body.Bytes()...)
}

type errReader struct {
	err error
}

func (e *errReader) Read(_ []byte) (int, error) {
	return 0, e.err
}

func resolvePayloadsBaseDir() string {
	if dataDir := strings.TrimSpace(os.Getenv("DATA_DIR")); dataDir != "" {
		return filepath.Join(dataDir, "logs", "payloads")
	}
	if fi, err := os.Stat("/app/data"); err == nil && fi.IsDir() {
		return "/app/data/logs/payloads"
	}
	if fi, err := os.Stat("deploy/data/logs"); err == nil && fi.IsDir() {
		return "deploy/data/logs/payloads"
	}
	if fi, err := os.Stat("data/logs"); err == nil && fi.IsDir() {
		return "data/logs/payloads"
	}
	return "/app/data/logs/payloads"
}

func sanitizeFilenamePart(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "unknown"
	}
	var sb strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			sb.WriteRune(r)
		} else {
			sb.WriteRune('_')
		}
	}
	res := sb.String()
	if len(res) > 64 {
		res = res[:64]
	}
	return res
}

func isGatewayRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	if c.Request.Method == http.MethodOptions {
		return false
	}

	path := c.Request.URL.Path
	// 排除健康检查、状态探针
	if path == "/health" || path == "/ping" || path == "/setup/status" {
		return false
	}
	// 排除管理后台与用户控制台 API
	if strings.HasPrefix(path, "/api/") {
		return false
	}
	// 排除前端静态资源
	if strings.HasPrefix(path, "/assets/") || path == "/favicon.ico" || path == "/robots.txt" || path == "/" {
		return false
	}

	// 匹配网关路径特征
	if strings.HasPrefix(path, "/v1/") ||
		strings.HasPrefix(path, "/v1beta/") ||
		strings.HasPrefix(path, "/backend-api/codex") ||
		strings.HasPrefix(path, "/antigravity/") ||
		strings.HasPrefix(path, "/responses") ||
		strings.HasPrefix(path, "/models") ||
		strings.HasPrefix(path, "/messages") ||
		strings.HasPrefix(path, "/alpha/") ||
		strings.HasPrefix(path, "/contents/") ||
		strings.HasPrefix(path, "/chat/completions") ||
		strings.HasPrefix(path, "/videos") ||
		strings.HasPrefix(path, "/tts") ||
		strings.HasPrefix(path, "/stt") ||
		strings.HasPrefix(path, "/custom-voices") ||
		strings.HasPrefix(path, "/realtime") {
		return true
	}

	// 如果包含 API Key 格式的 Authorization 头，也视为网关请求
	auth := c.GetHeader("Authorization")
	if strings.HasPrefix(auth, "Bearer sk-") || strings.HasPrefix(auth, "Bearer sub2api-") {
		return true
	}

	return false
}

// PayloadLogger 记录网关请求的入参和出参到本地文件中。
func PayloadLogger() gin.HandlerFunc {
	baseDir := resolvePayloadsBaseDir()

	return func(c *gin.Context) {
		if !isGatewayRequest(c) {
			c.Next()
			return
		}

		startTime := time.Now()

		// 1. 读取并克隆请求体
		var reqBody []byte
		if c.Request.Body != nil {
			bodyBytes, err := io.ReadAll(c.Request.Body)
			if err != nil {
				reqBody = bodyBytes
				c.Request.Body = io.NopCloser(io.MultiReader(bytes.NewBuffer(bodyBytes), &errReader{err: err}))
			} else {
				reqBody = bodyBytes
				c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
			}
		}

		// 2. 包装 ResponseWriter 捕获回包（针对非 WebSocket）
		var writer *payloadResponseWriter
		isWS := strings.EqualFold(c.GetHeader("Upgrade"), "websocket") || c.IsWebsocket()
		if !isWS {
			writer = &payloadResponseWriter{
				ResponseWriter: c.Writer,
				maxCapture:     maxPayloadCaptureBytes,
			}
			c.Writer = writer
		}

		// 3. 执行后续处理
		c.Next()

		// 4. 提取元数据并异步写入日志文件
		endTime := time.Now()
		latency := endTime.Sub(startTime)
		statusCode := c.Writer.Status()
		clientIP := ip.GetClientIP(c)
		method := c.Request.Method
		path := c.Request.URL.Path

		// 提取 Request ID
		reqID, _ := c.Request.Context().Value(ctxkey.RequestID).(string)
		if reqID == "" {
			reqID = c.GetHeader("X-Request-ID")
		}
		if reqID == "" {
			reqID = c.Writer.Header().Get("X-Request-ID")
		}
		if reqID == "" {
			reqID = uuid.NewString()
		}

		clientRequestID, _ := c.Request.Context().Value(ctxkey.ClientRequestID).(string)

		// 提取 Model
		model, _ := c.Request.Context().Value(ctxkey.Model).(string)
		if model == "" {
			model, _ = c.Request.Context().Value(ctxkey.RequestedPublicModel).(string)
		}
		if model == "" {
			model, _ = c.Request.Context().Value(ctxkey.ResolvedUpstreamModel).(string)
		}
		if model == "" && len(reqBody) > 0 {
			model = gjson.GetBytes(reqBody, "model").String()
		}
		if model == "" {
			model = c.Param("model")
		}
		if model == "" {
			ma := c.Param("modelAction")
			if ma != "" {
				parts := strings.Split(strings.TrimPrefix(ma, "/"), ":")
				if len(parts) > 0 && parts[0] != "" {
					model = parts[0]
				}
			}
		}
		if model == "" {
			model = "unknown"
		}

		platform, _ := c.Request.Context().Value(ctxkey.Platform).(string)
		accountID, _ := c.Request.Context().Value(ctxkey.AccountID).(int64)

		safeModel := sanitizeFilenamePart(model)
		safeReqID := sanitizeFilenamePart(reqID)
		dateDir := startTime.Format("2006-01-02")
		timeStr := startTime.Format("20060102-150405")
		fileName := fmt.Sprintf("payload-%s-%s-%s.log", timeStr, safeModel, safeReqID)
		filePath := filepath.Join(baseDir, dateDir, fileName)

		var respBody []byte
		if writer != nil {
			respBody = writer.getCapturedBody()
		}

		// 异步写入文件
		go func(contentPath string, bodyReq, bodyResp []byte) {
			var b strings.Builder
			b.WriteString("=== METADATA ===\n")
			b.WriteString(fmt.Sprintf("Timestamp: %s\n", startTime.Format(time.RFC3339Nano)))
			b.WriteString(fmt.Sprintf("Method: %s\n", method))
			b.WriteString(fmt.Sprintf("Path: %s\n", path))
			b.WriteString(fmt.Sprintf("Status: %d\n", statusCode))
			b.WriteString(fmt.Sprintf("Latency: %v\n", latency))
			b.WriteString(fmt.Sprintf("Client-IP: %s\n", clientIP))
			b.WriteString(fmt.Sprintf("Request-ID: %s\n", reqID))
			if clientRequestID != "" {
				b.WriteString(fmt.Sprintf("Client-Request-ID: %s\n", clientRequestID))
			}
			b.WriteString(fmt.Sprintf("Model: %s\n", model))
			if platform != "" {
				b.WriteString(fmt.Sprintf("Platform: %s\n", platform))
			}
			if accountID > 0 {
				b.WriteString(fmt.Sprintf("Account-ID: %d\n", accountID))
			}

			b.WriteString("\n=== REQUEST BODY ===\n")
			if len(bodyReq) > 0 {
				b.Write(bodyReq)
				if !bytes.HasSuffix(bodyReq, []byte("\n")) {
					b.WriteString("\n")
				}
			} else {
				b.WriteString("<EMPTY>\n")
			}

			b.WriteString("\n=== RESPONSE BODY ===\n")
			if len(bodyResp) > 0 {
				b.Write(bodyResp)
				if !bytes.HasSuffix(bodyResp, []byte("\n")) {
					b.WriteString("\n")
				}
			} else {
				b.WriteString("<EMPTY>\n")
			}

			dir := filepath.Dir(contentPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return
			}
			_ = os.WriteFile(contentPath, []byte(b.String()), 0644)
		}(filePath, reqBody, respBody)
	}
}

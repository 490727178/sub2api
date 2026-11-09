package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
)

var invalidCodes = map[string]bool{
	"API_KEY_REQUIRED": true,
	"INVALID_API_KEY":  true,
	"API_KEY_DISABLED": true,
}

func main() {
	upstream := os.Getenv("UPSTREAM_URL")
	if upstream == "" {
		upstream = "http://sub2api:8080"
	}
	message := os.Getenv("BLACKHOLE_MESSAGE")
	if message == "" {
		message = "Request accepted"
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8788"
	}
	_ = http.ListenAndServe(addr, newGateway(upstream, message))
}

func newGateway(rawUpstream, message string) http.Handler {
	target, err := url.Parse(rawUpstream)
	if err != nil {
		panic(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) {
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}
	proxy.ModifyResponse = func(resp *http.Response) error {
		if resp.StatusCode != http.StatusUnauthorized || !isInvalidUserKeyResponse(resp) {
			return nil
		}
		return replaceWithBlackhole(resp, message, resp.Request.URL.Path)
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, "ok\n")
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			http.NotFound(w, r)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}

func isInvalidUserKeyResponse(resp *http.Response) bool {
	if resp == nil || resp.Body == nil {
		return false
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var envelope struct {
		Code string `json:"code"`
	}
	return json.Unmarshal(body, &envelope) == nil && invalidCodes[envelope.Code]
}

func replaceWithBlackhole(resp *http.Response, message, path string) error {
	protocol := "openai"
	if strings.Contains(path, "/messages") {
		protocol = "anthropic"
	} else if strings.Contains(path, "/responses") {
		protocol = "responses"
	}
	body := blackholeBody(protocol, message)
	resp.StatusCode = http.StatusOK
	resp.Status = "200 OK"
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set("Cache-Control", "no-store")
	resp.Header.Del("Content-Length")
	resp.Body = io.NopCloser(bytes.NewReader(body))
	resp.ContentLength = int64(len(body))
	resp.Header.Set("Content-Length", strconv.Itoa(len(body)))
	return nil
}

func blackholeBody(protocol, message string) []byte {
	var body any
	if protocol == "anthropic" {
		body = map[string]any{
			"id": "msg_blackhole", "type": "message", "role": "assistant",
			"content": []map[string]string{{"type": "text", "text": message}},
			"model":   "blackhole", "stop_reason": "end_turn",
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		}
	} else if protocol == "responses" {
		body = map[string]any{
			"id": "resp_blackhole", "object": "response", "status": "completed", "model": "blackhole",
			"output": []map[string]any{{"type": "message", "role": "assistant", "content": []map[string]string{{"type": "output_text", "text": message}}}},
			"usage":  map[string]int{"input_tokens": 0, "output_tokens": 0},
		}
	} else {
		body = map[string]any{
			"id": "chatcmpl-blackhole", "object": "chat.completion", "model": "blackhole",
			"choices": []map[string]any{{"index": 0, "message": map[string]string{"role": "assistant", "content": message}, "finish_reason": "stop"}},
			"usage":   map[string]int{"prompt_tokens": 0, "completion_tokens": 0, "total_tokens": 0},
		}
	}
	encoded, _ := json.Marshal(body)
	return encoded
}

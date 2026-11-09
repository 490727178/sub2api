//go:build unit

package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestClampGeminiChatCompletionsMaxTokens(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantField string
		wantValue int64
	}{
		{
			name:      "responses max_completion_tokens over cap",
			body:      `{"model":"gemini-3.8-flash","max_completion_tokens":128000}`,
			wantField: "max_completion_tokens",
			wantValue: geminiChatCompletionsMaxOutputTokens,
		},
		{
			name:      "anthropic max_tokens over cap",
			body:      `{"model":"gemini-3.8-flash","max_tokens":128000}`,
			wantField: "max_tokens",
			wantValue: geminiChatCompletionsMaxOutputTokens,
		},
		{
			name:      "exactly at cap is untouched",
			body:      `{"model":"gemini-3.8-flash","max_tokens":65536}`,
			wantField: "max_tokens",
			wantValue: 65536,
		},
		{
			name:      "one over cap is clamped",
			body:      `{"model":"gemini-3.8-flash","max_tokens":65537}`,
			wantField: "max_tokens",
			wantValue: geminiChatCompletionsMaxOutputTokens,
		},
		{
			name:      "small value untouched",
			body:      `{"model":"gemini-3.8-flash","max_tokens":4096}`,
			wantField: "max_tokens",
			wantValue: 4096,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := clampGeminiChatCompletionsMaxTokens([]byte(tc.body))
			require.Equal(t, tc.wantValue, gjson.GetBytes(got, tc.wantField).Int())
		})
	}
}

func TestClampGeminiChatCompletionsMaxTokens_BothFields(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","max_tokens":200000,"max_completion_tokens":300000}`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.Equal(t, int64(geminiChatCompletionsMaxOutputTokens), gjson.GetBytes(got, "max_tokens").Int())
	require.Equal(t, int64(geminiChatCompletionsMaxOutputTokens), gjson.GetBytes(got, "max_completion_tokens").Int())
}

func TestClampGeminiChatCompletionsMaxTokens_NonGeminiUntouched(t *testing.T) {
	body := []byte(`{"model":"gpt-6.1-sol","max_tokens":200000,"max_completion_tokens":300000}`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.Equal(t, `{"model":"gpt-6.1-sol","max_tokens":200000,"max_completion_tokens":300000}`, string(got))
}

func TestClampGeminiChatCompletionsMaxTokens_MissingFields(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","messages":[]}`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.Equal(t, string(body), string(got))
}

func TestClampGeminiChatCompletionsMaxTokens_InvalidJSON(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash",`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.Equal(t, string(body), string(got))
}

func TestClampGeminiChatCompletionsMaxTokens_NonNumericUntouched(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","max_tokens":"128000"}`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.Equal(t, "128000", gjson.GetBytes(got, "max_tokens").String())
}

func TestClampGeminiChatCompletionsMaxTokens_ProviderPrefixedModel(t *testing.T) {
	body := []byte(`{"model":"google/gemini-3.8-flash","max_tokens":128000}`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.Equal(t, int64(128000), gjson.GetBytes(got, "max_tokens").Int())
}

func TestClampGeminiChatCompletionsMaxTokens_PreservesOtherFields(t *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","max_tokens":128000,"stream":true,"temperature":0.7,"messages":[{"role":"user","content":"hi"}]}`)
	got := clampGeminiChatCompletionsMaxTokens(body)
	require.True(t, gjson.ValidBytes(got))
	var parsed map[string]any
	require.NoError(t, json.Unmarshal(got, &parsed))
	require.Equal(t, true, parsed["stream"])
	require.Equal(t, 0.7, parsed["temperature"])
	require.Len(t, parsed["messages"], 1)
}

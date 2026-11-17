//go:build unit

package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type toolCallIDAliasCache struct {
	stubGatewayCache
	values map[string]string
}

func (c *toolCallIDAliasCache) SetReasoningContent(_ context.Context, itemID string, content string, _ time.Duration) error {
	c.values[itemID] = content
	return nil
}

func (c *toolCallIDAliasCache) GetReasoningContent(_ context.Context, itemID string) (string, error) {
	if v, ok := c.values[itemID]; ok {
		return v, nil
	}
	return "", ErrReasoningContentNotFound
}

func TestToolCallIDAlias_RoundTrip(t *testing.T) {
	cache := &toolCallIDAliasCache{values: map[string]string{}}
	svc := &OpenAIGatewayService{cache: cache}
	long := "call_4156152384608832275__thought__" + strings.Repeat("A", 11000)
	idx := 0

	chunk := &apicompat.ChatCompletionsChunk{Choices: []apicompat.ChatChunkChoice{{
		Delta: apicompat.ChatDelta{ToolCalls: []apicompat.ChatToolCall{
			{Index: &idx, ID: long},
			{Index: &idx, ID: "call_short"},
			{Index: &idx},
		}},
	}}}
	svc.shortenChatChunkToolCallIDs(chunk)
	alias := chunk.Choices[0].Delta.ToolCalls[0].ID
	require.LessOrEqual(t, len(alias), toolCallIDMaxLen)
	require.True(t, strings.HasPrefix(alias, toolCallIDAliasPrefix))
	require.Equal(t, "call_short", chunk.Choices[0].Delta.ToolCalls[1].ID)
	require.Empty(t, chunk.Choices[0].Delta.ToolCalls[2].ID)

	resp := &apicompat.ChatCompletionsResponse{Choices: []apicompat.ChatChoice{{
		Message: apicompat.ChatMessage{ToolCalls: []apicompat.ChatToolCall{{ID: long}}},
	}}}
	svc.shortenChatResponseToolCallIDs(resp)
	require.Equal(t, alias, resp.Choices[0].Message.ToolCalls[0].ID, "alias must be deterministic")

	body := func(model string) []byte {
		return []byte(`{"model":"` + model + `","messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"` + alias + `","type":"function","function":{"name":"f","arguments":"{}"}},{"id":"call_short","type":"function","function":{"name":"g","arguments":"{}"}}]},{"role":"tool","tool_call_id":"` + alias + `","content":"ok"},{"role":"tool","tool_call_id":"call_short","content":"ok"}]}`)
	}

	restored := svc.restoreGeminiChatCompletionsToolCallIDs(body("gemini-3.8-flash"))
	require.Equal(t, long, gjson.GetBytes(restored, "messages.1.tool_calls.0.id").String())
	require.Equal(t, long, gjson.GetBytes(restored, "messages.2.tool_call_id").String())
	require.Equal(t, "call_short", gjson.GetBytes(restored, "messages.1.tool_calls.1.id").String())
	require.Equal(t, "call_short", gjson.GetBytes(restored, "messages.3.tool_call_id").String())

	nonGemini := body("deepseek-v4.1-flash")
	require.Equal(t, nonGemini, svc.restoreGeminiChatCompletionsToolCallIDs(nonGemini))

	// 缓存未命中：保留别名，调用与结果仍然配对。
	miss := &OpenAIGatewayService{cache: &toolCallIDAliasCache{values: map[string]string{}}}
	require.Equal(t, body("gemini-3.8-flash"), miss.restoreGeminiChatCompletionsToolCallIDs(body("gemini-3.8-flash")))

	// 无缓存时仍然缩短，不 panic。
	require.Equal(t, alias, (&OpenAIGatewayService{}).shortenToolCallID(long))
}

func TestShortenResponsesInputCallIDs(t *testing.T) {
	cache := &toolCallIDAliasCache{values: map[string]string{}}
	svc := &OpenAIGatewayService{cache: cache}
	long := "call_1__thought__" + strings.Repeat("B", 11000)
	body := []byte(`{"model":"gpt-6.1-sol","input":[{"role":"user","content":"hi"},{"type":"function_call","call_id":"` + long + `","name":"f","arguments":"{}"},{"type":"function_call_output","call_id":"` + long + `","output":"ok"},{"type":"function_call","call_id":"call_short","name":"g","arguments":"{}"}]}`)

	got := svc.shortenResponsesInputCallIDs(body)
	alias := gjson.GetBytes(got, "input.1.call_id").String()
	require.LessOrEqual(t, len(alias), toolCallIDMaxLen)
	require.Equal(t, alias, gjson.GetBytes(got, "input.2.call_id").String())
	require.Equal(t, "call_short", gjson.GetBytes(got, "input.3.call_id").String())
	require.Equal(t, "hi", gjson.GetBytes(got, "input.0.content").String())
	require.Equal(t, long, cache.values[toolCallIDAliasCachePrefix+alias])

	untouched := []byte(`{"model":"gpt-6.1-sol","input":"hello"}`)
	require.Equal(t, untouched, svc.shortenResponsesInputCallIDs(untouched))
}

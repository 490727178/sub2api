package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Gemini（LiteLLM/Vertex 风格）会把 thought signature 嵌进 tool call id
// （call_<n>__thought__<签名>），长度可达上万字符。客户端把它存进会话历史后，
// 一旦同一会话切到 OpenAI Responses 上游，就会因 call_id 超过 64 字符被 400。
//
// 这里在回程把超长 id 换成定长别名并缓存原值；去程发往 Gemini 时再按别名还原，
// 让 Gemini 仍能拿回签名。别名由原 id 确定性派生，跨实例一致。
const (
	toolCallIDMaxLen           = 64
	toolCallIDAliasPrefix      = "call_s2a_"
	toolCallIDAliasCachePrefix = "tool_call_id:"
)

func toolCallIDAlias(original string) string {
	sum := sha256.Sum256([]byte(original))
	return toolCallIDAliasPrefix + hex.EncodeToString(sum[:20])
}

// shortenToolCallID 把超过上限的 tool call id 换成别名并缓存原值（复用 reasoning
// 缓存，7 天 TTL）。缓存写失败只影响后续还原，不影响本次响应。
func (s *OpenAIGatewayService) shortenToolCallID(id string) string {
	if len(id) <= toolCallIDMaxLen {
		return id
	}
	alias := toolCallIDAlias(id)
	s.setReasoningContent(toolCallIDAliasCachePrefix+alias, id)
	return alias
}

func (s *OpenAIGatewayService) shortenChatChunkToolCallIDs(chunk *apicompat.ChatCompletionsChunk) {
	if chunk == nil {
		return
	}
	for i := range chunk.Choices {
		calls := chunk.Choices[i].Delta.ToolCalls
		for j := range calls {
			calls[j].ID = s.shortenToolCallID(calls[j].ID)
		}
	}
}

func (s *OpenAIGatewayService) shortenChatResponseToolCallIDs(resp *apicompat.ChatCompletionsResponse) {
	if resp == nil {
		return
	}
	for i := range resp.Choices {
		calls := resp.Choices[i].Message.ToolCalls
		for j := range calls {
			calls[j].ID = s.shortenToolCallID(calls[j].ID)
		}
	}
}

// restoreGeminiChatCompletionsToolCallIDs 在发往 Gemini 的 CC 请求体里把别名还原
// 为原始 tool call id。缓存未命中时保留别名（调用与结果仍然配对）。
func (s *OpenAIGatewayService) restoreGeminiChatCompletionsToolCallIDs(body []byte) []byte {
	if !strings.Contains(string(body), toolCallIDAliasPrefix) || !gjson.ValidBytes(body) ||
		!isGeminiChatCompletionsModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	resolved := make(map[string]string)
	resolve := func(id string) string {
		if !strings.HasPrefix(id, toolCallIDAliasPrefix) {
			return ""
		}
		original, seen := resolved[id]
		if !seen {
			original = s.reasoningContentByID(toolCallIDAliasCachePrefix + id)
			resolved[id] = original
		}
		return original
	}
	type edit struct{ path, value string }
	var edits []edit
	for i, message := range gjson.GetBytes(body, "messages").Array() {
		if original := resolve(message.Get("tool_call_id").String()); original != "" {
			edits = append(edits, edit{fmt.Sprintf("messages.%d.tool_call_id", i), original})
		}
		for j, call := range message.Get("tool_calls").Array() {
			if original := resolve(call.Get("id").String()); original != "" {
				edits = append(edits, edit{fmt.Sprintf("messages.%d.tool_calls.%d.id", i, j), original})
			}
		}
	}
	out := body
	for _, e := range edits {
		updated, err := sjson.SetBytes(out, e.path, e.value)
		if err != nil {
			return body
		}
		out = updated
	}
	return out
}

// shortenResponsesInputCallIDs 把 Responses 请求历史（input[]）里超过上限的
// call_id 换成别名。会话在 Gemini 路由上留下的超长 id 被客户端原样回放到
// OpenAI Responses 上游时会 400；别名是确定性的，调用与结果仍然配对。
func (s *OpenAIGatewayService) shortenResponsesInputCallIDs(body []byte) []byte {
	if len(body) <= toolCallIDMaxLen {
		return body
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body
	}
	out := body
	for i, item := range input.Array() {
		callID := item.Get("call_id")
		if callID.Type != gjson.String || len(callID.String()) <= toolCallIDMaxLen {
			continue
		}
		updated, err := sjson.SetBytes(out, fmt.Sprintf("input.%d.call_id", i), s.shortenToolCallID(callID.String()))
		if err != nil {
			return body
		}
		out = updated
	}
	return out
}

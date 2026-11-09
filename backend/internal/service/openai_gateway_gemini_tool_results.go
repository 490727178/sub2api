package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiToolResultMaxNestingDepth is the maximum JSON nesting depth allowed
// before Google Cloud Vertex AI's protobuf Struct serializer fails with
// "400 INVALID_ARGUMENT: Request contains an invalid argument."
//
// When LiteLLM bridges an OpenAI Chat Completions tool message to Gemini's
// native functionResponse, it executes json.loads(content). If content parses
// as a JSON object, LiteLLM passes that dictionary to Google GenAI SDK, which
// tries to serialize it into a google.protobuf.Struct. Vertex AI enforces a hard
// Struct recursion limit (~38 levels). Deep structures (such as OS accessibility
// trees returned by agent-desktop with 40+ levels of container nesting) trigger
// this 400 rejection, which the gateway surfaces as 500 / 502.
//
// By wrapping such deeply nested tool results in a markdown code fence
// (```json\n...\n```), LiteLLM fails json.loads and falls back to treating it
// as plain string content ({"response":{"content":"..."}}, depth=1). Gemini's
// attention mechanism reads the full text including all nested fields without
// any loss of context, completely avoiding the protobuf Struct recursion crash.
const geminiToolResultMaxNestingDepth = 25

// normalizeGeminiChatCompletionsToolResults detects tool messages whose content
// is valid JSON with nesting depth >= geminiToolResultMaxNestingDepth and wraps
// them in a markdown code block to protect upstream Vertex AI / LiteLLM.
func normalizeGeminiChatCompletionsToolResults(body []byte) []byte {
	if !isGeminiChatCompletionsModel(gjson.GetBytes(body, "model").String()) ||
		len(body) == 0 || !utf8.Valid(body) || !gjson.ValidBytes(body) {
		return body
	}
	messages := gjson.GetBytes(body, "messages")
	if !messages.IsArray() {
		return body
	}
	msgValues := messages.Array()
	for index, msg := range msgValues {
		if msg.Get("role").String() != "tool" {
			continue
		}
		contentVal := msg.Get("content")
		if contentVal.Type != gjson.String {
			continue
		}
		rawContent := contentVal.String()
		trimmed := strings.TrimSpace(rawContent)
		if (strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}")) ||
			(strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]")) {
			if jsonNestingDepth([]byte(trimmed)) >= geminiToolResultMaxNestingDepth {
				wrapped := "```json\n" + trimmed + "\n```"
				updated, err := sjson.SetBytes(body, fmt.Sprintf("messages.%d.content", index), wrapped)
				if err == nil {
					body = updated
				}
			}
		}
	}
	return body
}

// jsonNestingDepth calculates the maximum nesting depth of a JSON byte slice
// using a streaming tokenizer without allocating objects or maps.
func jsonNestingDepth(data []byte) int {
	dec := json.NewDecoder(bytes.NewReader(data))
	maxDepth := 0
	currDepth := 0
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch tok {
		case json.Delim('{'), json.Delim('['):
			currDepth++
			if currDepth > maxDepth {
				maxDepth = currDepth
			}
		case json.Delim('}'), json.Delim(']'):
			currDepth--
		}
	}
	return maxDepth
}

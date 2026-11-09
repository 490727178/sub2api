package service

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// geminiChatCompletionsMaxOutputTokens is the hard upper bound Vertex AI accepts
// for Gemini maxOutputTokens. Values >= 65537 are rejected with:
//
//	400 INVALID_ARGUMENT: "Unable submit request because maxOutputTokens value
//	128000 but range from 1 (inclusive) 65537 (exclusive). Update value again."
//
// Clients speaking Responses natively (Codex sends max_output_tokens=128000)
// map onto Gemini through LiteLLM, which forwards the value verbatim. Without a
// clamp the upstream 400 is surfaced by the gateway as 502.
const geminiChatCompletionsMaxOutputTokens = 65536

// clampGeminiChatCompletionsMaxTokens applies a one-way clamp to
// max_tokens / max_completion_tokens on the final outbound Chat Completions body
// for Gemini-family models. Both fields are handled because the two ingress
// bridges disagree: the Anthropic bridge emits max_tokens, the Responses bridge
// emits max_completion_tokens.
//
// Only oversized values are rewritten, so clients requesting a smaller budget
// keep their exact value. Model detection reuses isGeminiChatCompletionsModel so
// the rule matches normalizeGeminiChatCompletionsToolSchemas on the same body.
func clampGeminiChatCompletionsMaxTokens(body []byte) []byte {
	if !gjson.ValidBytes(body) || !isGeminiChatCompletionsModel(gjson.GetBytes(body, "model").String()) {
		return body
	}
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		result := gjson.GetBytes(body, key)
		if !result.Exists() || result.Type != gjson.Number || result.Int() <= geminiChatCompletionsMaxOutputTokens {
			continue
		}
		updated, err := sjson.SetBytes(body, key, geminiChatCompletionsMaxOutputTokens)
		if err != nil {
			return body
		}
		body = updated
	}
	return body
}

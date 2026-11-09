//go:build unit

package service

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func buildNestedJSON(depth int) string {
	if depth <= 1 {
		return `{"leaf":"val"}`
	}
	res := `{"leaf":"val"}`
	for i := 1; i < depth; i++ {
		res = `{"k":` + res + `}`
	}
	return res
}

func TestJsonNestingDepth(t *testing.T) {
	require.Equal(t, 0, jsonNestingDepth([]byte(`"hello"`)))
	require.Equal(t, 0, jsonNestingDepth([]byte(`123`)))
	require.Equal(t, 1, jsonNestingDepth([]byte(`{"a": 1}`)))
	require.Equal(t, 1, jsonNestingDepth([]byte(`[1, 2, 3]`)))
	require.Equal(t, 2, jsonNestingDepth([]byte(`{"a": [1, 2]}`)))
	require.Equal(t, 5, jsonNestingDepth([]byte(`{"a":{"b":{"c":{"d":{"e":1}}}}}`)))

	deep := buildNestedJSON(30)
	require.Equal(t, 30, jsonNestingDepth([]byte(deep)))
}

func TestNormalizeGeminiChatCompletionsToolResults_DeepJSONWrapped(t *testing.T) {
	deepJSON := buildNestedJSON(30)
	req := map[string]any{
		"model": "gemini-3.8-flash",
		"messages": []map[string]any{
			{"role": "user", "content": "run snapshot"},
			{"role": "assistant", "content": nil, "tool_calls": []map[string]any{{"id": "call_1", "type": "function"}}},
			{"role": "tool", "tool_call_id": "call_1", "content": deepJSON},
		},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	normalized := normalizeGeminiChatCompletionsToolResults(body)
	resContent := gjson.GetBytes(normalized, "messages.2.content").String()
	require.True(t, strings.HasPrefix(resContent, "```json\n"))
	require.True(t, strings.HasSuffix(resContent, "\n```"))
	require.Contains(t, resContent, deepJSON)
}

func TestNormalizeGeminiChatCompletionsToolResults_ShallowJSONUnchanged(t *testing.T) {
	shallowJSON := `{"status":"ok","code":200}`
	req := map[string]any{
		"model": "gemini-3.8-flash",
		"messages": []map[string]any{
			{"role": "tool", "tool_call_id": "call_1", "content": shallowJSON},
		},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	normalized := normalizeGeminiChatCompletionsToolResults(body)
	resContent := gjson.GetBytes(normalized, "messages.0.content").String()
	require.Equal(t, shallowJSON, resContent)
}

func TestNormalizeGeminiChatCompletionsToolResults_NonGeminiModelUnchanged(t *testing.T) {
	deepJSON := buildNestedJSON(30)
	req := map[string]any{
		"model": "gpt-5.4",
		"messages": []map[string]any{
			{"role": "tool", "tool_call_id": "call_1", "content": deepJSON},
		},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	normalized := normalizeGeminiChatCompletionsToolResults(body)
	resContent := gjson.GetBytes(normalized, "messages.0.content").String()
	require.Equal(t, deepJSON, resContent)
}

func TestNormalizeGeminiChatCompletionsToolResults_NonToolRoleUnchanged(t *testing.T) {
	deepJSON := buildNestedJSON(30)
	req := map[string]any{
		"model": "gemini-3.8-flash",
		"messages": []map[string]any{
			{"role": "user", "content": deepJSON},
		},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	normalized := normalizeGeminiChatCompletionsToolResults(body)
	resContent := gjson.GetBytes(normalized, "messages.0.content").String()
	require.Equal(t, deepJSON, resContent)
}

func TestNormalizeGeminiChatCompletionsToolResults_AlreadyFencedIdempotent(t *testing.T) {
	deepJSON := buildNestedJSON(30)
	fenced := "```json\n" + deepJSON + "\n```"
	req := map[string]any{
		"model": "gemini-3.8-flash",
		"messages": []map[string]any{
			{"role": "tool", "tool_call_id": "call_1", "content": fenced},
		},
	}
	body, err := json.Marshal(req)
	require.NoError(t, err)

	normalized := normalizeGeminiChatCompletionsToolResults(body)
	resContent := gjson.GetBytes(normalized, "messages.0.content").String()
	require.Equal(t, fenced, resContent)
}

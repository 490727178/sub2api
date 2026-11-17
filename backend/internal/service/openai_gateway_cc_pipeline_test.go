package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tidwall/gjson"
)

func TestClampGeminiChatCompletionsReasoningEffort(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "effort max mapped to high",
			body: `{"model":"gemini-3.8-flash","reasoning_effort":"max","messages":[]}`,
			want: "high",
		},
		{
			name: "effort xhigh mapped to high",
			body: `{"model":"gemini-3.8-flash","reasoning_effort":"xhigh","messages":[]}`,
			want: "high",
		},
		{
			name: "effort ultra mapped to high",
			body: `{"model":"gemini-3.8-flash","reasoning_effort":"ultra","messages":[]}`,
			want: "high",
		},
		{
			name: "effort medium unchanged",
			body: `{"model":"gemini-3.8-flash","reasoning_effort":"medium","messages":[]}`,
			want: "medium",
		},
		{
			name: "effort low unchanged",
			body: `{"model":"gemini-3.8-flash","reasoning_effort":"low","messages":[]}`,
			want: "low",
		},
		{
			name: "effort high unchanged",
			body: `{"model":"gemini-3.8-flash","reasoning_effort":"high","messages":[]}`,
			want: "high",
		},
		{
			name: "non-gemini glm keeps xhigh",
			body: `{"model":"glm-5.2","reasoning_effort":"xhigh","messages":[]}`,
			want: "xhigh",
		},
		{
			name: "non-gemini gpt keeps max",
			body: `{"model":"gpt-5.6-luna","reasoning_effort":"max","messages":[]}`,
			want: "max",
		},
		{
			name: "provider-prefixed gemini is not treated as gemini",
			body: `{"model":"openai/gemini-3.8-flash","reasoning_effort":"max","messages":[]}`,
			want: "max",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clampGeminiChatCompletionsReasoningEffort([]byte(tt.body))
			effort := gjson.GetBytes(got, "reasoning_effort").String()
			assert.Equal(t, tt.want, effort)
		})
	}
}

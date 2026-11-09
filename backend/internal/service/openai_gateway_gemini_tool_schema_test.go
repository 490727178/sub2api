//go:build unit

package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const geminiMCPToolParameterSchema = `{
  "type": "object",
  "properties": {
    "resolution": {
      "anyOf": [
        {
          "oneOf": [
            {
              "type": "object",
              "properties": {
                "disposition": {"type": "string", "enum": ["verified"]},
                "evidence_ids": {"type": "array", "items": {"type": "string"}, "minItems": 1},
                "rationale": {"type": "string"}
              },
              "required": ["disposition", "rationale", "evidence_ids"],
              "additionalProperties": false
            },
            {
              "type": "object",
              "properties": {
                "disposition": {"type": "string", "enum": ["withdrawn", "out-of-scope"]},
                "evidence_ids": {"type": "array", "items": {"type": "string"}},
                "rationale": {"type": "string"}
              },
              "required": ["disposition", "rationale", "evidence_ids"],
              "additionalProperties": false
            }
          ]
        },
        {"type": "null"}
      ]
    }
  },
  "required": ["resolution"],
  "additionalProperties": false
}`

func TestNormalizeGeminiChatCompletionsToolSchemas(test *testing.T) {
	cases := []struct {
		name   string
		schema string
		want   string
	}{
		{"nullable-any-of", `{"anyOf":[{"type":"string"},{"type":"null"}]}`, `{"type":"string","nullable":true}`},
		{"nullable-type-array", `{"type":["string","null"]}`, `{"type":"string","nullable":true}`},
		{"enum-without-type", `{"enum":["read","write"]}`, `{"type":"string","enum":["read","write"]}`},
		{"disjoint-string-one-of", `{"oneOf":[{"type":"string","enum":["read"]},{"type":"string","enum":["write"]}]}`, `{"type":"string","anyOf":[{"type":"string","enum":["read"]},{"type":"string","enum":["write"]}]}`},
		{"overlapping-objects", `{"oneOf":[{"type":"object","properties":{"first":{"type":"string"}}},{"type":"object","properties":{"second":{"type":"string"}}}]}`, ""},
		{"optional-discriminator", `{"oneOf":[{"type":"object","properties":{"mode":{"type":"string","enum":["a"]}}},{"type":"object","properties":{"mode":{"type":"string","enum":["b"]}}}]}`, ""},
		{"overlapping-enums", `{"oneOf":[{"type":"object","properties":{"mode":{"type":"string","enum":["a","b"]}},"required":["mode"]},{"type":"object","properties":{"mode":{"type":"string","enum":["b","c"]}},"required":["mode"]}]}`, ""},
		{"primitive-properties-are-not-discriminators", `{"oneOf":[{"type":"string","properties":{"mode":{"type":"string","enum":["a"]}},"required":["mode"]},{"type":"string","properties":{"mode":{"type":"string","enum":["b"]}},"required":["mode"]}]}`, `{"type":"string","anyOf":[{"type":"string","properties":{"mode":{"type":"string","enum":["a"]}},"required":["mode"]},{"type":"string","properties":{"mode":{"type":"string","enum":["b"]}},"required":["mode"]}]}`},
		{"unknown-type", `{"anyOf":[{},{"type":"null"}]}`, ""},
		{"null-excluded-by-not", `{"anyOf":[{"type":"string"},{"type":"null"}],"not":{"type":"null"}}`, ""},
		{"explicit-nonnullable-type", `{"type":"string","anyOf":[{"type":"string"},{"type":"null"}]}`, `{"type":"string"}`},
		{"heterogeneous-types", `{"type":["string","number","null"]}`, ""},
		{"nullable-enum-constraint", `{"type":["string","null"],"enum":["red"]}`, ""},
		{"boolean-schema-branch", `{"anyOf":[true,{"type":"null"}]}`, ""},
		{"unresolved-reference", `{"anyOf":[{"$ref":"#/$defs/unknown"},{"type":"null"}]}`, ""},
		{"empty-string-enum", `{"type":"string","enum":[""]}`, `{"type":"string"}`},
		{"mixed-empty-string-enum", `{"type":"string","enum":["","valid"]}`, `{"type":"string","enum":["valid"]}`},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			schema := fmt.Sprintf(`{"type":"object","properties":{"value":%s}}`, testCase.schema)
			body := geminiToolSchemaRequestBody("gemini-3.8-flash", schema)
			normalized := normalizeGeminiChatCompletionsToolSchemas(body)
			if testCase.want == "" {
				require.Equal(test, body, normalized)
				return
			}
			require.JSONEq(test, testCase.want, gjson.GetBytes(normalized, "tools.0.function.parameters.properties.value").Raw)
			require.Equal(test, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(normalized, "messages").Raw)
			require.Equal(test, gjson.GetBytes(body, "metadata").Raw, gjson.GetBytes(normalized, "metadata").Raw)
		})
	}
}

func TestNormalizeGeminiChatCompletionsToolSchemas_ProviderModelNames(test *testing.T) {
	for _, model := range []string{
		"gemini-3.8-flash",
		"GEMINI-3.8-FLASH",
	} {
		test.Run(model, func(test *testing.T) {
			body := geminiToolSchemaRequestBody(model, geminiMCPToolParameterSchema)
			resolution := gjson.GetBytes(normalizeGeminiChatCompletionsToolSchemas(body), "tools.0.function.parameters.properties.resolution")
			require.Equal(test, "object", resolution.Get("type").String())
			require.True(test, resolution.Get("nullable").Bool())
		})
	}
	for _, model := range []string{"gemini", "gemini/gemini-3.8-flash", "not-gemini/gemini-3.8-flash", "openai/gpt-6.1-sol"} {
		test.Run("unchanged/"+model, func(test *testing.T) {
			body := geminiToolSchemaRequestBody(model, geminiMCPToolParameterSchema)
			require.Equal(test, body, normalizeGeminiChatCompletionsToolSchemas(body))
		})
	}
}

func TestNormalizeGeminiChatCompletionsToolSchemas_PreservesLiterals(test *testing.T) {
	literal := `{"type":null,"anyOf":[{"type":"null"}],"oneOf":[{"type":"object"}],"number":9007199254740993}`
	schema := fmt.Sprintf(`{"type":"object","properties":{"value":{"anyOf":[{"type":"string"},{"type":"null"}]},"literal":{"type":"object","default":%s,"const":%s,"enum":[%s],"examples":[%s]}}}`, literal, literal, literal, literal)
	body := geminiToolSchemaRequestBody("gemini-3.8-flash", schema)
	normalized := normalizeGeminiChatCompletionsToolSchemas(body)
	parameters := gjson.GetBytes(normalized, "tools.0.function.parameters")
	require.Equal(test, "string", parameters.Get("properties.value.type").String())
	require.JSONEq(test, gjson.GetBytes(body, "tools.0.function.parameters.properties.literal").Raw, parameters.Get("properties.literal").Raw)
	for _, path := range []string{"default", "const", "enum.0", "examples.0"} {
		value := parameters.Get("properties.literal." + path)
		require.Equal(test, "null", value.Get("type").Raw)
		require.Equal(test, "9007199254740993", value.Get("number").Raw)
	}
	require.Equal(test, gjson.GetBytes(body, "messages").Raw, gjson.GetBytes(normalized, "messages").Raw)
	require.Equal(test, gjson.GetBytes(body, "metadata").Raw, gjson.GetBytes(normalized, "metadata").Raw)
}

func TestNormalizeGeminiChatCompletionsToolSchemas_SchemaPositions(test *testing.T) {
	nullable := `{"anyOf":[{"type":"string"},{"type":"null"}]}`
	schema := fmt.Sprintf(`{"type":"object","$defs":{"choice":%s},"definitions":{"choice":%s},"dependencies":{"field":["other"]},"properties":{"array":{"type":"array","items":%s},"dictionary":{"type":"object","additionalProperties":%s},"reference":{"$ref":"#/$defs/choice"}}}`, nullable, nullable, nullable, nullable)
	body := geminiToolSchemaRequestBody("gemini-3.8-flash", schema)
	parameters := gjson.GetBytes(normalizeGeminiChatCompletionsToolSchemas(body), "tools.0.function.parameters")
	for _, path := range []string{"$defs.choice", "definitions.choice", "properties.array.items", "properties.dictionary.additionalProperties"} {
		require.Equal(test, "string", parameters.Get(path+".type").String())
		require.True(test, parameters.Get(path+".nullable").Bool())
	}
	require.JSONEq(test, `{"$ref":"#/$defs/choice"}`, parameters.Get("properties.reference").Raw)
	require.Equal(test, `["other"]`, parameters.Get("dependencies.field").Raw)
}

func TestNormalizeGeminiChatCompletionsToolSchemas_EmptyEnumCleaned(test *testing.T) {
	body := []byte(`{"model":"gemini-3.8-flash","tools":[{"type":"function","function":{"name":"test_tool","parameters":{"type":"object","properties":{"seed":{"anyOf":[{"type":"object","properties":{"value":{"type":"string","enum":[""]}}},{"type":"object","properties":{"value":{"type":"string","enum":["valid"]}}}]}}}}}]}`)
	normalized := normalizeGeminiChatCompletionsToolSchemas(body)
	valEnum := gjson.GetBytes(normalized, "tools.0.function.parameters.properties.seed.anyOf.0.properties.value.enum")
	require.False(test, valEnum.Exists())
	require.Equal(test, "string", gjson.GetBytes(normalized, "tools.0.function.parameters.properties.seed.anyOf.0.properties.value.type").String())
	require.Equal(test, "valid", gjson.GetBytes(normalized, "tools.0.function.parameters.properties.seed.anyOf.1.properties.value.enum.0").String())
}

func TestNormalizeGeminiChatCompletionsToolSchemas_Limits(test *testing.T) {
	deepSchema := `{"type":"object"}`
	for depth := 0; depth <= openAIResponsesToolSchemaMaxDepth; depth++ {
		deepSchema = fmt.Sprintf(`{"type":"object","properties":{"next":%s}}`, deepSchema)
	}
	branches := make([]string, geminiToolSchemaMaxUnionBranches+1)
	for index := range branches {
		branches[index] = fmt.Sprintf(`{"type":"string","enum":["choice-%d"]}`, index)
	}
	oversizedSchema := fmt.Sprintf(`{"type":"object","default":%q}`, strings.Repeat("x", openAIResponsesObjectUnionMaxSize))
	twoTools := func(schema string) []byte {
		return []byte(fmt.Sprintf(`{"model":"gemini-3.8-flash","tools":[{"type":"function","function":{"name":"eligible","parameters":%s}},{"type":"function","function":{"name":"limited","parameters":%s}}]}`, geminiMCPToolParameterSchema, schema))
	}
	cases := []struct {
		name string
		body []byte
	}{
		{"non-gemini", geminiToolSchemaRequestBody("llama-3.3", geminiMCPToolParameterSchema)},
		{"no-tools", []byte(`{"model":"gemini-3.8-flash","messages":[]}`)},
		{"invalid-json", []byte(`{"model":"gemini-3.8-flash","tools":[`)},
		{"invalid-utf8", append(geminiToolSchemaRequestBody("gemini-3.8-flash", geminiMCPToolParameterSchema), 0xff)},
		{"depth-is-atomic", twoTools(deepSchema)},
		{"size-is-atomic", twoTools(oversizedSchema)},
		{"wide-union", geminiToolSchemaRequestBody("gemini-3.8-flash", `{"type":"object","properties":{"value":{"oneOf":[`+strings.Join(branches, ",")+`]}}}`)},
	}
	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			require.Equal(test, testCase.body, normalizeGeminiChatCompletionsToolSchemas(testCase.body))
		})
	}
}

func geminiToolSchemaRequestBody(model, schema string) []byte {
	return []byte(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"literal {\"oneOf\":[]}"}],"metadata":{"number":9007199254740993},"tools":[{"type":"function","function":{"name":"test_tool","parameters":%s}}]}`, model, schema))
}

func TestForwardChatCompletions_GeminiToolSchemaCompatibility(test *testing.T) {
	gin.SetMode(gin.TestMode)

	requests := []struct {
		name     string
		endpoint string
		template string
	}{
		{
			name:     "responses",
			endpoint: "/v1/responses",
			template: `{"model":%q,"input":"hello","stream":%t,"tools":[{"type":"function","name":"mcp__rea__update_unknown","parameters":%s}]}`,
		},
		{
			name:     "chat-completions",
			endpoint: "/v1/chat/completions",
			template: `{"model":%q,"messages":[{"role":"user","content":"hello"}],"stream":%t,"tools":[{"type":"function","function":{"name":"mcp__rea__update_unknown","parameters":%s}}]}`,
		},
		{
			name:     "messages",
			endpoint: "/v1/messages",
			template: `{"model":%q,"messages":[{"role":"user","content":"hello"}],"max_tokens":64,"stream":%t,"tools":[{"name":"mcp__rea__update_unknown","input_schema":%s}]}`,
		},
	}
	models := []struct {
		name           string
		requestedModel string
		upstreamModel  string
		wantNormalized bool
	}{
		{"mapped-gemini", "gpt-6.1-sol", "gemini-3.8-flash", true},
		{"mapped-away-from-gemini", "gemini-3.8-flash", "llama-3.3", false},
	}

	for _, requestCase := range requests {
		for _, modelCase := range models {
			for _, stream := range []bool{false, true} {
				name := fmt.Sprintf("%s/%s/stream=%t", requestCase.name, modelCase.name, stream)
				test.Run(name, func(test *testing.T) {
					body := []byte(fmt.Sprintf(requestCase.template, modelCase.requestedModel, stream, geminiMCPToolParameterSchema))
					responseRecorder := httptest.NewRecorder()
					requestContext, _ := gin.CreateTestContext(responseRecorder)
					requestContext.Request = httptest.NewRequest(http.MethodPost, requestCase.endpoint, bytes.NewReader(body))
					requestContext.Request.Header.Set("Content-Type", "application/json")

					responseBody := `{"id":"chatcmpl_schema","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
					contentType := "application/json"
					if stream {
						responseBody = "data: {\"id\":\"chatcmpl_schema\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"
						contentType = "text/event-stream"
					}
					upstream := &httpUpstreamRecorder{resp: &http.Response{
						StatusCode: http.StatusOK,
						Header:     http.Header{"Content-Type": []string{contentType}},
						Body:       io.NopCloser(strings.NewReader(responseBody)),
					}}
					gateway := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
					account := forceChatResponsesFallbackAccount()
					account.Credentials["model_mapping"] = map[string]any{modelCase.requestedModel: modelCase.upstreamModel}

					var result *OpenAIForwardResult
					var forwardError error
					switch requestCase.endpoint {
					case "/v1/responses":
						result, forwardError = gateway.Forward(context.Background(), requestContext, account, body)
					case "/v1/chat/completions":
						result, forwardError = gateway.forwardAsRawChatCompletions(context.Background(), requestContext, account, body, "")
					case "/v1/messages":
						result, forwardError = gateway.ForwardAsAnthropic(context.Background(), requestContext, account, body, "", "")
					}
					require.NoError(test, forwardError)
					require.NotNil(test, result)
					require.Equal(test, http.StatusOK, responseRecorder.Code)
					require.Equal(test, modelCase.upstreamModel, gjson.GetBytes(upstream.lastBody, "model").String())
					require.Equal(test, modelCase.upstreamModel, result.UpstreamModel)
					require.Equal(test, "/v1/chat/completions", GetActualOpenAIUpstreamEndpoint(requestContext))

					parameters := gjson.GetBytes(upstream.lastBody, "tools.0.function.parameters")
					if !modelCase.wantNormalized {
						require.JSONEq(test, geminiMCPToolParameterSchema, parameters.Raw)
						return
					}
					resolution := parameters.Get("properties.resolution")
					require.Equal(test, "object", resolution.Get("type").String())
					require.True(test, resolution.Get("nullable").Bool())
					require.NotContains(test, resolution.Raw, `"oneOf"`)
					require.Len(test, resolution.Get("anyOf").Array(), 2)
					require.Equal(test, "verified", resolution.Get("anyOf.0.properties.disposition.enum.0").String())
					require.Equal(test, int64(1), resolution.Get("anyOf.0.properties.evidence_ids.minItems").Int())
					require.Equal(test, "withdrawn", resolution.Get("anyOf.1.properties.disposition.enum.0").String())
					require.Equal(test, "out-of-scope", resolution.Get("anyOf.1.properties.disposition.enum.1").String())
				})
			}
		}
	}
}

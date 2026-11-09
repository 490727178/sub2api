package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const geminiToolSchemaMaxUnionBranches = 64

func normalizeGeminiChatCompletionsToolSchemas(body []byte) []byte {
	if !isGeminiChatCompletionsModel(gjson.GetBytes(body, "model").String()) || len(body) > openAIResponsesToolSchemaMaxBodySize ||
		!utf8.Valid(body) || !gjson.ValidBytes(body) {
		return body
	}
	tools := gjson.GetBytes(body, "tools")
	if !tools.IsArray() {
		return body
	}
	toolValues := tools.Array()
	normalizedTools := make([]json.RawMessage, len(toolValues))
	changed := false
	for index, tool := range toolValues {
		normalizedTools[index] = json.RawMessage(tool.Raw)
		parameters := tool.Get("function.parameters")
		if tool.Get("type").String() != "function" || !parameters.IsObject() {
			continue
		}
		if len(parameters.Raw) > openAIResponsesObjectUnionMaxSize {
			return body
		}
		var schema map[string]any
		decoder := json.NewDecoder(strings.NewReader(parameters.Raw))
		decoder.UseNumber()
		if err := decoder.Decode(&schema); err != nil {
			return body
		}
		schemaChanged, err := normalizeGeminiToolSchemaNode(schema, 0)
		if err != nil {
			return body
		}
		if !schemaChanged {
			continue
		}
		encodedSchema, err := json.Marshal(schema)
		if err != nil {
			return body
		}
		normalizedTool, err := sjson.SetRawBytes([]byte(tool.Raw), "function.parameters", encodedSchema)
		if err != nil {
			return body
		}
		normalizedTools[index] = normalizedTool
		changed = true
	}
	if !changed {
		return body
	}
	encodedTools, err := json.Marshal(normalizedTools)
	if err != nil {
		return body
	}
	normalizedBody, err := sjson.SetRawBytes(body, "tools", encodedTools)
	if err != nil {
		return body
	}
	return normalizedBody
}

func isGeminiChatCompletionsModel(model string) bool {
	model = strings.TrimSpace(model)
	if strings.ContainsAny(model, "/:") {
		return false
	}
	return strings.HasPrefix(strings.ToLower(model), "gemini-")
}

func normalizeGeminiToolSchemaNode(schema map[string]any, depth int) (bool, error) {
	if depth > openAIResponsesToolSchemaMaxDepth {
		return false, fmt.Errorf("Gemini tool schema exceeds maximum depth")
	}
	changed := false
	for _, keyword := range []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas", "dependencies"} {
		children, _ := schema[keyword].(map[string]any)
		for _, value := range children {
			if child, ok := value.(map[string]any); ok {
				childChanged, err := normalizeGeminiToolSchemaNode(child, depth+1)
				if err != nil {
					return false, err
				}
				changed = changed || childChanged
			}
		}
	}
	for _, keyword := range []string{
		"items", "additionalProperties", "additionalItems", "contains", "propertyNames",
		"not", "if", "then", "else", "unevaluatedProperties", "unevaluatedItems", "contentSchema",
		"anyOf", "oneOf", "allOf", "prefixItems",
	} {
		children := []any{schema[keyword]}
		if array, ok := schema[keyword].([]any); ok {
			children = array
		}
		for _, value := range children {
			if child, ok := value.(map[string]any); ok {
				childChanged, err := normalizeGeminiToolSchemaNode(child, depth+1)
				if err != nil {
					return false, err
				}
				changed = changed || childChanged
			}
		}
	}
	if typeValues, ok := schema["type"].([]any); ok {
		commonType := ""
		nullable := false
		for _, value := range typeValues {
			typeName, ok := value.(string)
			if !ok || (typeName != "null" && commonType != "" && commonType != typeName) {
				commonType = ""
				break
			}
			if typeName == "null" {
				nullable = true
			} else {
				commonType = typeName
			}
		}
		if commonType != "" && (!nullable || geminiToolSchemaAllowsNullableUnion(schema, "")) {
			schema["type"] = commonType
			if nullable {
				schema["nullable"] = true
			}
			changed = true
		}
	}
	if enumRaw, ok := schema["enum"].([]any); ok {
		var cleanedEnum []any
		enumModified := false
		for _, item := range enumRaw {
			if strVal, isStr := item.(string); isStr && strings.TrimSpace(strVal) == "" {
				enumModified = true
				continue
			}
			cleanedEnum = append(cleanedEnum, item)
		}
		if enumModified {
			if len(cleanedEnum) == 0 {
				delete(schema, "enum")
			} else {
				schema["enum"] = cleanedEnum
			}
			changed = true
		}
	}

	if enumRaw, ok := schema["enum"].([]any); ok {
		var cleanedEnum []any
		enumModified := false
		for _, item := range enumRaw {
			if strVal, isStr := item.(string); isStr && strings.TrimSpace(strVal) == "" {
				enumModified = true
				continue
			}
			cleanedEnum = append(cleanedEnum, item)
		}
		if enumModified {
			if len(cleanedEnum) == 0 {
				delete(schema, "enum")
			} else {
				schema["enum"] = cleanedEnum
			}
			changed = true
		}
	}
	if _, exists := schema["type"]; !exists && len(geminiToolSchemaStringEnum(schema)) > 0 {
		schema["type"] = "string"
		changed = true
	}
	for _, keyword := range []string{"oneOf", "anyOf"} {
		unionChanged := normalizeGeminiToolSchemaUnion(schema, keyword)
		changed = changed || unionChanged
	}
	return changed, nil
}

func normalizeGeminiToolSchemaUnion(schema map[string]any, keyword string) bool {
	branches, ok := schema[keyword].([]any)
	if !ok || len(branches) == 0 || len(branches) > geminiToolSchemaMaxUnionBranches {
		return false
	}
	if _, hasOneOf := schema["oneOf"]; hasOneOf {
		if _, hasAnyOf := schema["anyOf"]; hasAnyOf {
			return false
		}
	}
	commonType := ""
	nullable := false
	nonNullBranches := make([]any, 0, len(branches))
	for _, value := range branches {
		branch, ok := value.(map[string]any)
		if !ok {
			return false
		}
		typeName, _ := branch["type"].(string)
		if typeName == "null" && len(branch) == 1 {
			nullable = true
			continue
		}
		if typeName == "" || typeName == "null" || (commonType != "" && commonType != typeName) {
			return false
		}
		commonType = typeName
		branchNullable, _ := branch["nullable"].(bool)
		nullable = nullable || branchNullable
		nonNullBranches = append(nonNullBranches, branch)
	}
	if commonType == "" || (keyword == "oneOf" && !geminiToolSchemaBranchesDisjoint(branches)) {
		return false
	}
	declaredType, _ := schema["type"].(string)
	if _, exists := schema["type"]; exists && declaredType != commonType {
		return false
	}
	if declaredType == "" && nullable && !geminiToolSchemaAllowsNullableUnion(schema, keyword) {
		return false
	}
	changed := false
	if declaredType == "" {
		schema["type"] = commonType
		if nullable {
			schema["nullable"] = true
		}
		changed = true
	}
	if keyword == "oneOf" {
		delete(schema, "oneOf")
		keyword = "anyOf"
		schema[keyword] = nonNullBranches
		changed = true
	} else if len(nonNullBranches) != len(branches) {
		schema[keyword] = nonNullBranches
		changed = true
	}
	if len(nonNullBranches) == 1 {
		branch := nonNullBranches[0].(map[string]any)
		for key := range branch {
			if _, exists := schema[key]; exists && key != keyword && key != "type" && key != "nullable" {
				return changed
			}
		}
		delete(schema, keyword)
		for key, value := range branch {
			if key != "type" && key != "nullable" {
				schema[key] = value
			}
		}
		changed = true
	}
	return changed
}

func geminiToolSchemaAllowsNullableUnion(schema map[string]any, unionKeyword string) bool {
	for _, keyword := range []string{"$ref", "enum", "const", "not", "allOf", "if", "then", "else", "oneOf", "anyOf"} {
		if keyword != unionKeyword {
			if _, exists := schema[keyword]; exists {
				return false
			}
		}
	}
	return true
}

func geminiToolSchemaBranchesDisjoint(branches []any) bool {
	for leftIndex, leftValue := range branches {
		left := leftValue.(map[string]any)
		for _, rightValue := range branches[leftIndex+1:] {
			right := rightValue.(map[string]any)
			leftType, _ := left["type"].(string)
			rightType, _ := right["type"].(string)
			leftNullable, _ := left["nullable"].(bool)
			rightNullable, _ := right["nullable"].(bool)
			if leftNullable && rightNullable {
				return false
			}
			if leftType == "null" || rightType == "null" {
				if leftType == rightType || leftNullable || rightNullable {
					return false
				}
				continue
			}
			if !geminiToolSchemaEnumsDisjoint(left, right) {
				return false
			}
		}
	}
	return true
}

func geminiToolSchemaEnumsDisjoint(left, right map[string]any) bool {
	leftEnum := geminiToolSchemaStringEnum(left)
	rightEnum := geminiToolSchemaStringEnum(right)
	if len(leftEnum) > 0 && len(rightEnum) > 0 && !geminiToolSchemaEnumsOverlap(leftEnum, rightEnum) {
		return true
	}
	leftProperties, _ := left["properties"].(map[string]any)
	rightProperties, _ := right["properties"].(map[string]any)
	leftRequired, _ := left["required"].([]any)
	rightRequired, _ := right["required"].([]any)
	for _, leftName := range leftRequired {
		name, ok := leftName.(string)
		if !ok {
			continue
		}
		for _, rightName := range rightRequired {
			if rightName != name {
				continue
			}
			leftProperty, _ := leftProperties[name].(map[string]any)
			rightProperty, _ := rightProperties[name].(map[string]any)
			leftEnum := geminiToolSchemaStringEnum(leftProperty)
			rightEnum := geminiToolSchemaStringEnum(rightProperty)
			if len(leftEnum) > 0 && len(rightEnum) > 0 && !geminiToolSchemaEnumsOverlap(leftEnum, rightEnum) {
				return true
			}
		}
	}
	return false
}

func geminiToolSchemaStringEnum(schema map[string]any) []string {
	values, ok := schema["enum"].([]any)
	if !ok || len(values) == 0 {
		return nil
	}
	enum := make([]string, 0, len(values))
	for _, value := range values {
		text, ok := value.(string)
		if !ok {
			return nil
		}
		enum = append(enum, text)
	}
	return enum
}

func geminiToolSchemaEnumsOverlap(left, right []string) bool {
	for _, leftValue := range left {
		for _, rightValue := range right {
			if leftValue == rightValue {
				return true
			}
		}
	}
	return false
}

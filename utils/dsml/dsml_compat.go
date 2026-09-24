// dsml_compat.go
//
// A small compatibility layer for DeepSeek-style DSML tool calls.
//
// Usage:
//   msg := NormalizeAssistantMessage(rawMsg)
//   if len(msg.ToolCalls) > 0 {
//       // Execute msg.ToolCalls just like OpenAI-compatible tool_calls.
//   }
//
// This file has no third-party dependencies.

package dsml

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"html"
	"regexp"
	"strings"
)

const (
	// DSML 是 DeepSeek 工具调用 XML-like 标签中的命名空间标记。
	DSML = "｜DSML｜"
	// BOS 是 DeepSeek 可能返回的句首特殊 token。
	BOS = "<｜begin▁of▁sentence｜>"
	// EOS 是 DeepSeek 可能返回的句尾特殊 token。
	EOS = "<｜end▁of▁sentence｜>"
)

// ToolCall 是 OpenAI-compatible 的工具调用结构。
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"` // always "function"
	Function ToolFunction `json:"function"`
}

// ToolFunction 描述工具函数名与 JSON 字符串参数。
type ToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // JSON string
}

// AssistantMessage 是本兼容层使用的 assistant 消息结构。
type AssistantMessage struct {
	Role              string     `json:"role"`
	Content           string     `json:"content,omitempty"`
	ToolCalls         []ToolCall `json:"tool_calls,omitempty"`
	ReasoningContent  string     `json:"reasoning_content,omitempty"`
	FinishReason      string     `json:"finish_reason,omitempty"`
	DSMLParseWarnings []string   `json:"dsml_parse_warnings,omitempty"`
}

// NormalizeAssistantMessage converts DeepSeek DSML tool calls in Content
// into OpenAI-compatible ToolCalls.
//
// It supports:
//  1. Native OpenAI-compatible tool_calls: returned as-is.
//  2. DeepSeek DSML in content: parsed into tool_calls.
//  3. Plain assistant text: returned as normal content.
//
// Important:
// Do not rely only on finish_reason == "tool_calls".
// Some OpenAI-compatible DeepSeek deployments may return DSML in content
// while finish_reason is still "stop".
func NormalizeAssistantMessage(msg AssistantMessage) AssistantMessage {
	msg.Role = "assistant"

	// Already OpenAI-compatible.
	if len(msg.ToolCalls) > 0 {
		if msg.FinishReason == "" {
			msg.FinishReason = "tool_calls"
		}
		return msg
	}

	raw := strings.TrimSpace(msg.Content)
	raw = stripCodeFence(raw)
	raw = strings.ReplaceAll(raw, BOS, "")
	raw = strings.ReplaceAll(raw, EOS, "")

	reasoning, body := extractThinking(raw)
	if msg.ReasoningContent == "" {
		msg.ReasoningContent = reasoning
	}

	toolCalls, warnings := ParseDSMLToolCalls(body)

	contentWithoutDSML := removeDSMLToolCallBlocks(body)
	contentWithoutDSML = strings.TrimSpace(strings.ReplaceAll(contentWithoutDSML, EOS, ""))

	msg.Content = contentWithoutDSML
	msg.ToolCalls = toolCalls
	msg.DSMLParseWarnings = warnings

	if len(toolCalls) > 0 {
		msg.FinishReason = "tool_calls"

		// When the assistant is calling tools, content is usually empty.
		// If the model emitted both natural text and DSML, this preserves
		// the natural text after stripping the DSML block.
		msg.Content = strings.TrimSpace(msg.Content)
	}

	return msg
}

// ParseDSMLToolCalls parses all DSML tool call blocks from text.
func ParseDSMLToolCalls(text string) ([]ToolCall, []string) {
	var toolCalls []ToolCall
	var warnings []string

	toolBlockRe := regexp.MustCompile(
		`(?s)<` + regexp.QuoteMeta(DSML) + `tool_calls\s*>\s*(.*?)\s*</` + regexp.QuoteMeta(DSML) + `tool_calls\s*>`,
	)

	matches := toolBlockRe.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		calls, ws := parseToolCallBlock(m[1], len(toolCalls))
		toolCalls = append(toolCalls, calls...)
		warnings = append(warnings, ws...)
	}

	if len(toolCalls) == 0 && strings.Contains(text, "<"+DSML+"invoke") {
		warnings = append(warnings, "found DSML invoke marker but failed to parse tool calls")
	}

	return toolCalls, warnings
}

func parseToolCallBlock(block string, startIndex int) ([]ToolCall, []string) {
	var toolCalls []ToolCall
	var warnings []string

	invokeRe := regexp.MustCompile(
		`(?s)<` + regexp.QuoteMeta(DSML) + `invoke\s+([^>]*)>\s*(.*?)\s*</` + regexp.QuoteMeta(DSML) + `invoke\s*>`,
	)

	invokes := invokeRe.FindAllStringSubmatch(block, -1)

	for i, inv := range invokes {
		attrs := parseAttrs(inv[1])
		name := html.UnescapeString(attrs["name"])
		if name == "" {
			warnings = append(warnings, "tool invoke missing name")
			continue
		}

		args, ws := parseParameters(inv[2], name)
		warnings = append(warnings, ws...)

		argBytes, err := json.Marshal(args)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("failed to marshal arguments for tool %q: %v", name, err))
			argBytes = []byte("{}")
		}

		toolCalls = append(toolCalls, ToolCall{
			ID:   stableCallID(name, startIndex+i, argBytes),
			Type: "function",
			Function: ToolFunction{
				Name:      name,
				Arguments: string(argBytes),
			},
		})
	}

	return toolCalls, warnings
}

func parseParameters(body string, toolName string) (map[string]any, []string) {
	args := map[string]any{}
	var warnings []string

	paramRe := regexp.MustCompile(
		`(?s)<` + regexp.QuoteMeta(DSML) + `parameter\s+([^>]*)>(.*?)</` + regexp.QuoteMeta(DSML) + `parameter\s*>`,
	)

	params := paramRe.FindAllStringSubmatch(body, -1)

	for _, p := range params {
		attrs := parseAttrs(p[1])
		paramName := html.UnescapeString(attrs["name"])
		isString := attrs["string"] == "true"
		rawValue := html.UnescapeString(p[2])

		if paramName == "" {
			warnings = append(warnings, fmt.Sprintf("tool %q has parameter without name", toolName))
			continue
		}

		if _, exists := args[paramName]; exists {
			warnings = append(warnings, fmt.Sprintf("duplicate parameter %q in tool %q, overwritten", paramName, toolName))
		}

		if isString {
			args[paramName] = rawValue
			continue
		}

		value, err := parseJSONValue(rawValue)
		if err != nil {
			// Fail soft. Keep the raw value instead of crashing the whole turn.
			args[paramName] = rawValue
			warnings = append(
				warnings,
				fmt.Sprintf("parameter %q in tool %q is marked string=false but is not valid JSON: %v", paramName, toolName, err),
			)
			continue
		}

		args[paramName] = value
	}

	return args, warnings
}

// HasDSMLToolCall checks whether text probably contains DSML tool calls.
func HasDSMLToolCall(content string) bool {
	return strings.Contains(content, "<"+DSML+"tool_calls") ||
		strings.Contains(content, "<"+DSML+"invoke")
}

// IsToolCallMessage checks both normalized ToolCalls and raw DSML content.
func IsToolCallMessage(msg AssistantMessage) bool {
	return len(msg.ToolCalls) > 0 || HasDSMLToolCall(msg.Content)
}

// BuildDeepSeekToolResultContent wraps tool result JSON as DeepSeek expects
// when your backend manually feeds tool results back as a user message.
//
// Multiple tool results should be appended in the same order as the assistant
// tool calls that produced them.
func BuildDeepSeekToolResultContent(result any) string {
	b, err := json.Marshal(result)
	if err != nil {
		b, _ = json.Marshal(map[string]any{
			"error": err.Error(),
		})
	}

	return "<tool_result>" + string(b) + "</tool_result>"
}

// BuildDeepSeekToolResultMessage returns a simple user message map containing
// a DeepSeek-style tool result block.
func BuildDeepSeekToolResultMessage(result any) map[string]any {
	return map[string]any{
		"role":    "user",
		"content": BuildDeepSeekToolResultContent(result),
	}
}

func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}

	attrRe := regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_\-]*)="([^"]*)"`)
	matches := attrRe.FindAllStringSubmatch(s, -1)

	for _, m := range matches {
		attrs[m[1]] = m[2]
	}

	return attrs
}

func parseJSONValue(s string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(s)))
	decoder.UseNumber()

	var v any
	if err := decoder.Decode(&v); err != nil {
		return nil, err
	}

	// Reject trailing non-whitespace data.
	if decoder.More() {
		return nil, fmt.Errorf("invalid JSON: trailing data")
	}

	return v, nil
}

func stripCodeFence(s string) string {
	s = strings.TrimSpace(s)

	re := regexp.MustCompile("(?s)^```(?:xml|html|text|json)?\\s*(.*?)\\s*```$")
	m := re.FindStringSubmatch(s)
	if len(m) == 2 {
		return strings.TrimSpace(m[1])
	}

	return s
}

func extractThinking(s string) (reasoning string, body string) {
	fullThinkRe := regexp.MustCompile(`(?s)<think>(.*?)</think>`)
	if m := fullThinkRe.FindStringSubmatch(s); len(m) == 2 {
		reasoning = m[1]
		body = fullThinkRe.ReplaceAllString(s, "")
		return strings.TrimSpace(reasoning), strings.TrimSpace(body)
	}

	// Compatible with: reasoning...</think>answer
	if idx := strings.Index(s, "</think>"); idx >= 0 {
		reasoning = s[:idx]
		body = s[idx+len("</think>"):]
		return strings.TrimSpace(reasoning), strings.TrimSpace(body)
	}

	return "", strings.TrimSpace(s)
}

func removeDSMLToolCallBlocks(s string) string {
	toolBlockRe := regexp.MustCompile(
		`(?s)<` + regexp.QuoteMeta(DSML) + `tool_calls\s*>.*?</` + regexp.QuoteMeta(DSML) + `tool_calls\s*>`,
	)
	return toolBlockRe.ReplaceAllString(s, "")
}

func stableCallID(name string, index int, argBytes []byte) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fmt.Sprintf("%s:%d:", name, index)))
	_, _ = h.Write(argBytes)
	return fmt.Sprintf("call_%x", h.Sum32())
}

package main

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ── Tool call markers for text-based detection ──

const toolCallBegin = "<tool_call>"
const toolCallEnd = "</tool_call>"

// ParsedToolCall is the intermediate format parsed from model text output.
type ParsedToolCall struct {
	Name      string `json:"name"`
	Arguments any    `json:"arguments"`
}

// ── Tool system prompt builder ──

func BuildToolSystemPrompt(tools []ToolDefinition, toolChoice any) string {
	if len(tools) == 0 {
		return ""
	}

	var b strings.Builder
	b.WriteString("You have access to the following functions. Use them ONLY when they are the appropriate way to answer the user's request.\n\n")

	for i, tool := range tools {
		if tool.Type != "function" || tool.Function.Name == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "Function %q:\n", tool.Function.Name)
		if tool.Function.Description != "" {
			fmt.Fprintf(&b, "  Description: %s\n", tool.Function.Description)
		}
		if len(tool.Function.Parameters) > 0 {
			b.WriteString("  Parameters (JSON Schema): ")
			var params any
			if err := json.Unmarshal(tool.Function.Parameters, &params); err == nil {
				pretty, _ := json.MarshalIndent(params, "    ", "  ")
				b.Write(pretty)
			} else {
				b.Write(tool.Function.Parameters)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\n## How to call a function\n")
	b.WriteString("If you decide to call a function, respond with EXACTLY this format and nothing else:\n")
	b.WriteString(toolCallBegin)
	b.WriteString(`{"name":"function_name","arguments":{"param1":"value1"}}`)
	b.WriteString(toolCallEnd)
	b.WriteString("\n")
	b.WriteString("Do NOT wrap in markdown code blocks. Do NOT add any text before or after.\n")
	b.WriteString("You may call multiple functions one after another, each in its own ")
	b.WriteString(toolCallBegin + "..." + toolCallEnd)
	b.WriteString(" block.\n")
	b.WriteString("If you do NOT need to call a function, respond with plain text as normal.\n")

	switch tc := toolChoice.(type) {
	case string:
		if tc == "none" {
			b.WriteString("\nIMPORTANT: Do NOT call any functions this turn. Respond with plain text only.\n")
		}
	case map[string]any:
		if tc["type"] == "function" {
			if fn, ok := tc["function"].(map[string]any); ok {
				if name, ok := fn["name"].(string); ok {
					fmt.Fprintf(&b, "\nIMPORTANT: You MUST call the function %q this turn.\n", name)
				}
			}
		}
	}

	return b.String()
}

// ── Tool call response parser ──

func FindToolCalls(text string) []ParsedToolCall {
	var calls []ParsedToolCall
	remaining := text

	for {
		start := strings.Index(remaining, toolCallBegin)
		if start < 0 {
			break
		}
		body := remaining[start+len(toolCallBegin):]
		end := strings.Index(body, toolCallEnd)
		if end < 0 {
			break
		}

		jsonStr := strings.TrimSpace(body[:end])
		if jsonStr == "" {
			remaining = body[end+len(toolCallEnd):]
			continue
		}

		var call ParsedToolCall
		if err := json.Unmarshal([]byte(jsonStr), &call); err == nil && call.Name != "" {
			calls = append(calls, call)
		}

		remaining = body[end+len(toolCallEnd):]
	}

	return calls
}

func StripToolCallMarkers(text string) string {
	s := text
	for {
		start := strings.Index(s, toolCallBegin)
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], toolCallEnd)
		if end < 0 {
			break
		}
		s = s[:start] + s[start+end+len(toolCallEnd):]
	}
	return strings.TrimSpace(s)
}

func HasToolCalls(text string) bool {
	return strings.Contains(text, toolCallBegin)
}

// ── Format conversion ──

func convertToolCalls(parsed []ParsedToolCall) []ToolCall {
	calls := make([]ToolCall, len(parsed))
	for i, p := range parsed {
		argsBytes, err := json.Marshal(p.Arguments)
		if err != nil {
			argsBytes = []byte("{}")
		}
		calls[i] = ToolCall{
			ID:   generateToolCallID(),
			Type: "function",
			Function: ToolCallFunction{
				Name:      p.Name,
				Arguments: string(argsBytes),
			},
		}
	}
	return calls
}

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseToolCalls(t *testing.T) {
	text := "Sure, let me check.\n```tool_call\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Tokyo\"}}\n```\nDone."
	clean, calls := parseToolCalls(text)
	if !strings.Contains(clean, "Sure, let me check.") {
		t.Fatalf("clean text lost content: %q", clean)
	}
	if strings.Contains(clean, "```") {
		t.Fatalf("tool_call block should be removed from clean text: %q", clean)
	}
	if len(calls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(calls))
	}
	fn := calls[0]["function"].(map[string]any)
	if fn["name"] != "get_weather" {
		t.Fatalf("unexpected name: %v", fn["name"])
	}
	if calls[0]["id"] == "" || calls[0]["type"] != "function" {
		t.Fatalf("bad call envelope: %v", calls[0])
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(fn["arguments"].(string)), &args); err != nil {
		t.Fatal(err)
	}
	if args["city"] != "Tokyo" {
		t.Fatalf("unexpected args: %v", args)
	}
}

func TestParseToolCallsInvalidJSON(t *testing.T) {
	text := "```tool_call\n{not json}\n```\nrest"
	clean, calls := parseToolCalls(text)
	if len(calls) != 0 {
		t.Fatalf("invalid JSON should be skipped: %v", calls)
	}
	if strings.TrimSpace(clean) != "rest" {
		t.Fatalf("unexpected clean text: %q", clean)
	}
}

func TestBuildToolChoiceInstruction(t *testing.T) {
	if !strings.Contains(buildToolChoiceInstruction("none"), "Do NOT call any tools") {
		t.Fatal("none instruction missing")
	}
	if !strings.Contains(buildToolChoiceInstruction("required"), "MUST call at least one tool") {
		t.Fatal("required instruction missing")
	}
	instr := buildToolChoiceInstruction(map[string]any{"type": "function", "function": map[string]any{"name": "get_weather"}})
	if !strings.Contains(instr, "get_weather") {
		t.Fatalf("expected named tool instruction: %q", instr)
	}
	if buildToolChoiceInstruction("auto") != "" {
		t.Fatal("auto should produce no instruction")
	}
}

func TestMessagesToPromptWithTools(t *testing.T) {
	messages := []map[string]any{
		{"role": "system", "content": "Be concise."},
		{"role": "user", "content": "Weather in Tokyo?"},
	}
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{"name": "get_weather", "description": "Get weather", "parameters": map[string]any{"type": "object"}}},
	}
	prompt, images := messagesToPrompt(messages, tools, "auto")
	if !strings.Contains(prompt, "# Tool Use") {
		t.Fatalf("expected tool use section: %q", prompt)
	}
	if !strings.Contains(prompt, "get_weather") {
		t.Fatalf("expected tool name: %q", prompt)
	}
	if !strings.Contains(prompt, "[System instruction]: Be concise.") {
		t.Fatalf("expected system instruction: %q", prompt)
	}
	if !strings.Contains(prompt, "Weather in Tokyo?") {
		t.Fatalf("expected user content: %q", prompt)
	}
	if len(images) != 0 {
		t.Fatalf("no images expected")
	}
}

func TestTruncateDesc(t *testing.T) {
	old := Config.MaxToolDesc
	defer func() { Config.MaxToolDesc = old }()
	Config.MaxToolDesc = 10

	if got := truncateDesc("short"); got != "short" {
		t.Fatalf("short desc should pass through, got %q", got)
	}
	got := truncateDesc("a long description that exceeds the limit")
	if len([]rune(got)) != 10 { // max runes, ellipsis included
		t.Fatalf("expected 10 runes, got %d: %q", len([]rune(got)), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("expected ellipsis suffix: %q", got)
	}

	Config.MaxToolDesc = 0
	long := "a very very long description"
	if got := truncateDesc(long); got != long {
		t.Fatalf("max=0 should be unlimited, got %q", got)
	}
}

// TestMessagesToPromptTruncatesToolDescs verifies long tool descriptions are
// truncated in the built prompt (keeps total prompt under Gemini Web limits).
func TestMessagesToPromptTruncatesToolDescs(t *testing.T) {
	old := Config.MaxToolDesc
	defer func() { Config.MaxToolDesc = old }()
	Config.MaxToolDesc = 20

	messages := []map[string]any{{"role": "user", "content": "hi"}}
	tools := []map[string]any{
		{"type": "function", "function": map[string]any{
			"name":        "f",
			"description": strings.Repeat("x", 500),
			"parameters":  map[string]any{"type": "object"},
		}},
	}
	prompt, _ := messagesToPrompt(messages, tools, "auto")
	if strings.Contains(prompt, strings.Repeat("x", 500)) {
		t.Fatalf("full description leaked into prompt")
	}
	if !strings.Contains(prompt, strings.Repeat("x", 19)+"…") {
		t.Fatalf("expected truncated description with ellipsis in prompt")
	}
}

func TestMessagesToPromptToolChoiceNone(t *testing.T) {
	messages := []map[string]any{{"role": "user", "content": "hi"}}
	tools := []map[string]any{{"type": "function", "function": map[string]any{"name": "f"}}}
	prompt, _ := messagesToPrompt(messages, tools, "none")
	if strings.Contains(prompt, "# Tool Use") {
		t.Fatalf("tools should be omitted when tool_choice is none: %q", prompt)
	}
}

func TestMessagesToPromptImageNote(t *testing.T) {
	messages := []map[string]any{
		{"role": "user", "content": []any{
			map[string]any{"type": "text", "text": "describe this"},
			map[string]any{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/x.png"}},
		}},
	}
	prompt, images := messagesToPrompt(messages, nil, "auto")
	if !strings.Contains(prompt, "describe this") {
		t.Fatalf("missing text: %q", prompt)
	}
	if !strings.Contains(prompt, "Image input not supported") {
		t.Fatalf("missing image note: %q", prompt)
	}
	if len(images) != 0 {
		t.Fatalf("images should be empty for OpenAI messages")
	}
}

func TestParseGoogleFunctionCalls(t *testing.T) {
	text := "Let me look that up.\n```function_call\n{\"name\": \"search\", \"args\": {\"q\": \"x\"}}\n```"
	clean, calls := parseGoogleFunctionCalls(text)
	if len(calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(calls))
	}
	if calls[0]["name"] != "search" {
		t.Fatalf("unexpected name: %v", calls[0]["name"])
	}
	if strings.Contains(clean, "function_call") {
		t.Fatalf("block should be stripped: %q", clean)
	}
}

func TestParseGoogleFunctionCallsRawJSON(t *testing.T) {
	clean, calls := parseGoogleFunctionCalls(`{"name": "search", "args": {"q": "y"}}`)
	if len(calls) != 1 || calls[0]["name"] != "search" {
		t.Fatalf("unexpected: %v", calls)
	}
	if clean != "" {
		t.Fatalf("clean should be empty, got %q", clean)
	}
}

func TestGoogleContentsToPrompt(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "What is 2+2?"}}},
			map[string]any{"role": "model", "parts": []any{map[string]any{"functionCall": map[string]any{"name": "add", "args": map[string]any{"a": 2, "b": 2}}}}},
			map[string]any{"role": "user", "parts": []any{map[string]any{"functionResponse": map[string]any{"name": "add", "response": map[string]any{"result": 4}}}}},
		},
	}
	prompt, images := googleContentsToPrompt(req)
	if !strings.Contains(prompt, "[Assistant]:") || !strings.Contains(prompt, "```function_call") {
		t.Fatalf("unexpected prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "[Tool result for add]") {
		t.Fatalf("expected tool result: %q", prompt)
	}
	if !strings.Contains(prompt, "What is 2+2?") {
		t.Fatalf("expected user text: %q", prompt)
	}
	if len(images) != 0 {
		t.Fatalf("no images expected")
	}
}

func TestGoogleContentsToPromptSystemAndTools(t *testing.T) {
	req := map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": "Be brief"}}},
		"tools": []any{
			map[string]any{"functionDeclarations": []any{
				map[string]any{"name": "add", "description": "Add numbers", "parameters": map[string]any{"type": "object"}},
			}},
		},
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{map[string]any{"text": "Compute 2+2"}}},
		},
	}
	prompt, _ := googleContentsToPrompt(req)
	if !strings.Contains(prompt, "# Tool Use") || !strings.Contains(prompt, "add") {
		t.Fatalf("expected tools section: %q", prompt)
	}
	if !strings.Contains(prompt, "Be brief") {
		t.Fatalf("expected system instruction: %q", prompt)
	}
}

func TestGoogleContentsToPromptInlineImage(t *testing.T) {
	req := map[string]any{
		"contents": []any{
			map[string]any{"role": "user", "parts": []any{
				map[string]any{"inlineData": map[string]any{"mimeType": "image/png", "data": "aGVsbG8="}},
			}},
		},
	}
	_, images := googleContentsToPrompt(req)
	if len(images) != 1 || string(images[0].Data) != "hello" || images[0].Mime != "image/png" {
		t.Fatalf("unexpected images: %+v", images)
	}
}

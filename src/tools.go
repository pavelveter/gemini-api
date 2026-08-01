package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var (
	// (?s) = DOTALL so the capture group can span newlines, like re.DOTALL.
	reToolCall  = regexp.MustCompile("(?s)```tool_call\\s*\\n(.*?)\\n```")
	reFuncCall  = regexp.MustCompile("(?s)```function_call\\s*\\n(.*?)\\n```")
	reFuncCall2 = regexp.MustCompile("(?:^|\\n)function_call\\s*\\n(\\{[^`]*?\\})")
)

// imageInput is an image to upload: either raw bytes or a URL to fetch.
type imageInput struct {
	Data []byte
	URL  string
	Mime string
}

// truncateDesc caps a tool description at Config.MaxToolDesc runes (a
// trailing ellipsis is included in the budget) so that clients sending many
// large tools (e.g. hermes with 40+) keep the total prompt under Gemini
// Web's anonymous size limit (~120KB), where upstream otherwise rejects the
// request with BardErrorInfo [1152]. 0 = unlimited.
func truncateDesc(desc string) string {
	max := Config.MaxToolDesc
	if max <= 0 || len(desc) <= max {
		return desc
	}
	runes := []rune(desc)
	if len(runes) <= max {
		return desc
	}
	if max == 1 {
		return "…"
	}
	return string(runes[:max-1]) + "…"
}

// buildToolChoiceInstruction builds the tool_choice constraint instruction.
func buildToolChoiceInstruction(toolChoice any) string {
	switch tc := toolChoice.(type) {
	case string:
		switch tc {
		case "none":
			return "\n\nIMPORTANT: Do NOT call any tools. Respond with text only."
		case "required":
			return "\n\nIMPORTANT: You MUST call at least one tool. Do not respond with text only."
		}
	case map[string]any:
		if fn, ok := tc["function"].(map[string]any); ok {
			if name := getStr(fn, "name"); name != "" {
				return fmt.Sprintf("\n\nIMPORTANT: You MUST call the tool %q. Do not call other tools.", name)
			}
		}
	}
	return ""
}

// messagesToPrompt converts OpenAI messages into a Gemini prompt string.
func messagesToPrompt(messages []map[string]any, tools []map[string]any, toolChoice any) (string, []imageInput) {
	var parts []string
	var images []imageInput

	if len(tools) > 0 && toolChoice != "none" {
		var toolDefs []map[string]any
		for _, tool := range tools {
			fn := tool
			if getStr(tool, "type") == "function" {
				if f, ok := tool["function"].(map[string]any); ok {
					fn = f
				}
			}
			name := getStr(fn, "name")
			if name == "" {
				name = getStr(tool, "name")
			}
			desc := getStr(fn, "description")
			if desc == "" {
				desc = getStr(tool, "description")
			}
			params := fn["parameters"]
			if params == nil {
				params = tool["parameters"]
			}
			if params == nil {
				params = map[string]any{}
			}
			toolDefs = append(toolDefs, map[string]any{
				"name":        name,
				"description": truncateDesc(desc),
				"parameters":  params,
			})
		}
		if len(toolDefs) > 0 {
			constraint := buildToolChoiceInstruction(toolChoice)
			spec, _ := json.MarshalIndent(toolDefs, "", "  ")
			parts = append(parts,
				"# Tool Use\n\n"+
					"You can call the following tools. Call format:\n"+
					"```tool_call\n{\"name\": \"func_name\", \"arguments\": {...}}\n```\n"+
					"When calling tools, output ONLY the tool_call block(s).\n\n"+
					"Available tools:\n"+string(spec)+constraint)
		}
	}

	for _, msg := range messages {
		role := getStr(msg, "role")
		if role == "" {
			role = "user"
		}
		content := msg["content"]

		if list, ok := content.([]any); ok {
			var textParts []string
			for _, c := range list {
				cm, ok := c.(map[string]any)
				if !ok {
					continue
				}
				switch getStr(cm, "type") {
				case "text", "input_text":
					textParts = append(textParts, getStr(cm, "text"))
				case "image_url", "image":
					textParts = append(textParts, "[Note: Image input not supported in this API. Please describe the image in text.]")
				}
			}
			content = strings.Join(textParts, " ")
		}
		contentStr, _ := content.(string)

		switch role {
		case "system":
			parts = append(parts, "[System instruction]: "+contentStr)
		case "assistant":
			if tcRaw, ok := msg["tool_calls"]; ok {
				var tcStrs []string
				if tcList, ok := tcRaw.([]any); ok {
					for _, tc := range tcList {
						tcm, _ := tc.(map[string]any)
						fn, _ := tcm["function"].(map[string]any)
						args := fn["arguments"]
						argsStr := ""
						if s, ok := args.(string); ok {
							argsStr = s
						} else if args != nil {
							if b, err := json.Marshal(args); err == nil {
								argsStr = string(b)
							}
						}
						tcStrs = append(tcStrs, fmt.Sprintf("```tool_call\n{\"name\": %q, \"arguments\": %s}\n```",
							getStr(fn, "name"), argsStr))
					}
				}
				parts = append(parts, "[Assistant]: "+contentStr+"\n"+strings.Join(tcStrs, "\n"))
			} else {
				parts = append(parts, "[Assistant]: "+contentStr)
			}
		case "tool":
			parts = append(parts, "[Tool result for "+getStr(msg, "name")+"]: "+contentStr)
		default:
			parts = append(parts, contentStr)
		}
	}

	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "\n\n"), images
}

// parseToolCalls extracts ```tool_call blocks from the model output.
func parseToolCalls(text string) (string, []map[string]any) {
	var toolCalls []map[string]any
	var cleanParts []string
	lastEnd := 0
	for _, m := range reToolCall.FindAllStringSubmatchIndex(text, -1) {
		cleanParts = append(cleanParts, text[lastEnd:m[0]])
		lastEnd = m[1]
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(text[m[2]:m[3]])), &data); err != nil {
			continue
		}
		name, ok := data["name"].(string)
		if !ok {
			continue
		}
		argsJSON, err := json.Marshal(data["arguments"])
		if err != nil {
			continue
		}
		toolCalls = append(toolCalls, map[string]any{
			"id":   "call_" + randomHex(8),
			"type": "function",
			"function": map[string]any{
				"name":      name,
				"arguments": string(argsJSON),
			},
		})
	}
	cleanParts = append(cleanParts, text[lastEnd:])
	return strings.TrimSpace(strings.Join(cleanParts, "")), toolCalls
}

// ─── Google native API helpers ───────────────────────────────────────────────

// buildToolPrompt builds the natural tool-use prompt for Gemini Web.
func buildToolPrompt(toolDefs []map[string]any) string {
	spec, _ := json.MarshalIndent(toolDefs, "", "  ")
	return "# Tool Use\n\n" +
		"You can call the following tools to help accomplish tasks. " +
		"These tools connect to the user's local environment and will execute when called.\n\n" +
		"Call format (use this exact format):\n" +
		"```function_call\n" +
		"{\"name\": \"<tool_name>\", \"args\": {<arguments>}}\n" +
		"```\n\n" +
		"When calling tools:\n" +
		"- Output ONLY the function_call block(s), nothing else\n" +
		"- You may call multiple tools with multiple blocks\n" +
		"- After receiving a [Tool result for ...], use that data to answer the user\n\n" +
		"Available tools:\n" + string(spec)
}

// googleToolChoiceInstruction extracts the tool_choice constraint from the
// Google API toolConfig.
func googleToolChoiceInstruction(req map[string]any) string {
	toolConfig, _ := req["toolConfig"].(map[string]any)
	fcConfig, _ := toolConfig["functionCallingConfig"].(map[string]any)
	mode := getStr(fcConfig, "mode")
	if mode == "" {
		mode = "AUTO"
	}
	allowed, _ := fcConfig["allowedFunctionNames"].([]any)
	switch mode {
	case "NONE":
		return "\n\nIMPORTANT: Do NOT call any tools. Respond with text only."
	case "ANY":
		if len(allowed) > 0 {
			var names []string
			for _, n := range allowed {
				if s, ok := n.(string); ok {
					names = append(names, fmt.Sprintf("%q", s))
				}
			}
			return fmt.Sprintf("\n\nIMPORTANT: You MUST call one of these tools: %s. Do not respond with text only.", strings.Join(names, ", "))
		}
		return "\n\nIMPORTANT: You MUST call at least one tool. Do not respond with text only."
	}
	return ""
}

// googleContentsToPrompt converts Google API contents/tools/systemInstruction
// into a Gemini prompt string.
func googleContentsToPrompt(req map[string]any) (string, []imageInput) {
	var parts []string
	var images []imageInput

	toolConfig, _ := req["toolConfig"].(map[string]any)
	fcConfig, _ := toolConfig["functionCallingConfig"].(map[string]any)
	fcMode := getStr(fcConfig, "mode")
	if fcMode == "" {
		fcMode = "AUTO"
	}

	var toolDefs []map[string]any
	if tools, _ := req["tools"].([]any); len(tools) > 0 && fcMode != "NONE" {
		for _, toolGroup := range tools {
			tg, _ := toolGroup.(map[string]any)
			fns, _ := tg["functionDeclarations"].([]any)
			for _, fnRaw := range fns {
				fn, _ := fnRaw.(map[string]any)
				td := map[string]any{
					"name":        getStr(fn, "name"),
					"description": truncateDesc(getStr(fn, "description")),
				}
				params := fn["parameters"]
				if params == nil {
					params = fn["parametersJsonSchema"]
				}
				if params != nil {
					td["parameters"] = params
				}
				toolDefs = append(toolDefs, td)
			}
		}
	}

	sysInst, _ := req["systemInstruction"].(map[string]any)
	sysParts, _ := sysInst["parts"].([]any)
	var sysTexts []string
	for _, p := range sysParts {
		pm, _ := p.(map[string]any)
		if t := getStr(pm, "text"); t != "" {
			sysTexts = append(sysTexts, t)
		}
	}
	sysText := strings.Join(sysTexts, " ")
	if sysText != "" {
		if len(toolDefs) > 0 {
			parts = append(parts, sysText+"\n\n"+buildToolPrompt(toolDefs)+googleToolChoiceInstruction(req))
		} else {
			parts = append(parts, sysText)
		}
	} else if len(toolDefs) > 0 {
		parts = append(parts, buildToolPrompt(toolDefs)+googleToolChoiceInstruction(req))
	}

	contents, _ := req["contents"].([]any)
	for _, content := range contents {
		cm, _ := content.(map[string]any)
		role := getStr(cm, "role")
		if role == "" {
			role = "user"
		}
		var msgParts []string
		partsList, _ := cm["parts"].([]any)
		for _, p := range partsList {
			pm, _ := p.(map[string]any)
			if t := getStr(pm, "text"); t != "" {
				msgParts = append(msgParts, t)
			} else if id, ok := pm["inlineData"].(map[string]any); ok {
				mime := getStr(id, "mimeType")
				if mime == "" {
					mime = "image/png"
				}
				data, err := base64.StdEncoding.DecodeString(getStr(id, "data"))
				if err != nil {
					continue
				}
				images = append(images, imageInput{Data: data, Mime: mime})
			} else if fc, ok := pm["functionCall"].(map[string]any); ok {
				fcJSON, _ := json.Marshal(map[string]any{
					"name": getStr(fc, "name"),
					"args": fc["args"],
				})
				msgParts = append(msgParts, "```function_call\n"+string(fcJSON)+"\n```")
			} else if fr, ok := pm["functionResponse"].(map[string]any); ok {
				respJSON, _ := json.Marshal(fr["response"])
				msgParts = append(msgParts, "[Tool result for "+getStr(fr, "name")+"]: "+string(respJSON))
			}
		}
		text := strings.Join(msgParts, "\n")
		if role == "model" {
			parts = append(parts, "[Assistant]: "+text)
		} else {
			parts = append(parts, text)
		}
	}

	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "\n\n"), images
}

// parseGoogleFunctionCalls extracts function_call blocks from the model
// output, handling three formats:
//  1. ```function_call\n{...}\n```
//  2. function_call\n{...} (without backticks)
//  3. Raw JSON with "name" + "args"/"arguments" keys
func parseGoogleFunctionCalls(text string) (string, []map[string]any) {
	var calls []map[string]any
	clean := text
	for _, re := range []*regexp.Regexp{reFuncCall, reFuncCall2} {
		for _, m := range re.FindAllStringSubmatch(clean, -1) {
			var data map[string]any
			if err := json.Unmarshal([]byte(strings.TrimSpace(m[1])), &data); err != nil {
				continue
			}
			if _, ok := data["name"]; !ok {
				continue
			}
			args := data["args"]
			if args == nil {
				args = data["arguments"]
			}
			calls = append(calls, map[string]any{
				"name": data["name"],
				"args": args,
			})
		}
		clean = strings.TrimSpace(re.ReplaceAllString(clean, ""))
	}
	if len(calls) == 0 && strings.HasPrefix(strings.TrimSpace(clean), "{") {
		var data map[string]any
		if err := json.Unmarshal([]byte(strings.TrimSpace(clean)), &data); err == nil {
			_, hasName := data["name"]
			_, hasArgs := data["args"]
			_, hasArguments := data["arguments"]
			if hasName && (hasArgs || hasArguments) {
				args := data["args"]
				if args == nil {
					args = data["arguments"]
				}
				calls = append(calls, map[string]any{"name": data["name"], "args": args})
				clean = ""
			}
		}
	}
	return clean, calls
}

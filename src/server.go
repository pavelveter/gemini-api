package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Server implements the OpenAI/Google-compatible HTTP API.
type Server struct{}

func errorResp(message string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message}}
}

func writeJSON(w http.ResponseWriter, data any, status int) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_, _ = w.Write(marshalJSON(data))
}

func startSSE(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(http.StatusOK)
}

func writeSSE(w http.ResponseWriter, s string) {
	_, _ = w.Write([]byte(s))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// authorized checks the request against the configured API keys. When no
// keys are configured, authentication is disabled.
func authorized(r *http.Request) bool {
	keys := Config.APIKeys
	if len(keys) == 0 {
		return true
	}
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") && containsKey(keys, strings.TrimPrefix(auth, "Bearer ")) {
		return true
	}
	for _, h := range []string{"x-api-key", "x-goog-api-key"} {
		if v := r.Header.Get(h); v != "" && containsKey(keys, v) {
			return true
		}
	}
	if v := r.URL.Query().Get("key"); v != "" && containsKey(keys, v) {
		return true
	}
	return false
}

// containsKey reports whether s matches any configured key using
// constant-time comparison, avoiding timing side channels when keys are
// compared across the internet.
func containsKey(keys []string, s string) bool {
	if s == "" {
		return false
	}
	for _, k := range keys {
		if subtle.ConstantTimeCompare([]byte(k), []byte(s)) == 1 {
			return true
		}
	}
	return false
}

func parseBody(body []byte) map[string]any {
	var req map[string]any
	if err := json.Unmarshal(body, &req); err != nil {
		return nil
	}
	return req
}

func readBody(r *http.Request) []byte {
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	return data
}

func usage(prompt, text string) map[string]any {
	p := charLen(prompt) / 4
	c := charLen(text) / 4
	return map[string]any{"prompt_tokens": p, "completion_tokens": c, "total_tokens": p + c}
}

var reGoogleModel = regexp.MustCompile(`^/v1beta/models/([^:?]+)`)

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Access log, mirroring the Python handler's log_message override.
	clientIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		clientIP = host
	}
	log(fmt.Sprintf("%s %s %s", clientIP, r.Method, r.URL.Path))

	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1") && !authorized(r) {
		writeJSON(w, errorResp("invalid api key"), http.StatusUnauthorized)
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r)
	case http.MethodPost:
		s.handlePost(w, r)
	default:
		writeJSON(w, errorResp("not found"), http.StatusNotFound)
	}
}

func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	switch {
	case path == "/v1/models":
		var data []map[string]any
		for _, n := range modelNames() {
			data = append(data, map[string]any{
				"id": n, "object": "model", "created": 1700000000,
				"owned_by": "google", "description": MODELS[n].Desc,
			})
		}
		writeJSON(w, map[string]any{"object": "list", "data": data}, http.StatusOK)
	case strings.HasPrefix(path, "/v1beta/models"):
		var models []map[string]any
		for _, n := range modelNames() {
			models = append(models, map[string]any{
				"name": "models/" + n, "displayName": n, "description": MODELS[n].Desc,
				"supportedGenerationMethods": []string{"generateContent", "streamGenerateContent"},
			})
		}
		writeJSON(w, map[string]any{"models": models}, http.StatusOK)
	case path == "/":
		writeJSON(w, map[string]any{"status": "ok", "version": Version, "models": modelNames()}, http.StatusOK)
	default:
		writeJSON(w, errorResp("not found"), http.StatusNotFound)
	}
}

func (s *Server) handlePost(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	body := readBody(r)
	switch {
	case path == "/v1/chat/completions":
		s.handleChat(w, r, body)
	case path == "/v1/responses":
		s.handleResponses(w, r, body)
	case path == "/v1/embeddings":
		s.handleEmbeddings(w, r, body)
	case strings.Contains(path, ":streamGenerateContent"):
		s.handleGoogleGenerate(w, r, body, true)
	case strings.Contains(path, ":generateContent"):
		s.handleGoogleGenerate(w, r, body, false)
	default:
		writeJSON(w, errorResp("not found"), http.StatusNotFound)
	}
}

// ─── /v1/chat/completions ────────────────────────────────────────────────────

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, body []byte) {
	req := parseBody(body)
	if req == nil {
		writeJSON(w, errorResp("invalid JSON"), http.StatusBadRequest)
		return
	}
	modelName := getStr(req, "model")
	if modelName == "" {
		modelName = Config.DefaultModel
	}
	rm, err := resolveModel(modelName, Config.DefaultModel)
	if err != nil {
		writeJSON(w, errorResp(err.Error()), http.StatusBadRequest)
		return
	}
	tools := anyList(req["tools"])
	toolChoice := req["tool_choice"]
	if toolChoice == nil {
		toolChoice = "auto"
	}
	prompt, images := messagesToPrompt(anyList(req["messages"]), tools, toolChoice)
	if strings.TrimSpace(prompt) == "" {
		writeJSON(w, errorResp("empty prompt"), http.StatusBadRequest)
		return
	}
	stream := getBool(req, "stream")
	cid := "chatcmpl-" + randomHex(12)
	fileRefs := uploadImages(images)

	if stream && (len(tools) == 0 || toolChoice == "none") {
		startSSE(w)
		err := generateStream(prompt, rm.ModeID, rm.ThinkMode, fileRefs, rm.Extra, func(delta string) {
			chunk := map[string]any{
				"id": cid, "object": "chat.completion.chunk", "created": time.Now().Unix(),
				"model":   rm.Name,
				"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": delta}, "finish_reason": nil}},
			}
			writeSSE(w, "data: "+string(marshalJSON(chunk))+"\n\n")
		})
		if err != nil {
			log("Chat stream error: " + err.Error())
			return
		}
		end := map[string]any{
			"id": cid, "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"model":   rm.Name,
			"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"}},
		}
		writeSSE(w, "data: "+string(marshalJSON(end))+"\n\n")
		writeSSE(w, "data: [DONE]\n\n")
		return
	}

	text, err := generate(prompt, rm.ModeID, rm.ThinkMode, fileRefs, rm.Extra)
	if err != nil {
		writeJSON(w, errorResp("upstream error: "+err.Error()), http.StatusBadGateway)
		return
	}

	var toolCalls []map[string]any
	if len(tools) > 0 && text != "" && toolChoice != "none" {
		text, toolCalls = parseToolCalls(text)
	}
	msg := map[string]any{"role": "assistant", "content": nil}
	if text != "" {
		msg["content"] = text
	}
	if toolCalls != nil {
		msg["tool_calls"] = toolCalls
	}
	finish := "stop"
	if toolCalls != nil {
		finish = "tool_calls"
	}

	if stream {
		startSSE(w)
		chunk := map[string]any{
			"id": cid, "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"model":   rm.Name,
			"choices": []any{map[string]any{"index": 0, "delta": msg, "finish_reason": finish}},
		}
		writeSSE(w, "data: "+string(marshalJSON(chunk))+"\n\n")
		writeSSE(w, "data: [DONE]\n\n")
		return
	}
	writeJSON(w, map[string]any{
		"id": cid, "object": "chat.completion", "created": time.Now().Unix(),
		"model":   rm.Name,
		"choices": []any{map[string]any{"index": 0, "message": msg, "finish_reason": finish}},
		"usage":   usage(prompt, text),
	}, http.StatusOK)
}

// ─── /v1/responses (Codex CLI) ───────────────────────────────────────────────

func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request, body []byte) {
	req := parseBody(body)
	if req == nil {
		writeJSON(w, errorResp("invalid JSON"), http.StatusBadRequest)
		return
	}
	modelName := getStr(req, "model")
	if modelName == "" {
		modelName = Config.DefaultModel
	}
	rm, err := resolveModel(modelName, Config.DefaultModel)
	if err != nil {
		writeJSON(w, errorResp(err.Error()), http.StatusBadRequest)
		return
	}

	tools := anyList(req["tools"])
	var messages []map[string]any
	if instr := getStr(req, "instructions"); instr != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instr})
	}
	switch v := req["input"].(type) {
	case string:
		messages = append(messages, map[string]any{"role": "user", "content": v})
	case []any:
		for _, item := range v {
			im, ok := item.(map[string]any)
			if !ok {
				if str, ok := item.(string); ok {
					messages = append(messages, map[string]any{"role": "user", "content": str})
				}
				continue
			}
			switch {
			case im["type"] == "function_call_output":
				messages = append(messages, map[string]any{
					"role": "tool", "tool_call_id": getStr(im, "call_id"),
					"name": getStr(im, "name"), "content": getStr(im, "output"),
				})
			case im["role"] == "assistant" || (im["type"] == "message" && im["role"] == "assistant"):
				textAcc := ""
				var tcList []map[string]any
				switch cv := im["content"].(type) {
				case []any:
					for _, c := range cv {
						cm, _ := c.(map[string]any)
						switch cm["type"] {
						case "output_text":
							textAcc += getStr(cm, "text")
						case "function_call":
							tcList = append(tcList, cm)
						}
					}
				case string:
					textAcc = cv
				}
				m := map[string]any{"role": "assistant", "content": nil}
				if textAcc != "" {
					m["content"] = textAcc
				}
				if len(tcList) > 0 {
					var tcs []map[string]any
					for i, tc := range tcList {
						callID := getStr(tc, "call_id")
						if callID == "" {
							callID = fmt.Sprintf("call_%d", i)
						}
						tcs = append(tcs, map[string]any{
							"id": callID, "type": "function",
							"function": map[string]any{"name": getStr(tc, "name"), "arguments": getStr(tc, "arguments")},
						})
					}
					m["tool_calls"] = tcs
				}
				messages = append(messages, m)
			default:
				role := getStr(im, "role")
				if role == "" {
					role = "user"
				}
				content := im["content"]
				if list, ok := content.([]any); ok {
					var texts []string
					for _, c := range list {
						cm, _ := c.(map[string]any)
						t := getStr(cm, "type")
						if t == "text" || t == "input_text" {
							texts = append(texts, getStr(cm, "text"))
						}
					}
					content = strings.Join(texts, " ")
				}
				messages = append(messages, map[string]any{"role": role, "content": content})
			}
		}
	}

	if len(tools) > 0 {
		var normalized []map[string]any
		for _, t := range tools {
			if getStr(t, "type") == "function" {
				if _, has := t["function"]; !has {
					normalized = append(normalized, map[string]any{
						"type": "function",
						"function": map[string]any{
							"name": getStr(t, "name"), "description": getStr(t, "description"), "parameters": t["parameters"],
						},
					})
					continue
				}
			}
			normalized = append(normalized, t)
		}
		tools = normalized
	}

	toolChoice := req["tool_choice"]
	if toolChoice == nil {
		toolChoice = "auto"
	}
	prompt, images := messagesToPrompt(messages, tools, toolChoice)
	if strings.TrimSpace(prompt) == "" {
		writeJSON(w, errorResp("empty input"), http.StatusBadRequest)
		return
	}

	text, err := generate(prompt, rm.ModeID, rm.ThinkMode, uploadImages(images), rm.Extra)
	if err != nil {
		writeJSON(w, errorResp("upstream error: "+err.Error()), http.StatusBadGateway)
		return
	}

	var toolCalls []map[string]any
	if len(tools) > 0 && text != "" && toolChoice != "none" {
		text, toolCalls = parseToolCalls(text)
	}

	rid := "resp_" + randomHex(16)
	mid := "msg_" + randomHex(12)
	var output []map[string]any
	if len(toolCalls) > 0 {
		for _, tc := range toolCalls {
			fn, _ := tc["function"].(map[string]any)
			output = append(output, map[string]any{
				"type": "function_call", "id": tc["id"], "call_id": tc["id"],
				"name": getStr(fn, "name"), "arguments": getStr(fn, "arguments"), "status": "completed",
			})
		}
	}
	if text != "" || len(toolCalls) == 0 {
		output = append(output, map[string]any{
			"type": "message", "id": mid, "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}},
		})
	}

	if getBool(req, "stream") {
		startSSE(w)
		evCreated := map[string]any{
			"type": "response.created",
			"response": map[string]any{
				"id": rid, "object": "response", "status": "in_progress", "model": rm.Name, "output": []any{},
			},
		}
		writeSSE(w, "event: response.created\ndata: "+string(marshalJSON(evCreated))+"\n\n")
		for _, item := range output {
			if item["type"] == "function_call" {
				ev := map[string]any{
					"type":    "response.function_call_arguments.done",
					"item_id": item["id"], "call_id": item["call_id"],
					"name": item["name"], "arguments": item["arguments"],
				}
				writeSSE(w, "event: response.function_call_arguments.done\ndata: "+string(marshalJSON(ev))+"\n\n")
			} else if item["type"] == "message" {
				content, _ := item["content"].([]any)
				for ci, cp := range content {
					cpm, _ := cp.(map[string]any)
					ev := map[string]any{
						"type":    "response.output_text.done",
						"item_id": item["id"], "content_index": ci, "text": getStr(cpm, "text"),
					}
					writeSSE(w, "event: response.output_text.done\ndata: "+string(marshalJSON(ev))+"\n\n")
				}
			}
		}
		respObj := map[string]any{
			"id": rid, "object": "response", "status": "completed", "model": rm.Name, "output": output,
			"usage": map[string]any{
				"input_tokens": charLen(prompt) / 4, "output_tokens": charLen(text) / 4,
				"total_tokens": (charLen(prompt) + charLen(text)) / 4,
			},
		}
		writeSSE(w, "event: response.completed\ndata: "+string(marshalJSON(map[string]any{"type": "response.completed", "response": respObj}))+"\n\n")
		return
	}

	writeJSON(w, map[string]any{
		"id": rid, "object": "response", "created_at": time.Now().Unix(), "status": "completed",
		"model": rm.Name, "output": output,
		"usage": map[string]any{
			"input_tokens": charLen(prompt) / 4, "output_tokens": charLen(text) / 4,
			"total_tokens": (charLen(prompt) + charLen(text)) / 4,
		},
	}, http.StatusOK)
}

// ─── /v1beta/models (Google Gemini CLI) ──────────────────────────────────────

func (s *Server) handleGoogleGenerate(w http.ResponseWriter, r *http.Request, body []byte, stream bool) {
	req := parseBody(body)
	if req == nil {
		writeJSON(w, errorResp("invalid JSON"), http.StatusBadRequest)
		return
	}
	modelName := Config.DefaultModel
	if m := reGoogleModel.FindStringSubmatch(r.URL.Path); m != nil {
		modelName = m[1]
	}
	rm, err := resolveModel(modelName, Config.DefaultModel)
	if err != nil {
		writeJSON(w, errorResp(err.Error()), http.StatusBadRequest)
		return
	}
	toolConfig, _ := req["toolConfig"].(map[string]any)
	fcConfig, _ := toolConfig["functionCallingConfig"].(map[string]any)
	fcMode := getStr(fcConfig, "mode")
	if fcMode == "" {
		fcMode = "AUTO"
	}
	hasTools := false
	if tools, ok := req["tools"].([]any); ok && len(tools) > 0 && fcMode != "NONE" {
		hasTools = true
	}
	prompt, images := googleContentsToPrompt(req)
	if strings.TrimSpace(prompt) == "" {
		writeJSON(w, errorResp("empty content"), http.StatusBadRequest)
		return
	}
	fileRefs := uploadImages(images)
	log(fmt.Sprintf("Google API: model=%s stream=%v tools=%v prompt_len=%d", rm.Name, stream, hasTools, charLen(prompt)))

	if stream && !hasTools {
		startSSE(w)
		fullText := ""
		err := generateStream(prompt, rm.ModeID, rm.ThinkMode, fileRefs, rm.Extra, func(delta string) {
			if delta == "" {
				return
			}
			fullText += delta
			chunk := map[string]any{
				"candidates": []any{map[string]any{
					"content": map[string]any{"parts": []any{map[string]any{"text": delta}}, "role": "model"},
					"index":   0,
				}},
				"modelVersion": rm.Name,
			}
			writeSSE(w, "data: "+string(marshalJSON(chunk))+"\n\n")
		})
		if err != nil {
			log("Google stream error: " + err.Error())
			return
		}
		final := map[string]any{
			"candidates": []any{map[string]any{"finishReason": "STOP", "index": 0}},
			"usageMetadata": map[string]any{
				"promptTokenCount":     charLen(prompt) / 4,
				"candidatesTokenCount": charLen(fullText) / 4,
				"totalTokenCount":      (charLen(prompt) + charLen(fullText)) / 4,
			},
			"modelVersion": rm.Name,
		}
		writeSSE(w, "data: "+string(marshalJSON(final))+"\n\n")
		return
	}

	text, err := generate(prompt, rm.ModeID, rm.ThinkMode, fileRefs, rm.Extra)
	if err != nil {
		writeJSON(w, errorResp("upstream error: "+err.Error()), http.StatusBadGateway)
		return
	}
	if text == "" {
		log("Warning: empty response from Gemini")
	}

	var responseParts []map[string]any
	if hasTools && text != "" {
		clean, fcs := parseGoogleFunctionCalls(text)
		if len(fcs) > 0 {
			if clean != "" {
				responseParts = append(responseParts, map[string]any{"text": clean})
			}
			for _, fc := range fcs {
				responseParts = append(responseParts, map[string]any{"functionCall": fc})
			}
		} else {
			responseParts = append(responseParts, map[string]any{"text": text})
		}
	} else {
		responseParts = append(responseParts, map[string]any{"text": text})
	}

	candidate := map[string]any{
		"content":      map[string]any{"parts": responseParts, "role": "model"},
		"finishReason": "STOP",
		"index":        0,
	}
	responseObj := map[string]any{
		"candidates": []any{candidate},
		"usageMetadata": map[string]any{
			"promptTokenCount":     charLen(prompt) / 4,
			"candidatesTokenCount": charLen(text) / 4,
			"totalTokenCount":      (charLen(prompt) + charLen(text)) / 4,
		},
		"modelVersion": rm.Name,
	}

	if stream {
		startSSE(w)
		writeSSE(w, "data: "+string(marshalJSON(responseObj))+"\n\n")
		return
	}
	writeJSON(w, responseObj, http.StatusOK)
}

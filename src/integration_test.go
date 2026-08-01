package main

// Integration tests: spin up the real HTTP server (httptest.NewServer) and a
// mock Gemini web upstream, then drive the full request → auth → handler →
// upstream → response pipeline over real HTTP. No real Gemini network calls
// happen; geminiWebBase / embedAPIBase point at local httptest servers.

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// integrationEnv boots the real server with a mock Gemini web upstream.
func integrationEnv(t *testing.T, upstream http.HandlerFunc) *httptest.Server {
	t.Helper()
	oldBase := geminiWebBase
	oldConfig := Config
	up := httptest.NewServer(upstream)
	geminiWebBase = up.URL
	Config.LogRequests = false
	Config.RetryAttempts = 1
	Config.RetryDelaySec = 0
	Config.Proxy = ""
	srv := httptest.NewServer(&Server{})
	t.Cleanup(func() {
		srv.Close()
		up.Close()
		geminiWebBase = oldBase
		Config = oldConfig
	})
	return srv
}

// wrbBody builds a valid raw Gemini StreamGenerate response containing text.
func wrbBody(t *testing.T, text string) string {
	t.Helper()
	line := makeWrbLine(t, `[[null,[`+mustJSONString(t, text)+`]]]`)
	return ")]}'\n\n" + line + "\n"
}

// chatUpstream is a mock Gemini web upstream returning the given text.
func chatUpstream(t *testing.T, text string) http.HandlerFunc {
	t.Helper()
	body := wrbBody(t, text)
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}
}

func doJSON(t *testing.T, method, url, body string, headers map[string]string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, data
}

func TestIntegrationChatCompletion(t *testing.T) {
	var gotFReq string
	srv := integrationEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.Contains(r.URL.Path, "StreamGenerate") {
			t.Errorf("unexpected upstream call: %s %s", r.Method, r.URL.Path)
		}
		if !strings.Contains(r.Header.Get("Content-Type"), "x-www-form-urlencoded") {
			t.Errorf("expected form body, got content-type %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		gotFReq = string(body)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, wrbBody(t, "Hello from mock"))
	})
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Object  string `json:"object"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "chat.completion" || out.Model != "gemini-3.5-flash" {
		t.Fatalf("unexpected shape: %+v", out)
	}
	if len(out.Choices) != 1 || out.Choices[0].Message.Content != "Hello from mock" {
		t.Fatalf("unexpected choices: %+v", out.Choices)
	}
	if out.Choices[0].FinishReason != "stop" {
		t.Fatalf("unexpected finish_reason: %q", out.Choices[0].FinishReason)
	}
	if !strings.Contains(gotFReq, "f.req=") {
		t.Fatalf("upstream should receive f.req form field, got: %s", gotFReq)
	}
}

func TestIntegrationChatStream(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "Hello stream world"))
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}],"stream":true}`
	resp, err := http.Post(srv.URL+"/v1/chat/completions", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("expected SSE content-type, got %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	var deltas []string
	done := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		if payload == "[DONE]" {
			done = true
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", payload, err)
		}
		for _, c := range chunk.Choices {
			deltas = append(deltas, c.Delta.Content)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if !done {
		t.Fatal("missing [DONE] sentinel")
	}
	if got := strings.Join(deltas, ""); got != "Hello stream world" {
		t.Fatalf("stream content mismatch: %q", got)
	}
}

func TestIntegrationToolCalls(t *testing.T) {
	toolText := "```tool_call\n{\"name\": \"get_weather\", \"arguments\": {\"city\": \"Paris\"}}\n```"
	srv := integrationEnv(t, chatUpstream(t, toolText))
	defer srv.Close()

	payload := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "weather?"}],
		"tools": [{"type": "function", "function": {
			"name": "get_weather",
			"description": "Get weather for a city",
			"parameters": {"type": "object", "properties": {"city": {"type": "string"}}, "required": ["city"]}
		}}]
	}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					ID       string `json:"id"`
					Type     string `json:"type"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Choices) != 1 || out.Choices[0].FinishReason != "tool_calls" {
		t.Fatalf("expected tool_calls finish: %s", data)
	}
	tc := out.Choices[0].Message.ToolCalls
	if len(tc) != 1 || tc[0].Function.Name != "get_weather" {
		t.Fatalf("unexpected tool calls: %+v", tc)
	}
	if !strings.Contains(tc[0].Function.Arguments, "Paris") {
		t.Fatalf("arguments should contain Paris: %q", tc[0].Function.Arguments)
	}
}

func TestIntegrationUpstreamError(t *testing.T) {
	srv := integrationEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "upstream error") {
		t.Fatalf("error should mention upstream: %s", data)
	}
}

func TestIntegrationBardError(t *testing.T) {
	srv := integrationEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, ")]}'\n\n...BardErrorInfo [20]...")
	})
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "BardErrorInfo") {
		t.Fatalf("error should surface BardErrorInfo: %s", data)
	}
}

// TestIntegrationBardErrorJSONFormat reproduces the real-world failure: the
// mock upstream returns the JSON-wrapped BardErrorInfo",[1152] payload (as
// Gemini does for oversized prompts). The server must surface a 502 with the
// error code — not a silent 200 with content:null that broke hermes.
func TestIntegrationBardErrorJSONFormat(t *testing.T) {
	raw := `)]}'\n\n159\n[["wrb.fr",null,null,null,null,[13,null,[["type.googleapis.com/assistant.boq.bard.application.BardErrorInfo",[1152]]]]]]\n60\n`
	srv := integrationEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, raw)
	})
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "BardErrorInfo [1152]") {
		t.Fatalf("error should surface BardErrorInfo [1152]: %s", data)
	}
}

func TestIntegrationAuthOverHTTP(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "hi"))
	defer srv.Close()

	// Set the key *after* creating the env so integrationEnv's Config
	// snapshot/restore captures the clean default state.
	old := Config.APIKeys
	Config.APIKeys = []string{"sk-test"}
	defer func() { Config.APIKeys = old }()

	if resp, _ := doJSON(t, http.MethodGet, srv.URL+"/v1/models", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodGet, srv.URL+"/v1/models", "", map[string]string{"Authorization": "Bearer sk-test"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with Bearer, got %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodGet, srv.URL+"/v1/models", "", map[string]string{"x-api-key": "sk-test"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with x-api-key, got %d", resp.StatusCode)
	}
}

func TestIntegrationModelsOverHTTP(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "hi"))
	defer srv.Close()

	resp, data := doJSON(t, http.MethodGet, srv.URL+"/v1/models", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	var list struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range list.Data {
		if m["id"] == "gemini-3.6-flash" {
			found = true
		}
	}
	if !found {
		t.Fatalf("gemini-3.6-flash missing from models: %s", data)
	}

	resp, data = doJSON(t, http.MethodGet, srv.URL+"/v1beta/models", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for /v1beta/models, got %d", resp.StatusCode)
	}
	if !strings.Contains(string(data), "models/gemini-3.6-flash") {
		t.Fatalf("v1beta models missing entry: %s", data)
	}

	resp, data = doJSON(t, http.MethodGet, srv.URL+"/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 for /, got %d", resp.StatusCode)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil || root["status"] != "ok" {
		t.Fatalf("bad root response: %s", data)
	}
}

func TestIntegrationCORS(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "hi"))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/chat/completions", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS header")
	}
}

func TestIntegrationResponses(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "Hello from responses"))
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","input":"hi"}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/responses", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "response" || out.Status != "completed" {
		t.Fatalf("unexpected response: %s", data)
	}
	if len(out.Output) != 1 || out.Output[0].Type != "message" {
		t.Fatalf("expected message output: %s", data)
	}
	if len(out.Output[0].Content) != 1 || out.Output[0].Content[0].Text != "Hello from responses" {
		t.Fatalf("unexpected output content: %s", data)
	}
}

func TestIntegrationGoogleGenerateContent(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "Hello from google api"))
	defer srv.Close()

	payload := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1beta/models/gemini-3.5-flash:generateContent", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Candidates) != 1 || len(out.Candidates[0].Content.Parts) != 1 ||
		out.Candidates[0].Content.Parts[0].Text != "Hello from google api" {
		t.Fatalf("unexpected candidates: %s", data)
	}
}

func TestIntegrationGoogleStreamGenerateContent(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "Hello google stream"))
	defer srv.Close()

	payload := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	resp, err := http.Post(srv.URL+"/v1beta/models/gemini-3.5-flash:streamGenerateContent?alt=sse", "application/json", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	var texts []string
	final := false
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")
		var chunk struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("bad chunk %q: %v", payload, err)
		}
		for _, c := range chunk.Candidates {
			for _, p := range c.Content.Parts {
				texts = append(texts, p.Text)
			}
			if c.FinishReason == "STOP" {
				final = true
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(texts, ""); got != "Hello google stream" {
		t.Fatalf("stream content mismatch: %q", got)
	}
	if !final {
		t.Fatal("missing STOP finish chunk")
	}
}

func TestIntegrationEmbeddingsOverHTTP(t *testing.T) {
	oldBase := embedAPIBase
	oldConfig := Config
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "AIza-test" {
			t.Errorf("missing x-goog-api-key")
		}
		if !strings.Contains(r.URL.Path, "batchEmbedContents") {
			t.Errorf("expected batch endpoint, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],"usageMetadata":{"promptTokenCount":7}}`)
	}))
	embedAPIBase = up.URL
	Config.EmbeddingsAPIKey = "AIza-test"
	Config.LogRequests = false
	srv := httptest.NewServer(&Server{})
	t.Cleanup(func() {
		srv.Close()
		up.Close()
		embedAPIBase = oldBase
		Config = oldConfig
	})

	payload := `{"model":"text-embedding-3-small","input":["a","b"]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/embeddings", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Object != "list" || len(out.Data) != 2 {
		t.Fatalf("unexpected embeddings response: %s", data)
	}
	if out.Data[0].Index != 0 || out.Data[1].Index != 1 || out.Data[0].Embedding[1] != 0.2 {
		t.Fatalf("unexpected data: %+v", out.Data)
	}
}

// TestIntegrationEmptyUpstreamResponse verifies that a 200 with no
// extractable text is surfaced as a retryable upstream error (502 after
// retries), not a silent content:null.
func TestIntegrationEmptyUpstreamResponse(t *testing.T) {
	srv := integrationEnv(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, ")]}'\n\n159\n[[\"wrb.fr\",null,null,null,null,[13,null,null]]]\n")
	})
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("expected 502, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "empty response") {
		t.Fatalf("error should mention empty response: %s", data)
	}
}

func TestIntegrationInvalidJSON(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "hi"))
	defer srv.Close()

	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", "{not json", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", resp.StatusCode, data)
	}
}

func TestIntegrationHealthEndpoint(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "hi"))
	defer srv.Close()

	resp, data := doJSON(t, http.MethodGet, srv.URL+"/healthz", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for /healthz (no such route), got %d: %s", resp.StatusCode, data)
	}
}

// TestIntegrationMultimodalRequest passes an image_url message and verifies
// the server still responds (images are noted as unsupported, not fatal).
func TestIntegrationMultimodalRequest(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "text-only reply"))
	defer srv.Close()

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":[{"type":"text","text":"what is this?"},{"type":"image_url","image_url":{"url":"https://example.com/x.png"}}]}]}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "text-only reply") {
		t.Fatalf("expected model reply in response: %s", data)
	}
}

// TestIntegrationPromptInjection keeps a smoke check that a normal tool
// payload with tool_choice=none yields plain text (no tool_calls).
func TestIntegrationToolChoiceNone(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "plain text answer"))
	defer srv.Close()

	payload := `{
		"model": "gemini-3.5-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "f", "description": "d", "parameters": {"type": "object", "properties": {}}}}],
		"tool_choice": "none"
	}`
	resp, data := doJSON(t, http.MethodPost, srv.URL+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	if strings.Contains(string(data), "tool_calls") {
		t.Fatalf("tool_choice=none should not produce tool_calls: %s", data)
	}
}

// TestIntegrationResponsesGet404 verifies GET /v1/responses is not routed.
func TestIntegrationResponsesGet404(t *testing.T) {
	srv := integrationEnv(t, chatUpstream(t, "hi"))
	defer srv.Close()

	resp, _ := doJSON(t, http.MethodGet, srv.URL+"/v1/responses", "", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for GET /v1/responses, got %d", resp.StatusCode)
	}
}

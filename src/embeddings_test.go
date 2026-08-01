package main

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestParseEmbeddingInputs(t *testing.T) {
	if got, err := parseEmbeddingInputs(map[string]any{"input": "hello"}); err != nil || len(got) != 1 || got[0] != "hello" {
		t.Fatalf("string input: got %v, %v", got, err)
	}
	got, err := parseEmbeddingInputs(map[string]any{"input": []any{"a", "b", "c"}})
	if err != nil || len(got) != 3 || got[0] != "a" || got[2] != "c" {
		t.Fatalf("array input: got %v, %v", got, err)
	}
	if _, err := parseEmbeddingInputs(map[string]any{}); err == nil {
		t.Fatal("missing input should error")
	}
	if _, err := parseEmbeddingInputs(map[string]any{"input": ""}); err == nil {
		t.Fatal("empty string input should error")
	}
	if _, err := parseEmbeddingInputs(map[string]any{"input": []any{}}); err == nil {
		t.Fatal("empty array input should error")
	}
	if _, err := parseEmbeddingInputs(map[string]any{"input": []any{"ok", 42}}); err == nil {
		t.Fatal("non-string array item should error")
	}
}

func TestResolveEmbeddingModel(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	Config.EmbeddingsModel = ""
	if got := resolveEmbeddingModel("text-embedding-3-small"); got != "text-embedding-004" {
		t.Fatalf("OpenAI name should map to default backend, got %q", got)
	}
	if got := resolveEmbeddingModel(""); got != "text-embedding-004" {
		t.Fatalf("empty model should use default, got %q", got)
	}
	Config.EmbeddingsModel = "gemini-embedding-001"
	if got := resolveEmbeddingModel("text-embedding-ada-002"); got != "gemini-embedding-001" {
		t.Fatalf("OpenAI name should map to configured backend, got %q", got)
	}
	if got := resolveEmbeddingModel("gemini-embedding-001"); got != "gemini-embedding-001" {
		t.Fatalf("native name should pass through, got %q", got)
	}
	if got := resolveEmbeddingModel("models/text-embedding-004"); got != "text-embedding-004" {
		t.Fatalf("models/ prefix should be stripped, got %q", got)
	}
}

func TestBase64Embedding(t *testing.T) {
	values := []float64{1.5, -2.25, 0}
	enc := base64Embedding(values)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("bad base64: %v", err)
	}
	if len(raw) != len(values)*4 {
		t.Fatalf("expected %d bytes, got %d", len(values)*4, len(raw))
	}
	if math.Float32frombits(uint32(raw[0])<<24|uint32(raw[1])<<16|uint32(raw[2])<<8|uint32(raw[3])) != 1.5 {
		t.Fatal("first value should round-trip as float32 1.5")
	}
}

func TestHandleEmbeddings(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "AIza-test" {
			t.Errorf("missing x-goog-api-key header")
		}
		if !strings.Contains(r.URL.Path, "batchEmbedContents") {
			t.Errorf("expected batch endpoint, got %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("bad body: %v", err)
		}
		if len(body["requests"].([]any)) != 2 {
			t.Errorf("expected 2 requests, got %v", body["requests"])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"embeddings": [
				{"values": [0.1, 0.2]},
				{"values": [0.3, 0.4]}
			],
			"usageMetadata": {"promptTokenCount": 7}
		}`))
	}))
	defer upstream.Close()
	oldBase := embedAPIBase
	embedAPIBase = upstream.URL
	defer func() { embedAPIBase = oldBase }()

	Config.EmbeddingsAPIKey = "AIza-test"
	Config.LogRequests = false

	body := `{"model": "text-embedding-3-small", "input": ["a", "b"]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	(&Server{}).handleEmbeddings(rr, req, []byte(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Object string `json:"object"`
		Model  string `json:"model"`
		Data   []struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
		Usage struct {
			PromptTokens int `json:"prompt_tokens"`
			TotalTokens  int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad response JSON: %v", err)
	}
	if out.Object != "list" || len(out.Data) != 2 {
		t.Fatalf("unexpected object/data: %+v", out)
	}
	if out.Model != "text-embedding-3-small" {
		t.Fatalf("model should echo the requested name, got %q", out.Model)
	}
	if out.Data[0].Index != 0 || out.Data[1].Index != 1 {
		t.Fatalf("indexes: %+v", out.Data)
	}
	if len(out.Data[0].Embedding) != 2 || out.Data[0].Embedding[1] != 0.2 {
		t.Fatalf("embeddings: %+v", out.Data[0].Embedding)
	}
	if out.Usage.PromptTokens != 7 || out.Usage.TotalTokens != 7 {
		t.Fatalf("usage: %+v", out.Usage)
	}
}

func TestHandleEmbeddingsBase64(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"embedding": {"values": [1.0]}, "usageMetadata": {"promptTokenCount": 3}}`))
	}))
	defer upstream.Close()
	oldBase := embedAPIBase
	embedAPIBase = upstream.URL
	defer func() { embedAPIBase = oldBase }()

	Config.EmbeddingsAPIKey = "AIza-test"
	Config.LogRequests = false

	body := `{"input": "single", "encoding_format": "base64"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	(&Server{}).handleEmbeddings(rr, req, []byte(body))

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d, body %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Data []struct {
			Embedding string `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("bad JSON: %v", err)
	}
	if len(out.Data) != 1 || out.Data[0].Embedding == "" {
		t.Fatalf("expected base64 embedding, got %+v", out.Data)
	}
	if _, err := base64.StdEncoding.DecodeString(out.Data[0].Embedding); err != nil {
		t.Fatalf("embedding is not valid base64: %v", err)
	}
}

func TestHandleEmbeddingsNoKey(t *testing.T) {
	old := Config
	defer func() { Config = old }()
	Config.EmbeddingsAPIKey = ""
	Config.LogRequests = false

	body := `{"input": "hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	(&Server{}).handleEmbeddings(rr, req, []byte(body))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without key, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "GEMINI_EMBEDDINGS_API_KEY") {
		t.Fatalf("error should mention the env var, got %s", rr.Body.String())
	}
}

func TestHandleEmbeddingsUpstreamError(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"code": 400, "message": "API key not valid", "status": "INVALID_ARGUMENT"}}`))
	}))
	defer upstream.Close()
	oldBase := embedAPIBase
	embedAPIBase = upstream.URL
	defer func() { embedAPIBase = oldBase }()

	Config.EmbeddingsAPIKey = "bad-key"
	Config.LogRequests = false

	body := `{"input": "hi"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/embeddings", strings.NewReader(body))
	rr := httptest.NewRecorder()
	(&Server{}).handleEmbeddings(rr, req, []byte(body))

	if rr.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 on upstream error, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "INVALID_ARGUMENT") {
		t.Fatalf("error should surface upstream status, got %s", rr.Body.String())
	}
}

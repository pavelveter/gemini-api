package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"
)

// The Gemini web session (cookie + XSRF) used for chat does NOT expose an
// embeddings endpoint, so /v1/embeddings proxies to the official Google AI
// Studio API, which requires its own API key (Config.EmbeddingsAPIKey).
// embedAPIBase is a variable so tests can point it at an httptest server.
var embedAPIBase = "https://generativelanguage.googleapis.com"

// defaultEmbeddingsModel is the Gemini embedding model used when nothing is
// configured. text-embedding-004 is stable, cheap (free tier) and supports
// outputDimensionality, mirroring OpenAI's text-embedding-3 behavior.
const defaultEmbeddingsModel = "text-embedding-004"

// resolveEmbeddingModel picks the Gemini model to call for an embeddings
// request. Native Gemini embedding model names pass through; OpenAI-style
// names (text-embedding-3-small, text-embedding-ada-002, ...) are routed to
// the configured backend model (default text-embedding-004).
func resolveEmbeddingModel(requested string) string {
	backend := Config.EmbeddingsModel
	if backend == "" {
		backend = defaultEmbeddingsModel
	}
	if requested != "" {
		clean := strings.TrimPrefix(requested, "models/")
		if strings.HasPrefix(clean, "gemini-embedding") || strings.HasPrefix(clean, "text-embedding-004") {
			return clean
		}
	}
	return backend
}

// parseEmbeddingInputs normalizes the OpenAI `input` field (string or array
// of strings) into a list of texts. Token-array inputs are not supported.
func parseEmbeddingInputs(req map[string]any) ([]string, error) {
	v, ok := req["input"]
	if !ok {
		return nil, fmt.Errorf("missing required field: input")
	}
	switch t := v.(type) {
	case string:
		if strings.TrimSpace(t) == "" {
			return nil, fmt.Errorf("input must not be empty")
		}
		return []string{t}, nil
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			s, ok := item.(string)
			if !ok || strings.TrimSpace(s) == "" {
				return nil, fmt.Errorf("input must be a string or an array of strings")
			}
			out = append(out, s)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("input array must not be empty")
		}
		return out, nil
	default:
		return nil, fmt.Errorf("input must be a string or an array of strings")
	}
}

// embedRequest builds a Gemini embedContent request body.
func embedRequest(model, text string, dimensions int) map[string]any {
	req := map[string]any{
		"model":   "models/" + model,
		"content": map[string]any{"parts": []any{map[string]any{"text": text}}},
	}
	if dimensions > 0 {
		req["outputDimensionality"] = dimensions
	}
	return req
}

// base64Embedding encodes a float vector as base64 of big-endian float32
// bytes, matching OpenAI's encoding_format=base64.
func base64Embedding(values []float64) string {
	buf := make([]byte, 0, len(values)*4)
	for _, v := range values {
		bits := math.Float32bits(float32(v))
		buf = append(buf, byte(bits>>24), byte(bits>>16), byte(bits>>8), byte(bits))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// geminiEmbed calls the official Google AI Studio API: :embedContent for a
// single text, :batchEmbedContents for multiple. Returns one vector per input
// plus the total prompt token count reported upstream.
func geminiEmbed(model string, inputs []string, dimensions int) ([][]float64, int, error) {
	client := getHTTPClient()
	timeout := time.Duration(Config.RequestTimeoutSec) * time.Second

	endpoint := embedAPIBase + "/v1beta/models/" + model + ":embedContent"
	if len(inputs) > 1 {
		endpoint = embedAPIBase + "/v1beta/models/" + model + ":batchEmbedContents"
	}

	var payload any
	if len(inputs) == 1 {
		payload = embedRequest(model, inputs[0], dimensions)
	} else {
		requests := make([]any, 0, len(inputs))
		for _, t := range inputs {
			requests = append(requests, embedRequest(model, t, dimensions))
		}
		payload = map[string]any{"requests": requests}
	}

	pb, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(pb)))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", Config.EmbeddingsAPIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := readAllTimeout(resp.Body, timeout)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode != http.StatusOK {
		var e struct {
			Error *struct {
				Message string `json:"message"`
				Status  string `json:"status"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error != nil {
			s := e.Error.Status
			if s == "" {
				s = resp.Status
			}
			return nil, 0, fmt.Errorf("%s: %s", s, e.Error.Message)
		}
		return nil, 0, fmt.Errorf("upstream HTTP %s", resp.Status)
	}

	var g struct {
		Embedding struct {
			Values []float64 `json:"values"`
		} `json:"embedding"`
		Embeddings []struct {
			Values []float64 `json:"values"`
		} `json:"embeddings"`
		Error *struct {
			Message string `json:"message"`
			Status  string `json:"status"`
		} `json:"error"`
		UsageMetadata struct {
			PromptTokenCount int `json:"promptTokenCount"`
			TotalTokenCount  int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, 0, fmt.Errorf("bad upstream response: %v", err)
	}
	if g.Error != nil {
		status := g.Error.Status
		if status == "" {
			status = "error"
		}
		return nil, 0, fmt.Errorf("%s: %s", status, g.Error.Message)
	}

	var out [][]float64
	if len(inputs) == 1 {
		if g.Embedding.Values == nil {
			return nil, 0, fmt.Errorf("empty embedding in upstream response")
		}
		out = [][]float64{g.Embedding.Values}
	} else {
		if len(g.Embeddings) == 0 {
			return nil, 0, fmt.Errorf("empty embeddings in upstream response")
		}
		for _, e := range g.Embeddings {
			out = append(out, e.Values)
		}
	}

	tokens := g.UsageMetadata.PromptTokenCount
	if tokens == 0 {
		tokens = g.UsageMetadata.TotalTokenCount
	}
	return out, tokens, nil
}

// ─── /v1/embeddings ──────────────────────────────────────────────────────────

func (s *Server) handleEmbeddings(w http.ResponseWriter, r *http.Request, body []byte) {
	req := parseBody(body)
	if req == nil {
		writeJSON(w, errorResp("invalid JSON"), http.StatusBadRequest)
		return
	}
	inputs, err := parseEmbeddingInputs(req)
	if err != nil {
		writeJSON(w, errorResp(err.Error()), http.StatusBadRequest)
		return
	}
	encodingFormat := getStr(req, "encoding_format")
	if encodingFormat == "" {
		encodingFormat = "float"
	}
	if encodingFormat != "float" && encodingFormat != "base64" {
		writeJSON(w, errorResp("encoding_format must be \"float\" or \"base64\""), http.StatusBadRequest)
		return
	}
	if Config.EmbeddingsAPIKey == "" {
		writeJSON(w, errorResp(
			"embeddings are not configured: set GEMINI_EMBEDDINGS_API_KEY "+
				"(official Google AI Studio API key, https://aistudio.google.com/apikey) "+
				"in .env"), http.StatusBadRequest)
		return
	}
	requestedModel := getStr(req, "model")
	backend := resolveEmbeddingModel(requestedModel)
	dimensions := 0
	if d, ok := req["dimensions"].(float64); ok && d > 0 {
		dimensions = int(d)
	}

	values, tokens, err := geminiEmbed(backend, inputs, dimensions)
	if err != nil {
		log(fmt.Sprintf("Embeddings upstream error: %v", err))
		writeJSON(w, errorResp("upstream error: "+err.Error()), http.StatusBadGateway)
		return
	}

	data := make([]map[string]any, 0, len(values))
	for i, vals := range values {
		item := map[string]any{"object": "embedding", "index": i}
		if encodingFormat == "base64" {
			item["embedding"] = base64Embedding(vals)
		} else {
			item["embedding"] = vals
		}
		data = append(data, item)
	}
	respModel := requestedModel
	if respModel == "" {
		respModel = backend
	}
	writeJSON(w, map[string]any{
		"object": "list",
		"data":   data,
		"model":  respModel,
		"usage":  map[string]any{"prompt_tokens": tokens, "total_tokens": tokens},
	}, http.StatusOK)
}

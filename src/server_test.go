package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func doReq(t *testing.T, handler http.Handler, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)
	return rr
}

func TestServerRoot(t *testing.T) {
	rr := doReq(t, &Server{}, http.MethodGet, "/", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "ok" || resp["version"] != Version {
		t.Fatalf("unexpected: %v", resp)
	}
}

func TestServerModels(t *testing.T) {
	rr := doReq(t, &Server{}, http.MethodGet, "/v1/models", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	data, _ := resp["data"].([]any)
	if len(data) == 0 {
		t.Fatalf("expected models: %v", resp)
	}
	first := data[0].(map[string]any)
	if first["object"] != "model" || first["owned_by"] != "google" {
		t.Fatalf("unexpected model shape: %v", first)
	}
}

func TestServerGoogleModels(t *testing.T) {
	rr := doReq(t, &Server{}, http.MethodGet, "/v1beta/models", "", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	models, _ := resp["models"].([]any)
	if len(models) == 0 {
		t.Fatalf("expected models: %v", resp)
	}
	m := models[0].(map[string]any)
	if m["name"] != "models/gemini-3.6-flash" {
		t.Fatalf("unexpected first model: %v", m)
	}
}

func TestServerOptions(t *testing.T) {
	rr := doReq(t, &Server{}, http.MethodOptions, "/v1/chat/completions", "", nil)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", rr.Code)
	}
	if rr.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatal("missing CORS header")
	}
	if rr.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Fatal("missing CORS methods")
	}
}

func TestServerNotFound(t *testing.T) {
	rr := doReq(t, &Server{}, http.MethodGet, "/nope", "", nil)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}
}

func TestServerChatInvalidJSON(t *testing.T) {
	rr := doReq(t, &Server{}, http.MethodPost, "/v1/chat/completions", "{not json", nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestServerChatEmptyPrompt(t *testing.T) {
	body := `{"model":"gemini-3.5-flash","messages":[]}`
	rr := doReq(t, &Server{}, http.MethodPost, "/v1/chat/completions", body, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

func TestServerAuth(t *testing.T) {
	old := Config.APIKeys
	Config.APIKeys = []string{"sk-test"}
	defer func() { Config.APIKeys = old }()

	if rr := doReq(t, &Server{}, http.MethodGet, "/v1/models", "", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", rr.Code)
	}
	if rr := doReq(t, &Server{}, http.MethodGet, "/v1/models", "", map[string]string{"Authorization": "Bearer sk-test"}); rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with Bearer key, got %d", rr.Code)
	}
	if rr := doReq(t, &Server{}, http.MethodGet, "/v1/models", "", map[string]string{"x-api-key": "sk-test"}); rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with x-api-key, got %d", rr.Code)
	}
	if rr := doReq(t, &Server{}, http.MethodGet, "/v1beta/models", "", map[string]string{"x-goog-api-key": "sk-test"}); rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with x-goog-api-key, got %d", rr.Code)
	}
}

func TestServerAuthQueryKey(t *testing.T) {
	old := Config.APIKeys
	Config.APIKeys = []string{"sk-test"}
	defer func() { Config.APIKeys = old }()

	if rr := doReq(t, &Server{}, http.MethodGet, "/v1/models?key=sk-test", "", nil); rr.Code != http.StatusOK {
		t.Fatalf("expected 200 with query key, got %d", rr.Code)
	}
	if rr := doReq(t, &Server{}, http.MethodGet, "/v1/models?key=wrong", "", nil); rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 with wrong query key, got %d", rr.Code)
	}
}

func TestServerChatModelError(t *testing.T) {
	// Invalid @think suffix should produce 400 before any upstream call.
	body := `{"model":"gemini-3.5-flash@think=abc","messages":[{"role":"user","content":"hi"}]}`
	rr := doReq(t, &Server{}, http.MethodPost, "/v1/chat/completions", body, nil)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rr.Code)
	}
}

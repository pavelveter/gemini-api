package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotenv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n" +
		"PORT=8081\n" +
		"EMPTY=\n" +
		"QUOTED=\"hello world\"\n" +
		"SINGLE='single value'\n" +
		"MULTI=sk-a, sk-b\n" +
		"EXPORTED=export me\n" +
		"BADLINE\n" +
		"export PREFIXED=value\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	vars := loadDotenv(path)

	checks := map[string]string{
		"PORT":     "8081",
		"EMPTY":    "",
		"QUOTED":   "hello world",
		"SINGLE":   "single value",
		"MULTI":    "sk-a, sk-b",
		"EXPORTED": "export me",
		"PREFIXED": "value",
	}
	for k, want := range checks {
		if got, ok := vars[k]; !ok || got != want {
			t.Fatalf("key %q: got %q, want %q (present=%v)", k, got, want, ok)
		}
	}
	if _, ok := vars["BADLINE"]; ok {
		t.Fatal("line without '=' should be skipped")
	}
}

func TestLoadDotenvMissingFile(t *testing.T) {
	if vars := loadDotenv(filepath.Join(t.TempDir(), "nope.env")); len(vars) != 0 {
		t.Fatalf("missing file should yield empty map: %v", vars)
	}
}

func TestSplitAPIKeys(t *testing.T) {
	keys := splitAPIKeys("sk-a, sk-b ,,sk-c")
	if len(keys) != 3 || keys[0] != "sk-a" || keys[1] != "sk-b" || keys[2] != "sk-c" {
		t.Fatalf("unexpected: %v", keys)
	}
	if keys := splitAPIKeys(""); len(keys) != 0 {
		t.Fatalf("empty string should yield no keys: %v", keys)
	}
}

func TestApplyEnvConfig(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	dotenv := map[string]string{
		envPort:          "9999",
		envRetryAttempts: "5",
		envDefaultModel:  "gemini-3.5-flash-thinking",
		envLogRequests:   "false",
		envAPIKeys:       "sk-a, sk-b",
		envCookieFile:    "/tmp/c.txt",
		envProxy:         "http://127.0.0.1:7890",
	}
	applyEnvConfig(dotenv)

	if Config.Port != 9999 {
		t.Fatalf("port: got %d", Config.Port)
	}
	if Config.RetryAttempts != 5 {
		t.Fatalf("retry attempts: got %d", Config.RetryAttempts)
	}
	if Config.DefaultModel != "gemini-3.5-flash-thinking" {
		t.Fatalf("default model: got %q", Config.DefaultModel)
	}
	if Config.LogRequests {
		t.Fatal("log requests should be false")
	}
	if len(Config.APIKeys) != 2 || Config.APIKeys[0] != "sk-a" || Config.APIKeys[1] != "sk-b" {
		t.Fatalf("api keys: got %v", Config.APIKeys)
	}
	if Config.CookieFile != "/tmp/c.txt" || Config.Proxy != "http://127.0.0.1:7890" {
		t.Fatalf("cookie/proxy not applied: %q %q", Config.CookieFile, Config.Proxy)
	}
}

func TestApplyEnvConfigInvalidValuesIgnored(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	// Seed a non-empty key list so the clearing behavior is actually tested.
	Config.APIKeys = []string{"sk-existing"}

	dotenv := map[string]string{
		envPort:    "not-a-number",
		envHost:    "",
		envAPIKeys: "",
	}
	applyEnvConfig(dotenv)

	if Config.Port != defaultConfig.Port {
		t.Fatalf("invalid port should be ignored, got %d", Config.Port)
	}
	if Config.Host != defaultConfig.Host {
		t.Fatalf("empty host should be ignored, got %q", Config.Host)
	}
	if len(Config.APIKeys) != 0 {
		t.Fatalf("empty api keys should clear the list, got %v", Config.APIKeys)
	}
}

func TestApplyEnvConfigRealEnvWins(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	os.Setenv(envPort, "7777")
	defer os.Unsetenv(envPort)

	dotenv := map[string]string{envPort: "9999"}
	applyEnvConfig(dotenv)
	if Config.Port != 7777 {
		t.Fatalf("real env should win over .env: got %d", Config.Port)
	}
}

func TestApplyEnvConfigAPIKeyPrecedence(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	// GEMINI_API_KEY (singular) must win over GEMINI_API_KEYS (plural).
	dotenv := map[string]string{
		envAPIKey:  "sk-single",
		envAPIKeys: "sk-a, sk-b",
	}
	applyEnvConfig(dotenv)
	if len(Config.APIKeys) != 1 || Config.APIKeys[0] != "sk-single" {
		t.Fatalf("GEMINI_API_KEY should take precedence, got %v", Config.APIKeys)
	}

	// Without the singular key, the plural list applies.
	dotenv = map[string]string{envAPIKeys: "sk-c, sk-d"}
	applyEnvConfig(dotenv)
	if len(Config.APIKeys) != 2 || Config.APIKeys[0] != "sk-c" || Config.APIKeys[1] != "sk-d" {
		t.Fatalf("GEMINI_API_KEYS should apply when singular is unset, got %v", Config.APIKeys)
	}
}

func TestApplyEnvConfigRequireAuth(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	dotenv := map[string]string{envRequireAuth: "true"}
	applyEnvConfig(dotenv)
	if !Config.RequireAuth {
		t.Fatal("GEMINI_REQUIRE_AUTH=true should enable require_auth")
	}

	Config.RequireAuth = true
	dotenv = map[string]string{envRequireAuth: "false"}
	applyEnvConfig(dotenv)
	if Config.RequireAuth {
		t.Fatal("GEMINI_REQUIRE_AUTH=false should disable require_auth")
	}
}

func TestRequireAuthErr(t *testing.T) {
	old := Config
	defer func() { Config = old }()

	// Fail-safe: require_auth on but no keys -> refuse to start.
	Config.RequireAuth = true
	Config.APIKeys = nil
	if err := requireAuthErr(); err == nil {
		t.Fatal("require_auth with no keys should return an error")
	}

	// require_auth on with keys -> OK.
	Config.APIKeys = []string{"sk-ok"}
	if err := requireAuthErr(); err != nil {
		t.Fatalf("require_auth with keys should pass, got %v", err)
	}

	// require_auth off without keys -> OK (open API is allowed locally).
	Config.RequireAuth = false
	Config.APIKeys = nil
	if err := requireAuthErr(); err != nil {
		t.Fatalf("no require_auth should pass, got %v", err)
	}
}

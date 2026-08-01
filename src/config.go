package main

// AppConfig is the runtime configuration. Every field is populated from
// environment variables (.env file or real env) on top of the built-in
// defaults; CLI flags override individual fields afterwards.
//
// .env is the single source of truth — config.json support was removed.
type AppConfig struct {
	Port              int
	Host              string
	RetryAttempts     int
	RetryDelaySec     int
	RequestTimeoutSec int
	GeminiBL          string
	AuthUser          string
	XSRFToken         string
	DefaultModel      string
	LogRequests       bool
	CookieFile        string
	Proxy             string
	APIKeys           []string
	RequireAuth       bool
	EmbeddingsAPIKey  string
	EmbeddingsModel   string
	DumpRaw           bool
	MaxToolDesc       int
}

var defaultConfig = AppConfig{
	Port:              8081,
	Host:              "0.0.0.0",
	RetryAttempts:     3,
	RetryDelaySec:     2,
	RequestTimeoutSec: 180,
	GeminiBL:          "boq_assistant-bard-web-server_20260716.08_p0",
	DefaultModel:      "gemini-3.6-flash",
	LogRequests:       true,
	MaxToolDesc:       150,
}

// Config is the live configuration. Populate it with applyEnvConfig
// (from .env / environment variables), then apply CLI flags.
var Config = defaultConfig

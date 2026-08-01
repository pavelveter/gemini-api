package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvConfigKeys lists every GEMINI_* environment variable this program reads.
// The user can override any of these through a .env file or the process
// environment; CLI flags still take precedence over both.
const (
	envPort          = "GEMINI_PORT"
	envHost          = "GEMINI_HOST"
	envRetryAttempts = "GEMINI_RETRY_ATTEMPTS"
	envRetryDelaySec = "GEMINI_RETRY_DELAY_SEC"
	envReqTimeoutSec = "GEMINI_REQUEST_TIMEOUT_SEC"
	envGeminiBL      = "GEMINI_BL"
	envAuthUser      = "GEMINI_AUTH_USER"
	envXSRFToken     = "GEMINI_XSRF_TOKEN"
	envDefaultModel  = "GEMINI_DEFAULT_MODEL"
	envLogRequests   = "GEMINI_LOG_REQUESTS"
	envCookieFile    = "GEMINI_COOKIE_FILE"
	envProxy         = "GEMINI_PROXY"
	envAPIKey        = "GEMINI_API_KEY"
	envAPIKeys       = "GEMINI_API_KEYS"
	envRequireAuth   = "GEMINI_REQUIRE_AUTH"
	envEmbedAPIKey   = "GEMINI_EMBEDDINGS_API_KEY"
	envEmbedModel    = "GEMINI_EMBEDDINGS_MODEL"
	envWebBase       = "GEMINI_WEB_BASE"
	envEmbedAPIBase  = "GEMINI_EMBEDDINGS_API_BASE"
	envDumpRaw       = "GEMINI_DUMP_RAW"
	envMaxToolDesc   = "GEMINI_MAX_TOOL_DESC"
)

// loadDotenv reads a .env file into a key->value map. Lines starting with '#'
// and blank lines are ignored; an optional "export " prefix is stripped and
// surrounding single/double quotes are removed from values. Missing or
// unreadable files yield an empty map.
func loadDotenv(path string) map[string]string {
	vars := map[string]string{}
	if path == "" {
		return vars
	}
	f, err := os.Open(path)
	if err != nil {
		return vars
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		vars[key] = value
	}
	if err := sc.Err(); err != nil {
		configWarn("Dotenv scan error: %v", err)
	}
	return vars
}

// configWarn prints a configuration warning to stderr regardless of the
// LogRequests setting, so .env/config typos are never silently hidden.
func configWarn(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[config] "+format+"\n", args...)
}

// envValue returns the effective value for an env key: the real process
// environment wins over values from a .env file.
func envValue(dotenv map[string]string, key string) (string, bool) {
	if v, ok := os.LookupEnv(key); ok {
		return v, true
	}
	if v, ok := dotenv[key]; ok {
		return v, true
	}
	return "", false
}

// findEnvFile searches standard locations for a .env file.
func findEnvFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "~"
	}
	for _, p := range []string{"./.env", filepath.Join(home, ".config", "gemini-web2api", ".env")} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// splitAPIKeys parses a comma-separated API key list, trimming whitespace and
// dropping empty entries.
func splitAPIKeys(v string) []string {
	var keys []string
	for _, p := range strings.Split(v, ",") {
		if s := strings.TrimSpace(p); s != "" {
			keys = append(keys, s)
		}
	}
	return keys
}

// applyInt applies an integer env value, logging a warning on parse failure.
func applyInt(dotenv map[string]string, key string, set func(int)) {
	v, ok := envValue(dotenv, key)
	if !ok {
		return
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		configWarn("Ignoring invalid %s=%q: %v", key, v, err)
		return
	}
	set(n)
}

// applyBool applies a boolean env value, logging a warning on parse failure.
func applyBool(dotenv map[string]string, key string, set func(bool)) {
	v, ok := envValue(dotenv, key)
	if !ok {
		return
	}
	b, err := strconv.ParseBool(strings.TrimSpace(v))
	if err != nil {
		configWarn("Ignoring invalid %s=%q: %v", key, v, err)
		return
	}
	set(b)
}

// applyString applies a string env value. Empty values are skipped unless
// allowEmpty is true (used for fields that may be explicitly unset/cleared).
func applyString(dotenv map[string]string, key string, set func(string), allowEmpty bool) {
	v, ok := envValue(dotenv, key)
	if !ok {
		return
	}
	v = strings.TrimSpace(v)
	if v == "" && !allowEmpty {
		return
	}
	set(v)
}

// applyEnvConfig overrides Config fields from environment variables
// (real process env takes precedence over the .env file).
func applyEnvConfig(dotenv map[string]string) {
	applyInt(dotenv, envPort, func(n int) { Config.Port = n })
	applyString(dotenv, envHost, func(s string) { Config.Host = s }, false)
	applyInt(dotenv, envRetryAttempts, func(n int) { Config.RetryAttempts = n })
	applyInt(dotenv, envRetryDelaySec, func(n int) { Config.RetryDelaySec = n })
	applyInt(dotenv, envReqTimeoutSec, func(n int) { Config.RequestTimeoutSec = n })
	applyString(dotenv, envGeminiBL, func(s string) { Config.GeminiBL = s }, false)
	applyString(dotenv, envAuthUser, func(s string) { Config.AuthUser = s }, true)
	applyString(dotenv, envXSRFToken, func(s string) { Config.XSRFToken = s }, true)
	applyString(dotenv, envDefaultModel, func(s string) { Config.DefaultModel = s }, false)
	applyBool(dotenv, envLogRequests, func(b bool) { Config.LogRequests = b })
	applyString(dotenv, envCookieFile, func(s string) { Config.CookieFile = s }, true)
	applyString(dotenv, envProxy, func(s string) { Config.Proxy = s }, true)
	applyBool(dotenv, envRequireAuth, func(b bool) { Config.RequireAuth = b })
	applyString(dotenv, envEmbedAPIKey, func(s string) { Config.EmbeddingsAPIKey = s }, true)
	applyString(dotenv, envEmbedModel, func(s string) { Config.EmbeddingsModel = s }, false)
	// Upstream base URLs are configurable so tests / local mocks can point the
	// server at a local upstream instead of the real Google endpoints.
	applyString(dotenv, envWebBase, func(s string) { geminiWebBase = s }, false)
	applyString(dotenv, envEmbedAPIBase, func(s string) { embedAPIBase = s }, false)
	applyBool(dotenv, envDumpRaw, func(b bool) { Config.DumpRaw = b })
	applyInt(dotenv, envMaxToolDesc, func(n int) { Config.MaxToolDesc = n })
	// GEMINI_API_KEY (singular) takes precedence over GEMINI_API_KEYS (plural).
	if v, ok := envValue(dotenv, envAPIKey); ok {
		Config.APIKeys = splitAPIKeys(v)
	} else {
		applyString(dotenv, envAPIKeys, func(s string) { Config.APIKeys = splitAPIKeys(s) }, true)
	}
}

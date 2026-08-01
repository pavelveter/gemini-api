package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// log mirrors the Python `log()` helper: stderr with a HH:MM:SS timestamp.
func log(msg string) {
	if !Config.LogRequests {
		return
	}
	fmt.Fprintf(os.Stderr, "[%s] %s\n", time.Now().Format("15:04:05"), msg)
}

// getStr returns the string value of key in m, or "" when absent/not a string.
func getStr(m map[string]any, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// getBool returns the bool value of key in m.
func getBool(m map[string]any, key string) bool {
	if v, ok := m[key].(bool); ok {
		return v
	}
	return false
}

// anyList converts an []any of objects into []map[string]any, dropping non-objects.
func anyList(v any) []map[string]any {
	list, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// marshalJSON marshals v without HTML escaping, mirroring Python's
// json.dumps(..., ensure_ascii=False).
func marshalJSON(v any) []byte {
	var sb strings.Builder
	enc := json.NewEncoder(&sb)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return []byte("null")
	}
	return []byte(strings.TrimSuffix(sb.String(), "\n"))
}

// randomHex returns n lowercase hex characters from a cryptographically
// secure source.
func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// newUUID returns a random RFC 4122 version 4 UUID string.
func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// charLen returns the number of runes in s (Python len() counts code points).
func charLen(s string) int {
	return utf8.RuneCountInString(s)
}

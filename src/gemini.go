package main

import (
	"bufio"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	// (?s) enables DOTALL so `.*?` can span newlines, matching re.DOTALL.
	reCleanCodeBlock = regexp.MustCompile("(?s)```(?:python|javascript|text)\\?code_(?:reference|stdout)&code_event_index=\\d+\\n.*?```\\n?")
	reCardContent    = regexp.MustCompile(`http://googleusercontent\.com/card_content/\d+\n?`)
	// Matches both the plain wire format "BardErrorInfo [20]" and the
	// JSON-encoded error payload "...application.BardErrorInfo",[1152]" that
	// Gemini returns in the wrb.fr stream when it rejects the request.
	reBardError = regexp.MustCompile(`BardErrorInfo[^\[]*\[(\d+)\]`)
)

// geminiWebBase is the Gemini web frontend base URL. It is a variable so
// tests can point it at a mock upstream; override via GEMINI_WEB_BASE in .env.
var geminiWebBase = "https://gemini.google.com"

var (
	httpClientOnce sync.Once
	httpClient     *http.Client
)

// getHTTPClient returns the shared HTTP client. An explicit proxy from the
// config is used when set; otherwise Go's default transport honors
// HTTP_PROXY/HTTPS_PROXY environment variables (same as Python urllib).
func getHTTPClient() *http.Client {
	httpClientOnce.Do(func() {
		httpClient = &http.Client{}
		if Config.Proxy != "" {
			if proxyURL, err := url.Parse(Config.Proxy); err == nil {
				transport := http.DefaultTransport.(*http.Transport).Clone()
				transport.Proxy = http.ProxyURL(proxyURL)
				httpClient.Transport = transport
			}
		}
	})
	return httpClient
}

// idleTimeoutReader fails a Read if no data arrives within the timeout,
// mirroring urllib/httpx per-operation timeouts.
type idleTimeoutReader struct {
	reader  io.Reader
	timeout time.Duration
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := r.reader.Read(p)
		done <- result{n, err}
	}()
	select {
	case res := <-done:
		return res.n, res.err
	case <-time.After(r.timeout):
		return 0, fmt.Errorf("request timed out after %s without data", r.timeout)
	}
}

func readAllTimeout(reader io.Reader, timeout time.Duration) ([]byte, error) {
	return io.ReadAll(&idleTimeoutReader{reader: reader, timeout: timeout})
}

var (
	cookieCacheMu      sync.Mutex
	cookieCacheStr     = ""
	cookieCacheSAPISID = ""
	cookieCacheMtime   time.Time
	cookieCacheValid   bool
)

// loadCookie loads the cookie from the configured file with mtime-based
// caching. Returns (cookie_string, sapisid_value).
func loadCookie() (string, string) {
	cookieCacheMu.Lock()
	defer cookieCacheMu.Unlock()

	cookieFile := Config.CookieFile
	if cookieFile == "" {
		return "", ""
	}
	info, err := os.Stat(cookieFile)
	if err != nil {
		// File missing/unreadable -> anonymous, like the original.
		return "", ""
	}
	if cookieCacheValid && info.ModTime().Equal(cookieCacheMtime) && cookieCacheStr != "" {
		return cookieCacheStr, cookieCacheSAPISID
	}
	content, err := os.ReadFile(cookieFile)
	if err != nil {
		log("Cookie load error: " + err.Error())
		if cookieCacheValid {
			return cookieCacheStr, cookieCacheSAPISID
		}
		return "", ""
	}
	text := strings.TrimSpace(string(content))
	cookieStr := ""
	sapisid := ""
	if strings.HasPrefix(text, "{") {
		var data struct {
			Cookie  string `json:"cookie"`
			SAPISID string `json:"sapisid"`
		}
		if err := json.Unmarshal([]byte(text), &data); err == nil {
			cookieStr = data.Cookie
			sapisid = data.SAPISID
		}
	} else {
		cookieStr = text
		for _, pair := range strings.Split(cookieStr, "; ") {
			kv := strings.SplitN(pair, "=", 2)
			if len(kv) == 2 && strings.TrimSpace(kv[0]) == "SAPISID" {
				sapisid = kv[1]
			}
		}
	}
	cookieCacheStr = cookieStr
	cookieCacheSAPISID = sapisid
	cookieCacheMtime = info.ModTime()
	cookieCacheValid = true
	return cookieStr, sapisid
}

// makeSapisidhash builds the SAPISIDHASH Authorization header value.
func makeSapisidhash(sapisid string) string {
	ts := time.Now().Unix()
	h := sha1.Sum([]byte(fmt.Sprintf("%d %s https://gemini.google.com", ts, sapisid)))
	return fmt.Sprintf("SAPISIDHASH %d_%s", ts, hex.EncodeToString(h[:]))
}

// accountPrefix returns the Gemini account path prefix for non-default
// Google accounts, e.g. "/u/1".
func accountPrefix() string {
	if Config.AuthUser == "" {
		return ""
	}
	return "/u/" + Config.AuthUser
}

// buildHeaders builds the request headers for the Gemini StreamGenerate call.
func buildHeaders() http.Header {
	prefix := accountPrefix()
	h := http.Header{}
	h.Set("Content-Type", "application/x-www-form-urlencoded")
	h.Set("Origin", "https://gemini.google.com")
	h.Set("Referer", "https://gemini.google.com"+prefix+"/app")
	h.Set("X-Same-Domain", "1")
	h.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
	if prefix != "" {
		h.Set("X-Goog-AuthUser", Config.AuthUser)
	}
	cookieStr, sapisid := loadCookie()
	if cookieStr != "" {
		h.Set("Cookie", cookieStr)
	}
	if sapisid != "" {
		h.Set("Authorization", makeSapisidhash(sapisid))
	}
	return h
}

// buildPayload builds the x-www-form-urlencoded request body for
// StreamGenerate, mirroring the Python _build_payload exactly.
func buildPayload(prompt string, modelID, thinkMode int, fileRefs []string, extraFields map[int]any) string {
	inner := make([]any, 102)
	if fileRefs != nil {
		refs := make([]any, 0, len(fileRefs))
		for _, ref := range fileRefs {
			refs = append(refs, []any{nil, nil, ref})
		}
		inner[0] = []any{prompt, 0, nil, refs, nil, nil, 0}
	} else {
		inner[0] = []any{prompt, 0, nil, nil, nil, nil, 0}
	}
	inner[1] = []any{"en"}
	inner[2] = []any{"", "", "", nil, nil, nil, nil, nil, nil, ""}
	inner[6] = []any{0}
	inner[7] = 1
	inner[10] = 1
	inner[11] = 0
	inner[17] = []any{[]any{thinkMode}}
	inner[18] = 0
	inner[27] = 1
	inner[30] = []any{4}
	inner[41] = []any{2}
	inner[53] = 0
	inner[59] = newUUID()
	inner[61] = []any{}
	inner[68] = 1
	inner[79] = modelID
	for k, v := range extraFields {
		inner[k] = v
	}
	innerJSON, _ := json.Marshal(inner)
	outerJSON, _ := json.Marshal([]any{nil, string(innerJSON)})
	params := url.Values{}
	params.Set("f.req", string(outerJSON))
	if Config.XSRFToken != "" {
		params.Set("at", Config.XSRFToken)
	}
	return params.Encode()
}

// getURL builds the StreamGenerate endpoint URL.
func getURL() string {
	reqid := time.Now().Unix() % 1000000
	return fmt.Sprintf("%s%s/_/BardChatUi/data/assistant.lamda.BardFrontendService/StreamGenerate?bl=%s&hl=en&_reqid=%d&rt=c",
		geminiWebBase, accountPrefix(), Config.GeminiBL, reqid)
}

// cleanText removes Gemini response artifacts, mirroring clean_text().
func cleanText(text string, strip bool) string {
	text = reCleanCodeBlock.ReplaceAllString(text, "")
	text = reCardContent.ReplaceAllString(text, "")
	if strip {
		text = strings.TrimSpace(text)
	}
	return text
}

// extractTextsFromLine parses a single wrb.fr response line and returns the
// text strings found in it. Line format (nested): [["wrb.fr","id","<inner>",...],...]
func extractTextsFromLine(line string) []string {
	if !strings.Contains(line, `"wrb.fr"`) || len(line) < 200 {
		return nil
	}
	var arr []any
	if err := json.Unmarshal([]byte(line), &arr); err != nil {
		return nil
	}
	if len(arr) < 1 {
		return nil
	}
	outer, ok := arr[0].([]any)
	if !ok || len(outer) < 3 {
		return nil
	}
	innerStr, ok := outer[2].(string)
	if !ok || innerStr == "" || len(innerStr) < 50 {
		return nil
	}
	var inner []any
	if err := json.Unmarshal([]byte(innerStr), &inner); err != nil {
		return nil
	}
	if len(inner) <= 4 {
		return nil
	}
	parts, ok := inner[4].([]any)
	if !ok || len(parts) == 0 {
		return nil
	}
	var texts []string
	for _, part := range parts {
		p, ok := part.([]any)
		if !ok || len(p) <= 1 {
			continue
		}
		ts, ok := p[1].([]any)
		if !ok {
			continue
		}
		for _, t := range ts {
			if s, ok := t.(string); ok && s != "" {
				texts = append(texts, s)
			}
		}
	}
	return texts
}

// extractResponseText parses a full raw response and returns the final text.
func extractResponseText(raw string) (string, error) {
	if m := reBardError.FindStringSubmatch(raw); m != nil {
		return "", fmt.Errorf("Gemini upstream rejected request: BardErrorInfo [%s]", m[1])
	}
	lastText := ""
	for _, line := range strings.Split(raw, "\n") {
		for _, t := range extractTextsFromLine(line) {
			if len(t) > len(lastText) {
				lastText = t
			}
		}
	}
	return cleanText(lastText, true), nil
}

// generate performs a non-streaming generation with automatic retries.
func generate(prompt string, modelID, thinkMode int, fileRefs []string, extraFields map[int]any) (string, error) {
	body := buildPayload(prompt, modelID, thinkMode, fileRefs, extraFields)
	urlStr := getURL()
	headers := buildHeaders()
	client := getHTTPClient()
	timeout := time.Duration(Config.RequestTimeoutSec) * time.Second

	var lastErr error
	for attempt := 0; attempt < Config.RetryAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(body))
		if err != nil {
			return "", err
		}
		req.Header = headers
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < Config.RetryAttempts-1 {
				log(fmt.Sprintf("Retry %d/%d: %v", attempt+1, Config.RetryAttempts, err))
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		if resp.StatusCode >= 400 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
			if attempt < Config.RetryAttempts-1 {
				log(fmt.Sprintf("Retry %d/%d: %v", attempt+1, Config.RetryAttempts, lastErr))
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		raw, rerr := readAllTimeout(resp.Body, timeout)
		resp.Body.Close()
		if rerr != nil {
			lastErr = rerr
			if attempt < Config.RetryAttempts-1 {
				log(fmt.Sprintf("Retry %d/%d: %v", attempt+1, Config.RetryAttempts, rerr))
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		text, xerr := extractResponseText(string(raw))
		if xerr != nil {
			lastErr = xerr
			if attempt < Config.RetryAttempts-1 {
				log(fmt.Sprintf("Retry %d/%d: %v", attempt+1, Config.RetryAttempts, xerr))
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		if text == "" {
			// Upstream returned 200 but no extractable text. Never return a
			// silent empty string (which became content:null for clients) —
			// log a raw snippet for diagnosis and retry; if every attempt
			// fails the caller surfaces a clear upstream error.
			lastErr = fmt.Errorf("Gemini upstream returned an empty response (prompt_len=%d)", len(prompt))
			snippet := string(raw)
			if len(snippet) > 1500 {
				snippet = snippet[:1500]
			}
			log(fmt.Sprintf("WARNING: %v. Raw head: %s", lastErr, snippet))
			if Config.DumpRaw {
				if err := os.WriteFile("/tmp/gemini-raw-empty.txt", raw, 0o600); err != nil {
					log("WARNING: failed to dump raw: " + err.Error())
				}
			}
			if attempt < Config.RetryAttempts-1 {
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		return text, nil
	}
	return "", lastErr
}

// handleLine processes one wrb.fr line during streaming, emitting only the
// delta not yet emitted (so retries can resume seamlessly).
func handleLine(line string, emittedRaw *string, onDelta func(string)) error {
	for _, t := range extractTextsFromLine(line) {
		if t == *emittedRaw || strings.HasPrefix(*emittedRaw, t) {
			continue
		}
		if !strings.HasPrefix(t, *emittedRaw) {
			return fmt.Errorf("Gemini stream content changed during retry")
		}
		delta := cleanText(t[len(*emittedRaw):], false)
		*emittedRaw = t
		if delta != "" {
			onDelta(delta)
		}
	}
	return nil
}

// processStream reads the response body line by line, yielding text deltas.
func processStream(reader io.Reader, emittedRaw *string, onDelta func(string)) error {
	br := bufio.NewReaderSize(reader, 64*1024)
	var pending []byte
	for {
		chunk := make([]byte, 4096)
		n, err := br.Read(chunk)
		if n > 0 {
			pending = append(pending, chunk[:n]...)
			if m := reBardError.FindSubmatch(pending); m != nil {
				return fmt.Errorf("Gemini upstream rejected request: BardErrorInfo [%s]", m[1])
			}
			for {
				idx := bytes.IndexByte(pending, '\n')
				if idx < 0 {
					break
				}
				line := pending[:idx]
				pending = pending[idx+1:]
				if err := handleLine(string(line), emittedRaw, onDelta); err != nil {
					return err
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if len(pending) > 0 {
		if m := reBardError.FindSubmatch(pending); m != nil {
			return fmt.Errorf("Gemini upstream rejected request: BardErrorInfo [%s]", m[1])
		}
		if err := handleLine(string(pending), emittedRaw, onDelta); err != nil {
			return err
		}
	}
	return nil
}

// generateStream performs streaming generation with retries, invoking
// onDelta for every text delta.
func generateStream(prompt string, modelID, thinkMode int, fileRefs []string, extraFields map[int]any, onDelta func(string)) error {
	body := buildPayload(prompt, modelID, thinkMode, fileRefs, extraFields)
	urlStr := getURL()
	headers := buildHeaders()
	client := getHTTPClient()
	timeout := time.Duration(Config.RequestTimeoutSec) * time.Second

	var lastErr error
	emittedRaw := ""
	for attempt := 0; attempt < Config.RetryAttempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, urlStr, strings.NewReader(body))
		if err != nil {
			return err
		}
		req.Header = headers
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			if attempt < Config.RetryAttempts-1 {
				log(fmt.Sprintf("Stream retry %d/%d: %v", attempt+1, Config.RetryAttempts, err))
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			lastErr = fmt.Errorf("upstream HTTP %d", resp.StatusCode)
			if attempt < Config.RetryAttempts-1 {
				log(fmt.Sprintf("Stream retry %d/%d: %v", attempt+1, Config.RetryAttempts, lastErr))
				time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
			}
			continue
		}
		err = processStream(&idleTimeoutReader{reader: resp.Body, timeout: timeout}, &emittedRaw, onDelta)
		resp.Body.Close()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < Config.RetryAttempts-1 {
			log(fmt.Sprintf("Stream retry %d/%d: %v", attempt+1, Config.RetryAttempts, err))
			time.Sleep(time.Duration(Config.RetryDelaySec) * time.Second)
		}
	}
	return lastErr
}

//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestMain builds the real binary once, then runs all e2e tests against it.
var binPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "gemini-web2api-e2e")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(tmp, "gemini-web2api")

	// The test binary's working directory is the package dir (e2e/), so the
	// module root is one level up.
	cmd := exec.Command("go", "build", "-o", binPath, "./src")
	cmd.Dir = ".."
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
		os.Exit(1)
	}
	// NOTE: os.Exit does not run deferred functions, so the temp dir must be
	// removed explicitly.
	code := m.Run()
	_ = os.RemoveAll(tmp)
	os.Exit(code)
}

// cleanEnv returns the current environment with all GEMINI_* variables
// removed, so a developer's shell cannot leak config into the subprocess.
func cleanEnv(extra ...string) []string {
	var out []string
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "GEMINI_") {
			continue
		}
		out = append(out, e)
	}
	return append(out, extra...)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type serverProc struct {
	cmd  *exec.Cmd
	port int
	url  string
}

// startServer writes a temp .env (GEMINI_PORT + given vars), starts the real
// binary with --env, and waits until GET / responds 200.
func startServer(t *testing.T, env map[string]string) *serverProc {
	t.Helper()
	port := freePort(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	var sb strings.Builder
	sb.WriteString("GEMINI_PORT=" + strconv.Itoa(port) + "\n")
	for k, v := range env {
		sb.WriteString(k + "=" + v + "\n")
	}
	if err := os.WriteFile(envFile, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "--env", envFile)
	cmd.Env = cleanEnv("HOME=" + dir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	proc := &serverProc{cmd: cmd, port: port, url: fmt.Sprintf("http://127.0.0.1:%d", port)}
	proc.waitReady(t)
	return proc
}

func (p *serverProc) waitReady(t *testing.T) {
	t.Helper()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := client.Get(p.url + "/")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server on %s did not become ready", p.url)
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

// mustJSONString mirrors the src helper: JSON-encode a Go string.
func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// wrbLine builds a synthetic Gemini StreamGenerate response line (>= 200
// chars, as the parser requires) containing the given text.
func wrbLine(t *testing.T, text string) string {
	t.Helper()
	parts := `[[null,[` + mustJSONString(t, text) + `]]]`
	inner := `[null,[null,null,null],null,"en",` + parts + `,null,null,"en",null,[1,2,3]]`
	line := `[["wrb.fr","dS5raY5Tk1C4",` + mustJSONString(t, inner) + `,null,null,null,0,1],null,null]`
	for len(line) < 200 {
		line = line[:len(line)-1] + ",null]"
	}
	return line
}

// mockChatUpstream is an httptest server the binary reaches via GEMINI_WEB_BASE.
func mockChatUpstream(t *testing.T, text string) *httptest.Server {
	t.Helper()
	body := ")]}'\n\n" + wrbLine(t, text) + "\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestE2ERootAndModels(t *testing.T) {
	srv := startServer(t, map[string]string{})

	resp, data := doJSON(t, http.MethodGet, srv.url+"/", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil || root["status"] != "ok" {
		t.Fatalf("bad root: %s", data)
	}

	resp, data = doJSON(t, http.MethodGet, srv.url+"/v1/models", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "gemini-3.6-flash") {
		t.Fatalf("models missing gemini-3.6-flash: %s", data)
	}
}

func TestE2EAuth(t *testing.T) {
	srv := startServer(t, map[string]string{"GEMINI_API_KEY": "sk-e2e"})

	if resp, _ := doJSON(t, http.MethodGet, srv.url+"/v1/models", "", nil); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 without key, got %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodGet, srv.url+"/v1/models", "", map[string]string{"Authorization": "Bearer sk-e2e"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with Bearer, got %d", resp.StatusCode)
	}
	if resp, _ := doJSON(t, http.MethodGet, srv.url+"/v1/models", "", map[string]string{"x-api-key": "sk-e2e"}); resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 with x-api-key, got %d", resp.StatusCode)
	}
}

func TestE2EChatCompletion(t *testing.T) {
	up := mockChatUpstream(t, "Hello from e2e")
	srv := startServer(t, map[string]string{"GEMINI_WEB_BASE": up.URL})

	payload := `{"model":"gemini-3.5-flash","messages":[{"role":"user","content":"hi"}]}`
	resp, data := doJSON(t, http.MethodPost, srv.url+"/v1/chat/completions", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	if !strings.Contains(string(data), "Hello from e2e") {
		t.Fatalf("expected mock content in reply: %s", data)
	}
}

func TestE2EEmbeddings(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "AIza-e2e" {
			t.Errorf("missing x-goog-api-key")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"embeddings":[{"values":[0.1,0.2]},{"values":[0.3,0.4]}],"usageMetadata":{"promptTokenCount":7}}`)
	}))
	t.Cleanup(up.Close)

	srv := startServer(t, map[string]string{
		"GEMINI_EMBEDDINGS_API_BASE": up.URL,
		"GEMINI_EMBEDDINGS_API_KEY":  "AIza-e2e",
	})
	payload := `{"model":"text-embedding-3-small","input":["a","b"]}`
	resp, data := doJSON(t, http.MethodPost, srv.url+"/v1/embeddings", payload, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, data)
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data) != 2 || out.Data[1].Embedding[0] != 0.3 {
		t.Fatalf("unexpected embeddings: %s", data)
	}
}

// TestE2ERequireAuthFailClosed verifies the binary refuses to start when
// GEMINI_REQUIRE_AUTH=true and no API keys are configured (fail-closed).
func TestE2ERequireAuthFailClosed(t *testing.T) {
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("GEMINI_REQUIRE_AUTH=true\nGEMINI_PORT="+strconv.Itoa(freePort(t))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binPath, "--env", envFile)
	cmd.Env = cleanEnv("HOME=" + dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit, got: %s", out)
	}
	if !strings.Contains(string(out), "refusing to start") {
		t.Fatalf("expected 'refusing to start' message, got: %s", out)
	}
}

func TestE2EVersion(t *testing.T) {
	cmd := exec.Command(binPath, "--version")
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("--version failed: %v", err)
	}
	if !strings.Contains(string(out), "1.1.0") {
		t.Fatalf("unexpected version output: %s", out)
	}
}

// TestE2EHealthCheck runs the --check health probe: exit 0 when the server is
// up, exit 1 when the port is closed.
func TestE2EHealthCheck(t *testing.T) {
	srv := startServer(t, map[string]string{})
	envFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(envFile, []byte("GEMINI_PORT="+strconv.Itoa(srv.port)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binPath, "--check", "--env", envFile)
	cmd.Env = cleanEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("--check against running server should exit 0: %v\n%s", err, out)
	}

	closed := freePort(t)
	closedFile := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(closedFile, []byte("GEMINI_PORT="+strconv.Itoa(closed)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(binPath, "--check", "--env", closedFile)
	cmd.Env = cleanEnv()
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("--check against closed port should exit non-zero, got: %s", out)
	}
}

// TestE2EEnvPrecedence verifies real process env wins over the .env file:
// GEMINI_PORT set in the environment overrides the value in .env.
func TestE2EEnvPrecedence(t *testing.T) {
	port := freePort(t)
	dir := t.TempDir()
	envFile := filepath.Join(dir, ".env")
	if err := os.WriteFile(envFile, []byte("GEMINI_PORT="+strconv.Itoa(port)+"\nGEMINI_HOST=127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Use a *different* port via the real environment.
	realPort := freePort(t)

	cmd := exec.Command(binPath, "--env", envFile)
	cmd.Env = cleanEnv("HOME="+dir, "GEMINI_PORT="+strconv.Itoa(realPort))
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})

	client := &http.Client{Timeout: 500 * time.Millisecond}
	url := fmt.Sprintf("http://127.0.0.1:%d/", realPort)
	deadline := time.Now().Add(10 * time.Second)
	ok := false
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ok = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ok {
		t.Fatalf("server should listen on real-env port %d, not .env port %d", realPort, port)
	}
}

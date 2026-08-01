package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

// Version matches the original Python project version.
const Version = "1.1.0"

func main() {
	flagPort := flag.Int("port", 0, "listen port (overrides env)")
	flagEnv := flag.String("env", "", "path to .env file (defaults to ./.env)")
	flagCookie := flag.String("cookie-file", "", "path to cookie file (overrides env)")
	flagProxy := flag.String("proxy", "", "HTTP proxy URL, e.g. http://127.0.0.1:7890 (overrides env)")
	flagVersion := flag.Bool("version", false, "print version and exit")
	flagCheck := flag.Bool("check", false, "health check: GET / on the configured port, exit 0 on success")
	flagRequireAuth := flag.Bool("require-auth", false, "refuse to start when no API keys are configured (public deployments)")
	flag.Parse()

	if *flagVersion {
		fmt.Printf("gemini-web2api %s\n", Version)
		return
	}

	// .env is the single source of truth; CLI flags below override it.
	envPath := *flagEnv
	if envPath == "" {
		envPath = findEnvFile()
	}
	dotenv := loadDotenv(envPath)
	applyEnvConfig(dotenv)

	if *flagPort != 0 {
		Config.Port = *flagPort
	}
	if *flagCookie != "" {
		Config.CookieFile = *flagCookie
	}
	if *flagProxy != "" {
		Config.Proxy = *flagProxy
	}
	if *flagRequireAuth {
		Config.RequireAuth = true
	}

	// Docker HEALTHCHECK mode: probe the local HTTP server and exit 0/1.
	if *flagCheck {
		if err := runHealthCheck(Config.Port); err != nil {
			fmt.Fprintf(os.Stderr, "Health check failed: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// Fail-safe for public deployments: never serve an unauthenticated API
	// when the user explicitly asked for auth to be required.
	if err := requireAuthErr(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	fmt.Printf("gemini-web2api v%s\n", Version)
	if envPath == "" {
		envPath = "(none — defaults)"
	}
	fmt.Printf("  Env file:  %s\n", envPath)
	fmt.Printf("  Listening: http://%s:%d\n", Config.Host, Config.Port)
	fmt.Printf("  Base URL:  http://localhost:%d/v1\n", Config.Port)
	fmt.Printf("  Models:    %s\n", strings.Join(modelNames(), ", "))
	cookie := "none (anonymous)"
	if Config.CookieFile != "" {
		cookie = "yes"
	}
	fmt.Printf("  Cookie:    %s\n", cookie)
	proxy := Config.Proxy
	if proxy == "" {
		proxy = "system env"
	}
	fmt.Printf("  Proxy:     %s\n", proxy)
	if len(Config.APIKeys) == 0 {
		fmt.Println("  Auth:      DISABLED — API is open to everyone!")
	} else {
		fmt.Printf("  Auth:      enabled (%d key(s))\n", len(Config.APIKeys))
	}
	if Config.EmbeddingsAPIKey == "" {
		fmt.Println("  Embeddings: disabled (set GEMINI_EMBEDDINGS_API_KEY)")
	} else {
		m := Config.EmbeddingsModel
		if m == "" {
			m = defaultEmbeddingsModel
		}
		fmt.Printf("  Embeddings: enabled (%s)\n", m)
	}

	srv := &http.Server{Addr: fmt.Sprintf("%s:%d", Config.Host, Config.Port), Handler: &Server{}}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		fmt.Println("\nStopped.")
		os.Exit(0)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

// requireAuthErr returns an error when RequireAuth is enabled but no API keys
// are configured — a fail-safe against accidentally exposing an open API to
// the internet.
func requireAuthErr() error {
	if Config.RequireAuth && len(Config.APIKeys) == 0 {
		return fmt.Errorf("require_auth is enabled but no API keys are configured; refusing to start. Set GEMINI_API_KEY or GEMINI_API_KEYS")
	}
	return nil
}

// runHealthCheck performs an HTTP GET against the local / endpoint; used by
// the Docker HEALTHCHECK (works in scratch images — no shell required).
func runHealthCheck(port int) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/", port))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	return nil
}

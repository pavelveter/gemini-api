// Package e2e contains end-to-end tests that build the real binary and run it
// as a subprocess with a temporary .env, exercising the API over real HTTP
// against local mock upstreams (GEMINI_WEB_BASE / GEMINI_EMBEDDINGS_API_BASE).
//
// Run with: go test -tags e2e ./e2e/...
package e2e

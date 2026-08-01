package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRunHealthCheckOK(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if err := runHealthCheck(port); err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
}

func TestRunHealthCheckNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if err := runHealthCheck(port); err == nil {
		t.Fatal("expected error for non-200 status")
	}
}

func TestRunHealthCheckUnreachable(t *testing.T) {
	if err := runHealthCheck(1); err == nil {
		t.Fatal("expected error for unreachable port")
	}
}

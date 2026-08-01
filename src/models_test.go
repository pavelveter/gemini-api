package main

import "testing"

func TestResolveModelDefault(t *testing.T) {
	rm, err := resolveModel("gemini-3.5-flash", "gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	if rm.Name != "gemini-3.5-flash" || rm.ModeID != 1 || rm.ThinkMode != 4 {
		t.Fatalf("unexpected: %+v", rm)
	}
}

func TestResolveModelThinking(t *testing.T) {
	rm, err := resolveModel("gemini-3.5-flash-thinking", "gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	if rm.ModeID != 2 || rm.ThinkMode != 0 {
		t.Fatalf("unexpected: %+v", rm)
	}
}

func TestResolveModelThinkOverride(t *testing.T) {
	rm, err := resolveModel("gemini-3.5-flash-thinking@think=2", "gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	if rm.ThinkMode != 2 {
		t.Fatalf("unexpected: %+v", rm)
	}
}

func TestResolveModelInvalidThink(t *testing.T) {
	if _, err := resolveModel("gemini-3.5-flash@think=abc", "gemini-3.6-flash"); err == nil {
		t.Fatal("expected error for invalid think level")
	}
}

func TestResolveModelFallback(t *testing.T) {
	rm, err := resolveModel("does-not-exist", "gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	if rm.Name != "gemini-3.6-flash" {
		t.Fatalf("expected fallback, got %s", rm.Name)
	}
}

func TestResolveModelExtra(t *testing.T) {
	rm, err := resolveModel("gemini-3.1-pro-enhanced", "gemini-3.6-flash")
	if err != nil {
		t.Fatal(err)
	}
	if rm.Extra[31] != 2 || rm.Extra[80] != 3 {
		t.Fatalf("unexpected extra: %+v", rm.Extra)
	}
}

package catalog

import (
	"testing"

	"opencode-go-cliproxyapi/internal/config"
)

func TestGeminiRoutesAndProtocolGate(t *testing.T) {
	cfg, err := config.Load([]byte("api-keys: [{value: test}]\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(cfg, nil)
	route, endpoint, ok, _ := m.resolve(rawModel{ID: "gemini-4-argon"})
	if !ok || route != RouteGemini || (endpoint != "" && endpoint != RouteGemini.EndpointPath()) || !m.protocolEnabled(route) {
		t.Fatalf("Gemini route missing: %s %s %v", route, endpoint, ok)
	}
	cfg.Protocols.Gemini = false
	if New(cfg, nil).protocolEnabled(route) {
		t.Fatal("Gemini protocol gate ignored")
	}
}

func TestGeminiOverrideCannotEscapeConfiguredHost(t *testing.T) {
	cfg, err := config.Load([]byte("api-keys: [{value: test}]\ngemini-base-url: https://trusted.example\nroute-overrides:\n  gemini-4-argon: {protocol: gemini, endpoint: '/v1@evil.example/models/{model}:generateContent'}\n"))
	if err != nil {
		t.Fatal(err)
	}
	m := New(cfg, nil)
	m.swap([]rawModel{{ID: "gemini-4-argon"}})
	if len(m.Models()) != 0 || len(m.Unsupported()) != 1 {
		t.Fatal("escaping endpoint must not receive upstream credentials")
	}
}

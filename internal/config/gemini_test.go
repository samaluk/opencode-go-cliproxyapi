package config

import (
	"strings"
	"testing"
)

func TestGeminiConfiguration(t *testing.T) {
	cfg, err := Load([]byte("api-keys: [{value: test}]\n"))
	if err != nil || cfg.GeminiBaseURL != DefaultGeminiBaseURL || !cfg.Protocols.Gemini {
		t.Fatalf("bad defaults: %v", err)
	}
	custom, err := Load([]byte("api-keys: [{value: test}]\nbase-url: https://private.example/prefix/v1\n"))
	if err != nil || custom.GeminiBaseURL != "https://private.example/prefix/v1beta" {
		t.Fatalf("custom base escaped its host: %v", err)
	}
	trailing, err := Load([]byte("api-keys: [{value: test}]\nbase-url: " + DefaultBaseURL + "/\n"))
	if err != nil || trailing.GeminiBaseURL != DefaultGeminiBaseURL {
		t.Fatalf("trailing slash changed native provider: %v", err)
	}
	for _, fragment := range []string{
		"gemini-base-url: http://example.test/v1beta\n",
		"gemini-base-url: https://user:secret@example.test/v1beta\n",
		"route-overrides: {gemini-test: {protocol: gemini, endpoint: /models/constant:generateContent}}\n",
	} {
		if _, err := Load([]byte("api-keys: [{value: test}]\n" + fragment)); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("invalid config accepted or leaked: %v", err)
		}
	}
}

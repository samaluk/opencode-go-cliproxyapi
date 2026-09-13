package adapters

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"opencode-go-cliproxyapi/internal/adapter/chatcompletions"
	"opencode-go-cliproxyapi/internal/adapter/responses"
	"opencode-go-cliproxyapi/internal/errclass"
)

func TestClaudeExplicitEffortAcrossTargets(t *testing.T) {
	for _, target := range []struct {
		name  string
		build func(string, string, []byte, *pluginapi.ThinkingSupport) ([]byte, *errclass.Error)
	}{
		{"chat", chatcompletions.BuildRequest},
		{"responses", responses.BuildRequest},
	} {
		for _, control := range []string{`"thinking":{"type":"adaptive"},`, `"thinking":{"type":"enabled","budget_tokens":1024},`, ""} {
			for _, effort := range []string{"low", "high", "medium"} {
				t.Run(target.name+"/"+control+effort, func(t *testing.T) {
					body := []byte(fmt.Sprintf(`{"model":"lab","max_tokens":123,%s"output_config":{"effort":%q},"messages":[{"role":"user","content":"hello"}]}`, control, effort))
					out, err := target.build("lab", "claude", body, &pluginapi.ThinkingSupport{Levels: []string{"low", "high"}})
					if effort == "medium" {
						if err == nil || err.Class != errclass.ClassUnsupported {
							t.Fatalf("unsupported explicit effort must fail, got %s, %v", out, err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					var result map[string]any
					if err := json.Unmarshal(out, &result); err != nil {
						t.Fatal(err)
					}
					actual := result["reasoning_effort"]
					if target.name == "responses" {
						if reasoning, ok := result["reasoning"].(map[string]any); ok {
							actual = reasoning["effort"]
						}
					}
					if actual != effort {
						t.Fatalf("explicit effort lost or rewritten: got %v, want %s; %s", actual, effort, out)
					}
				})
			}
		}
	}
}

func TestClaudeExplicitEffortUsesResponsesWirePolicy(t *testing.T) {
	for _, test := range []struct {
		name, effort, want string
		thinking           pluginapi.ThinkingSupport
	}{
		{"dynamic auto", " AUTO ", "", pluginapi.ThinkingSupport{DynamicAllowed: true, Levels: []string{"low", "high"}}},
		{"off without named none", "none", "", pluginapi.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "high"}}},
		{"declared none", "none", "none", pluginapi.ThinkingSupport{Levels: []string{"none", "high"}}},
		{"declared effort", " HIGH ", "high", pluginapi.ThinkingSupport{Levels: []string{"low", "high"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := []byte(fmt.Sprintf(`{"messages":[{"role":"user","content":"hello"}],"output_config":{"effort":%q}}`, test.effort))
			out, err := responses.BuildRequest("lab", "claude", body, &test.thinking)
			if err != nil {
				t.Fatal(err)
			}
			var result struct {
				Reasoning *struct{ Effort string }
			}
			if err := json.Unmarshal(out, &result); err != nil {
				t.Fatal(err)
			}
			if test.want == "" {
				if result.Reasoning != nil {
					t.Fatalf("unsupported wire sentinel was forwarded: %s", out)
				}
			} else if result.Reasoning == nil || result.Reasoning.Effort != test.want {
				t.Fatalf("declared effort lost: %s", out)
			}
		})
	}
}

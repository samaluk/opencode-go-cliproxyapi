package adapters

import (
	"encoding/json"
	"testing"

	"opencode-go-cliproxyapi/internal/adapter/chatcompletions"
	"opencode-go-cliproxyapi/internal/adapter/messages"
	"opencode-go-cliproxyapi/internal/adapter/responses"
)

func TestPiEasyInputMessage(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_text","text":"hello"}]}]}`)
	if _, err := chatcompletions.BuildRequest("lab", "openai-response", body, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := messages.BuildRequest("lab", "openai-response", body, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := chatcompletions.BuildRequest("lab", "openai-response", []byte(`{"input":[{}]}`), nil); err == nil {
		t.Fatal("empty item must still fail")
	}
}

func TestClaudeEnvironmentMessagePreservesPosition(t *testing.T) {
	body := []byte(`{"system":"initial","messages":[{"role":"user","content":"hello"},{"role":"system","content":[{"type":"text","text":"environment"}]}]}`)
	out, err := chatcompletions.BuildRequest("lab", "claude", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	var cc struct {
		Messages []struct{ Role, Content string }
	}
	if err := json.Unmarshal(out, &cc); err != nil {
		t.Fatal(err)
	}
	if len(cc.Messages) != 3 || cc.Messages[2].Role != "system" || cc.Messages[2].Content != "environment" {
		t.Fatalf("%s", out)
	}
	out, err = responses.BuildRequest("lab", "claude", body, nil)
	if err != nil {
		t.Fatal(err)
	}
	var resp struct{ Input []struct{ Role string } }
	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Input) != 2 || resp.Input[1].Role != "system" {
		t.Fatalf("%s", out)
	}
	bad := []byte(`{"messages":[{"role":"system","content":[{"type":"image","source":{"type":"url","url":"https://example.com/a.png"}}]}]}`)
	if _, err := chatcompletions.BuildRequest("lab", "claude", bad, nil); err == nil {
		t.Fatal("system images cannot silently disappear")
	}
}

package gemini

import (
	"bytes"
	"encoding/json"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"strconv"
	"strings"
	"testing"

	"opencode-go-cliproxyapi/internal/adapter/shared"
)

const nativeText = `{"responseId":"g1","modelVersion":"gemini-4-argon","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"thoughtsTokenCount":3,"totalTokenCount":15}}`

func TestBuildRequestClientFormats(t *testing.T) {
	for format, body := range map[string]string{
		"openai":          `{"messages":[{"role":"system","content":"Be helpful."},{"role":"user","content":"Hi"}],"max_completion_tokens":32,"temperature":0.2,"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object"}}}],"tool_choice":{"type":"function","function":{"name":"weather"}}}`,
		"claude":          `{"system":"Be helpful.","messages":[{"role":"user","content":"Hi"}],"max_tokens":32,"temperature":0.2,"tools":[{"name":"weather","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"weather"}}`,
		"openai-response": `{"instructions":"Be helpful.","input":"Hi","max_output_tokens":32,"temperature":0.2,"tools":[{"type":"function","name":"weather","parameters":{"type":"object"}}],"tool_choice":{"type":"function","name":"weather"}}`,
	} {
		t.Run(format, func(t *testing.T) {
			a := NewAdapter("gemini-4-argon", format, shared.NewResponseTools())
			out, err := a.BuildRequest([]byte(body), nil)
			if err != nil {
				t.Fatal(err)
			}
			var native map[string]any
			if json.Unmarshal(out, &native) != nil {
				t.Fatal("invalid JSON")
			}
			if _, present := native["model"]; present {
				t.Fatal("model belongs in URL")
			}
			if len(native["contents"].([]any)) == 0 || len(native["tools"].([]any)) == 0 {
				t.Fatalf("missing content or tools: %s", out)
			}
			generation := native["generationConfig"].(map[string]any)
			if generation["maxOutputTokens"] != float64(32) || generation["temperature"] != 0.2 {
				t.Fatalf("lost controls: %s", out)
			}
			if !bytes.Contains(out, []byte("weather")) || !bytes.Contains(out, []byte("Be helpful.")) {
				t.Fatalf("lost system or tool: %s", out)
			}
		})
	}
}

func TestBuildRequestPreservesToolResultAndImage(t *testing.T) {
	a := NewAdapter("gemini-4-argon", "openai", shared.NewResponseTools())
	body := `{"messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Paris\"}"},"extra_content":{"google":{"thought_signature":"EgUKAwEMOQ=="}}}]},{"role":"tool","tool_call_id":"call1","content":"Sunny"},{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,aGVsbG8="}}]}]}`
	out, err := a.BuildRequest([]byte(body), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"functionCall", "functionResponse", "EgUKAwEMOQ==", "Sunny", "inlineData", "aGVsbG8="} {
		if !bytes.Contains(out, []byte(value)) {
			t.Errorf("missing %s: %s", value, out)
		}
	}
}

func TestExplicitClaudeReasoningEffort(t *testing.T) {
	support := &pluginapi.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
	a := NewAdapter("gemini-4-argon", "claude", shared.NewResponseTools())
	out, err := a.BuildRequest([]byte(`{"messages":[{"role":"user","content":"Hi"}],"max_tokens":32,"output_config":{"effort":"high"}}`), support)
	if err != nil || !bytes.Contains(out, []byte(`"thinkingLevel":"high"`)) {
		t.Fatalf("effort not preserved: %v %s", err, out)
	}
	if _, err := a.BuildRequest([]byte(`{"messages":[{"role":"user","content":"Hi"}],"max_tokens":32,"output_config":{"effort":"xhigh"}}`), support); err == nil {
		t.Fatal("unsupported effort must not be ignored")
	}
}

func TestEndpointEscapesModelAndSelectsStreamingMethod(t *testing.T) {
	base := "/models/{model}:generateContent"
	if got := Endpoint(base, "gemini-4-argon", false); got != "/models/gemini-4-argon:generateContent" {
		t.Fatal(got)
	}
	if got := Endpoint(base, "model/with?query", true); got != "/models/model%2Fwith%3Fquery:streamGenerateContent?alt=sse" {
		t.Fatal(got)
	}
}

func TestConvertNonStreamClientFormats(t *testing.T) {
	for _, format := range []string{"openai", "claude", "openai-response"} {
		t.Run(format, func(t *testing.T) {
			a := NewAdapter("gemini-4-argon", format, shared.NewResponseTools())
			out, err := a.ConvertNonStreamResponse(200, []byte(nativeText))
			if err != nil {
				t.Fatal(err)
			}
			if !json.Valid(out) || !bytes.Contains(out, []byte("OK")) {
				t.Fatalf("invalid result: %s", out)
			}
			var d map[string]json.RawMessage
			_ = json.Unmarshal(out, &d)
			if len(d["usage"]) == 0 {
				t.Fatalf("missing usage: %s", out)
			}
		})
	}
}

func TestStreamAllClientFormatsAndChunkBoundaries(t *testing.T) {
	stream := ": heartbeat\r\ndata: {\"responseId\":\"g1\",\"modelVersion\":\"gemini-4-argon\",\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"OK\"}]}}]}\r\n\r\n" +
		"data: {\"candidates\":[{\"index\":0,\"finishReason\":\"STOP\"}]}\n\n" +
		"data: {\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":2,\"thoughtsTokenCount\":3,\"totalTokenCount\":15}}\n\n"
	for _, format := range []string{"openai", "claude", "openai-response"} {
		for _, size := range []int{1, 7, len(stream)} {
			t.Run(format+"/chunk="+strconv.Itoa(size), func(t *testing.T) {
				a := NewAdapter("gemini-4-argon", format, shared.NewResponseTools())
				var result []byte
				for offset := 0; offset < len(stream); offset += size {
					end := min(offset+size, len(stream))
					events, done, err := a.Feed([]byte(stream[offset:end]))
					if err != nil || done {
						t.Fatalf("premature end/error: %v", err)
					}
					if format == "openai" {
						for _, event := range events {
							var d map[string]any
							_ = json.Unmarshal(event, &d)
							if d["model"] != "gemini-4-argon" || d["id"] != "g1" {
								t.Fatalf("stream identity changed: %s", event)
							}
						}
					}
					result = append(result, bytes.Join(events, nil)...)
				}
				events, err := a.Finish()
				if err != nil {
					t.Fatal(err)
				}
				result = append(result, bytes.Join(events, nil)...)
				terminal := map[string]string{"openai": `"finish_reason":"stop"`, "claude": "message_stop", "openai-response": "response.completed"}[format]
				if format != "openai" {
					terminal = "event: " + terminal + "\n"
				}
				if !bytes.Contains(result, []byte("OK")) || bytes.Count(result, []byte(terminal)) != 1 {
					t.Fatalf("missing content or terminal: %s", result)
				}
				if !bytes.Contains(result, []byte("10")) || !bytes.Contains(result, []byte("5")) {
					t.Fatalf("lost final usage: %s", result)
				}
				if out, err := a.Finish(); err != nil || len(out) != 0 {
					t.Fatal("Finish must be idempotent")
				}
			})
		}
	}
}

func TestMalformedBlockedAndTruncatedResponsesFail(t *testing.T) {
	for _, body := range []string{"null", "{", `{"error":{"message":"secret-provider-body"}}`, `{"promptFeedback":{"blockReason":"SAFETY"}}`, `{"candidates":[{"finishReason":"SAFETY"}]}`, `{}`} {
		a := NewAdapter("gemini-4-argon", "openai-response", shared.NewResponseTools())
		if out, err := a.ConvertNonStreamResponse(200, []byte(body)); err == nil || out != nil || strings.Contains(err.Message, "secret-provider-body") {
			t.Fatalf("not rejected/redacted: %s", body)
		}
	}
	a := NewAdapter("gemini-4-argon", "claude", shared.NewResponseTools())
	_, _, err := a.Feed([]byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := a.Finish(); err == nil || out != nil {
		t.Fatal("truncation must not synthesize success")
	}
}

func TestStreamToolCallsAllClientFormats(t *testing.T) {
	frame := `{"responseId":"g-tool","modelVersion":"gemini-4-argon","candidates":[{"index":0,"content":{"role":"model","parts":[{"functionCall":{"name":"add","args":{"a":2,"b":3}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`
	for _, format := range []string{"openai", "claude", "openai-response"} {
		t.Run(format, func(t *testing.T) {
			a := NewAdapter("gemini-4-argon", format, shared.NewResponseTools())
			events, _, err := a.Feed([]byte("data: " + frame + "\n\n"))
			if err != nil {
				t.Fatal(err)
			}
			final, err := a.Finish()
			if err != nil {
				t.Fatal(err)
			}
			output := bytes.Join(append(events, final...), nil)
			kind := map[string]string{"openai": "tool_calls", "claude": "tool_use", "openai-response": "function_call"}[format]
			if !bytes.Contains(output, []byte(kind)) || !bytes.Contains(output, []byte("add")) || !bytes.Contains(output, []byte("2")) || !bytes.Contains(output, []byte("3")) {
				t.Fatalf("lost streaming function call: %s", output)
			}
		})
	}
}

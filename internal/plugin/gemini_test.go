package plugin

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const geminiCompletion = `{"responseId":"g1","modelVersion":"gemini-4-argon","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"OK"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`

func TestGeminiExecutorNonStream(t *testing.T) {
	for format, body := range map[string]string{"openai": ccRequestBody, "claude": claudeRequestBody, "openai-response": `{"input":"hi"}`} {
		t.Run(format, func(t *testing.T) {
			seen := false
			f := &fakeCaller{}
			f.responder = wrapWithCatalog(`{"data":[{"id":"gemini-4-argon"}]}`, func(method string, payload []byte) ([]byte, error) {
				if method != pluginabi.MethodHostHTTPDo {
					return hostOK(map[string]any{}), nil
				}
				var req struct {
					URL     string      `json:"url"`
					Headers http.Header `json:"headers"`
					Body    []byte      `json:"body"`
				}
				_ = json.Unmarshal(payload, &req)
				if req.URL != "https://opencode.ai/inference/go/google/v1beta/models/gemini-4-argon:generateContent" {
					t.Errorf("URL: %s", req.URL)
				}
				if req.Headers.Get("x-goog-api-key") != testKey || req.Headers.Get("x-opencode-session") == "" || req.Headers.Get("Authorization") != "" {
					t.Error("missing native auth/session or unexpected bearer")
				}
				if !bytes.Contains(req.Body, []byte(`"contents"`)) || bytes.Contains(req.Body, []byte(`"messages"`)) {
					t.Error("not a native Gemini request")
				}
				seen = true
				return hostOK(pluginapi.HTTPResponse{StatusCode: 200, Body: []byte(geminiCompletion)}), nil
			})
			m := NewManager(NewHostBridge(f.call))
			t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
			registered, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML))
			if err != nil || !decodeEnv(t, registered).OK {
				t.Fatalf("register: %v %s", err, registered)
			}
			result, err := m.HandleCall("executor.execute", execReqBody("opencode-go/gemini-4-argon", format, []byte(body), false))
			if err != nil {
				t.Fatal(err)
			}
			var response pluginapi.ExecutorResponse
			decodeResult(t, result, &response)
			if !seen || !bytes.Contains(response.Payload, []byte("OK")) {
				t.Fatalf("no successful Gemini result: %s", response.Payload)
			}
		})
	}
}

func TestGeminiExecutorStreamingAndEOF(t *testing.T) {
	for _, format := range []string{"openai", "claude", "openai-response"} {
		for _, truncated := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/truncated=%v", format, truncated), func(t *testing.T) {
				frame := "data: " + geminiCompletion + "\n\n"
				if truncated {
					frame = strings.Replace(frame, `,"finishReason":"STOP"`, "", 1)
				}
				script := streamResponder(streamScript{upstreamID: "gemini-upstream", frames: []string{frame}})
				f := &fakeCaller{}
				f.responder = wrapWithCatalog(`{"data":[{"id":"gemini-4-argon"}]}`, func(method string, payload []byte) ([]byte, error) {
					if method == pluginabi.MethodHostHTTPDoStream {
						var req struct {
							URL     string      `json:"url"`
							Headers http.Header `json:"headers"`
						}
						_ = json.Unmarshal(payload, &req)
						if !strings.HasSuffix(req.URL, "/models/gemini-4-argon:streamGenerateContent?alt=sse") || req.Headers.Get("x-goog-api-key") != testKey {
							t.Errorf("invalid native stream transport: %s", req.URL)
						}
					}
					return script(method, payload)
				})
				m := NewManager(NewHostBridge(f.call))
				t.Cleanup(func() { _, _ = m.HandleCall("plugin.shutdown", nil) })
				registered, err := m.HandleCall("plugin.register", lifecycleRequestBody(testValidYAML))
				if err != nil || !decodeEnv(t, registered).OK {
					t.Fatalf("register: %v %s", err, registered)
				}
				body := map[string]string{"openai": ccRequestBody, "claude": claudeRequestBody, "openai-response": `{"input":"hi"}`}[format]
				result, err := m.HandleCall("executor.execute_stream", execStreamReqBody("opencode-go/gemini-4-argon", format, []byte(body), "gemini-downstream"))
				if err != nil || !decodeEnv(t, result).OK {
					t.Fatalf("execute: %v %s", err, result)
				}
				waitFor(t, "downstream close", func() bool { return len(f.callsOf(pluginabi.MethodHostStreamClose)) > 0 })
				var emitted []byte
				var closing string
				for _, c := range f.recorded() {
					if c.method == pluginabi.MethodHostStreamEmit {
						var wire map[string]any
						_ = json.Unmarshal(c.payload, &wire)
						if value, ok := wire["payload"].(string); ok {
							v, _ := base64.StdEncoding.DecodeString(value)
							emitted = append(emitted, v...)
						}
					}
					if c.method == pluginabi.MethodHostStreamClose {
						closing = string(c.payload)
					}
				}
				if truncated {
					if !strings.Contains(closing, "before a final candidate") {
						t.Fatalf("truncation became success: %s", closing)
					}
				} else if !bytes.Contains(emitted, []byte("OK")) {
					t.Fatalf("no stream output: %s", emitted)
				}
			})
		}
	}
}

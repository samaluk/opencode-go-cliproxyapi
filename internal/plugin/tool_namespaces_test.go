package plugin

import (
	"encoding/json"
	"strings"
	"testing"

	"opencode-go-cliproxyapi/internal/catalog"
)

const namespacedRequest = `{"tools":[{"type":"function","name":"lookup"},{"type":"namespace","name":"files","tools":[{"type":"function","name":"lookup","parameters":{"type":"object"}}]}],"tool_choice":{"type":"function","namespace":"files","name":"lookup"},"input":[{"type":"function_call","namespace":"files","name":"lookup","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"found"}]}`

func TestNamespaceRoundTripAcrossRoutes(t *testing.T) {
	for _, route := range []catalog.Route{catalog.RouteChatCompletions, catalog.RouteMessages} {
		t.Run(string(route), func(t *testing.T) {
			body, names, err := prepareToolNamespaces(route, "openai-response", []byte(namespacedRequest))
			if err != nil {
				t.Fatal(err)
			}
			translated, err := buildUpstreamRequest(route, "lab", "openai-response", body, nil)
			if err != nil || !strings.Contains(string(translated), "files__lookup") || strings.Contains(string(translated), `"namespace"`) {
				t.Fatalf("flattened request = %s, %v", translated, err)
			}
			response := []byte(`{"output":[{"type":"function_call","name":"files__lookup","arguments":"{\"name\":\"files__lookup\"}","call_id":"c2"}]}`)
			var decoded struct {
				Output []struct{ Name, Namespace, Arguments, CallID string }
			}
			if err := json.Unmarshal(names.restoreJSON(response), &decoded); err != nil {
				t.Fatal(err)
			}
			if item := decoded.Output[0]; item.Name != "lookup" || item.Namespace != "files" || item.Arguments != `{"name":"files__lookup"}` {
				t.Fatalf("identity or opaque arguments changed: %+v", item)
			}
			for _, envelope := range []string{
				`{"type":"response.output_item.added","item":{"type":"function_call","name":"files__lookup"}}`,
				`{"type":"response.output_item.done","item":{"type":"function_call","name":"files__lookup"}}`,
				`{"type":"response.completed","response":` + string(response) + `}`,
			} {
				event := names.restoreEvents([][]byte{[]byte("event: fixture\ndata: " + envelope + "\n\n")})[0]
				if !strings.Contains(string(event), `"namespace":"files"`) || !strings.HasSuffix(string(event), "\n\n") {
					t.Fatalf("stream identity or framing lost: %s", event)
				}
			}
		})
	}
}

func TestNamespaceRejectsAmbiguousAndUnsupportedTools(t *testing.T) {
	for _, body := range []string{
		strings.Replace(namespacedRequest, `"name":"lookup"`, `"name":"files__lookup"`, 1),
		strings.Replace(namespacedRequest, `"name":"files"`, `"name":""`, 1),
		strings.Replace(namespacedRequest, `"tools":[{"type":"function","name":"lookup","parameters"`, `"tools":[{"type":"custom","name":"lookup","parameters"`, 1),
		strings.Replace(namespacedRequest, `"namespace":"files"`, `"namespace":"missing"`, 1),
	} {
		if _, _, err := prepareToolNamespaces(catalog.RouteChatCompletions, "openai-response", []byte(body)); err == nil {
			t.Fatalf("expected explicit rejection: %s", body)
		}
	}
}

func TestNativeResponsesNamespaceUntouched(t *testing.T) {
	body, names, err := prepareToolNamespaces(catalog.RouteResponses, "openai-response", []byte(namespacedRequest))
	if err != nil || len(names) != 0 || string(body) != namespacedRequest {
		t.Fatal("native Responses namespace must pass through")
	}
}

func TestNamespaceHistorySurvivesChangingToolDefinitions(t *testing.T) {
	for _, toolField := range []string{
		``,
		`"tools":null,`,
		`"tools":[],`,
		`"tools":[{"type":"function","name":"current","parameters":{"type":"object"}}],`,
		`"tools":[{"type":"namespace","name":"files","tools":[{"type":"function","name":"current","parameters":{"type":"object"}}]}],`,
	} {
		for _, route := range []catalog.Route{catalog.RouteChatCompletions, catalog.RouteMessages} {
			t.Run(string(route)+"/"+toolField, func(t *testing.T) {
				body := []byte(`{` + toolField + `"input":[{"type":"function_call","namespace":"files","name":"retired","call_id":"c1","arguments":"{}"},{"type":"function_call_output","call_id":"c1","output":"done"},{"type":"function_call","namespace":"files","name":"retired","call_id":"c2","arguments":"{}"},{"type":"function_call_output","call_id":"c2","output":"again"},{"role":"user","content":"continue"}]}`)
				flat, names, err := prepareToolNamespaces(route, "openai-response", body)
				if err != nil {
					t.Fatal(err)
				}
				translated, err := buildUpstreamRequest(route, "lab", "openai-response", flat, nil)
				if err != nil || strings.Count(string(translated), "files__retired") != 2 {
					t.Fatalf("historical identity lost: %s %v", translated, err)
				}
				if names["files__retired"] != (toolIdentity{Name: "retired", Namespace: "files"}) {
					t.Fatal("historical identity is not restorable")
				}
				var decoded map[string]json.RawMessage
				if err := json.Unmarshal(flat, &decoded); err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(decoded["tools"]), "retired") {
					t.Fatal("replaying history re-enabled a removed tool")
				}
				if toolField == "" && decoded["tools"] != nil {
					t.Fatal("absent tools gained definitions")
				}
			})
		}
	}
}

func TestNamespaceHistoryStillRejectsCollisionsAndForcedMissingTools(t *testing.T) {
	history := `{"type":"function_call","namespace":"files","name":"retired","call_id":"c1","arguments":"{}"}`
	for _, body := range []string{
		`{"tools":[{"type":"function","name":"files__retired"}],"input":[` + history + `]}`,
		`{"input":[` + history + `,{"type":"function_call","name":"files__retired","call_id":"c2","arguments":"{}"}]}`,
		`{"input":[{"type":"function_call","namespace":"a__b","name":"c"},{"type":"function_call","namespace":"a","name":"b__c"}]}`,
		`{"input":[` + history + `],"tool_choice":{"type":"function","namespace":"files","name":"retired"}}`,
		`{"tools":[],"tool_choice":{"type":"function","namespace":"files","name":"retired"}}`,
	} {
		for _, route := range []catalog.Route{catalog.RouteChatCompletions, catalog.RouteMessages} {
			if _, _, err := prepareToolNamespaces(route, "openai-response", []byte(body)); err == nil {
				t.Fatalf("ambiguous or unavailable choice accepted: %s", body)
			}
		}
	}
}

func TestLongNamespaceHistoryKeepsDefinitionWireName(t *testing.T) {
	identity := toolIdentity{Name: "lookup", Namespace: strings.Repeat("files", 20)}
	tool, _ := json.Marshal(map[string]any{"type": "namespace", "name": identity.Namespace, "tools": []any{map[string]any{"type": "function", "name": identity.Name}}})
	call, _ := json.Marshal(map[string]any{"type": "function_call", "namespace": identity.Namespace, "name": identity.Name, "call_id": "c1", "arguments": "{}"})
	withTools := []byte(`{"tools":[` + string(tool) + `],"input":[` + string(call) + `]}`)
	withoutTools := []byte(`{"input":[` + string(call) + `]}`)
	var previous string
	for _, body := range [][]byte{withTools, withoutTools} {
		flat, names, err := prepareToolNamespaces(catalog.RouteMessages, "openai-response", body)
		if err != nil {
			t.Fatal(err)
		}
		var decoded struct{ Input []struct{ Name string } }
		if err := json.Unmarshal(flat, &decoded); err != nil {
			t.Fatal(err)
		}
		wire := decoded.Input[0].Name
		if len(wire) > 64 || !strings.HasPrefix(wire, "cpa_ns_") || names[wire] != identity || (previous != "" && wire != previous) {
			t.Fatalf("long identity changed with definitions: %s", flat)
		}
		previous = wire
	}
}

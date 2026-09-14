package chatcompletions

import (
	"encoding/json"
	"reflect"
	"testing"
)

func replayReasoning(t *testing.T, output []any) []map[string]any {
	t.Helper()
	input := append([]any{map[string]any{"type": "message", "role": "user", "content": "Use the test tool."}}, output...)
	input = append(input, map[string]any{"type": "function_call_output", "call_id": "call_1", "output": "42"})
	body, _ := json.Marshal(map[string]any{"input": input})
	got, e := BuildRequest("deepseek-v4.1-flash", "openai-response", body, nil)
	if e != nil {
		t.Fatal(e)
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	if err := json.Unmarshal(got, &req); err != nil {
		t.Fatal(err)
	}
	return req.Messages
}

func TestReasoningToolRoundTrip(t *testing.T) {
	const response = `{"id":"chat1","model":"deepseek-v4.1-flash","choices":[{"message":{"reasoning_content":"Inspect the test value.","content":"Reading it.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read_value","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	raw, e := ConvertNonStreamResponse("openai-response", 200, []byte(response))
	if e != nil {
		t.Fatal(e)
	}
	var converted struct {
		Output []any `json:"output"`
	}
	json.Unmarshal(raw, &converted)
	messages := replayReasoning(t, converted.Output)
	if len(messages) != 3 || messages[1]["reasoning_content"] != "Inspect the test value." || messages[1]["content"] != "Reading it." || len(messages[1]["tool_calls"].([]any)) != 1 {
		t.Fatalf("reasoning/tool turn lost: %#v", messages)
	}

	sc := NewStreamConverter("openai-response")
	events := feedAll(t, sc,
		`data: {"id":"chat1","model":"deepseek-v4.1-flash","choices":[{"delta":{"reasoning_content":"Inspect "}}]}`,
		`data: {"choices":[{"delta":{"reasoning_content":"the test value."}}]}`,
		`data: {"choices":[{"delta":{"content":"Reading it."}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"read_value","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`)
	var done []any
	var terminal []any
	var indexes []int
	for _, event := range events {
		if event.Name == "response.output_item.done" {
			done = append(done, event.Data["item"])
			indexes = append(indexes, int(event.Data["output_index"].(float64)))
		}
		if event.Name == "response.completed" {
			terminal = event.Data["response"].(map[string]any)["output"].([]any)
		}
	}
	if !reflect.DeepEqual(indexes, []int{0, 1, 2}) || !reflect.DeepEqual(done, terminal) || !reflect.DeepEqual(done, converted.Output) {
		t.Fatalf("stream/nonstream mismatch: %#v / %#v", done, converted.Output)
	}
	if got := replayReasoning(t, done); !reflect.DeepEqual(got, messages) {
		t.Fatalf("stream replay differs: %#v", got)
	}
}

func TestReasoningDoesNotLeakAcrossUserTurns(t *testing.T) {
	body := []byte(`{"input":[{"type":"reasoning","id":"rs_cpa_chat_old","summary":[{"text":"old thought"}]},{"role":"assistant","content":"old answer"},{"role":"user","content":"new question"},{"type":"function_call","call_id":"new","name":"read","arguments":"{}"}]}`)
	raw, e := BuildRequest("deepseek-v4.1-flash", "openai-response", body, nil)
	if e != nil {
		t.Fatal(e)
	}
	var req struct {
		Messages []map[string]any `json:"messages"`
	}
	json.Unmarshal(raw, &req)
	if req.Messages[0]["reasoning_content"] != "old thought" || req.Messages[2]["reasoning_content"] != "" {
		t.Fatalf("wrong reasoning association: %#v", req.Messages)
	}
}

func TestMissingAndForeignReasoningCompatibility(t *testing.T) {
	for _, model := range []string{"deepseek-v4.1-flash", "other-model"} {
		raw, e := BuildRequest(model, "openai-response", []byte(`{"input":[{"type":"reasoning","id":"rs_foreign","summary":[{"text":"only a summary"}]},{"type":"function_call","name":"read","call_id":"1","arguments":"{}"}]}`), nil)
		if e != nil {
			t.Fatal(e)
		}
		var req struct {
			Messages []map[string]any `json:"messages"`
		}
		json.Unmarshal(raw, &req)
		value, present := req.Messages[0]["reasoning_content"]
		if model == "other-model" && present {
			t.Fatal("foreign summary replayed as full reasoning")
		}
		if model != "other-model" && (!present || value != "") {
			t.Fatal("older DeepSeek history cannot resume")
		}
	}
}

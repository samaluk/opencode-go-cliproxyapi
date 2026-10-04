package shared

import (
	"encoding/json"
	"testing"
)

func TestNormalizeRejectsAmbiguousToolIdentities(t *testing.T) {
	plain := `{"type":"function","name":"files__lookup"}`
	namespaced := `{"type":"namespace","name":"files","tools":[{"type":"function","name":"lookup"}]}`
	call := `{"type":"function_call","namespace":"files","name":"lookup","call_id":"c1","arguments":"{}"}`
	plainCall := `{"type":"function_call","name":"files__lookup","call_id":"c2","arguments":"{}"}`
	cases := map[string]string{
		"plain declaration first":        `{"tools":[` + plain + `,` + namespaced + `],"input":[]}`,
		"namespace declaration first":    `{"tools":[` + namespaced + `,` + plain + `],"input":[]}`,
		"declaration versus history":     `{"tools":[` + plain + `],"input":[` + call + `]}`,
		"namespace versus plain history": `{"tools":[` + namespaced + `],"input":[` + plainCall + `]}`,
		"plain history first":            `{"input":[` + plainCall + `,` + call + `]}`,
		"namespace history first":        `{"input":[` + call + `,` + plainCall + `]}`,
		"ambiguous historical separator": `{"input":[{"type":"function_call","namespace":"a__b","name":"c","call_id":"c1","arguments":"{}"},{"type":"function_call","namespace":"a","name":"b__c","call_id":"c2","arguments":"{}"}]}`,
		"forced choice":                  `{"tools":[` + plain + `],"input":[],"tool_choice":{"type":"function","namespace":"files","name":"lookup"}}`,
	}
	for name, body := range cases {
		for _, target := range []string{"/v1/chat/completions", "/v1/messages"} {
			t.Run(name+target, func(t *testing.T) {
				var r ResponsesRequest
				if err := json.Unmarshal([]byte(body), &r); err != nil {
					t.Fatal(err)
				}
				if _, err := NewResponseTools().Normalize(&r, target); err == nil {
					t.Fatal("ambiguous tool identities accepted")
				}
			})
		}
	}
}

func TestNormalizeRetainsCustomIdentityOnHistoryReplay(t *testing.T) {
	var r ResponsesRequest
	body := `{"tools":[{"type":"namespace","name":"files","tools":[{"type":"custom","name":"lookup"}]}],"input":[{"type":"function_call","namespace":"files","name":"lookup","call_id":"c1","arguments":"{}"},{"type":"custom_tool_call","namespace":"files","name":"lookup","call_id":"c2","input":"query"}]}`
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatal(err)
	}
	registry := NewResponseTools()
	items, err := registry.Normalize(&r, "/v1/chat/completions")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || !registry.IsCustom(items[0].Name) || registry.Identity(items[1].Name).namespace != "files" {
		t.Fatal("history lost custom restoration identity")
	}
}

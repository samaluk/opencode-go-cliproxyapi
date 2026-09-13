package plugin

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"opencode-go-cliproxyapi/internal/catalog"
	"opencode-go-cliproxyapi/internal/errclass"
)

type toolIdentity struct {
	Name      string
	Namespace string
}

// A namespace is a client-side grouping, not an upstream hosted tool. Flatten
// function definitions for Chat/Messages and restore their identity on return.
// Unsupported hosted/custom tools still reach the adapters' explicit rejection.
type toolNamespaces map[string]toolIdentity

func prepareToolNamespaces(route catalog.Route, source string, body []byte) ([]byte, toolNamespaces, *errclass.Error) {
	if source != "openai-response" || route == catalog.RouteResponses {
		return body, nil, nil
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, nil, nil // The adapter owns malformed-request errors.
	}
	var tools []map[string]json.RawMessage
	if raw, present := request["tools"]; present && json.Unmarshal(raw, &tools) != nil {
		return body, nil, nil
	}
	var input []map[string]json.RawMessage
	inputIsArray := json.Unmarshal(request["input"], &input) == nil
	identities := toolNamespaces{}
	declared := map[string]bool{}
	flat := make([]map[string]json.RawMessage, 0, len(tools))
	used := map[string]bool{}
	for _, tool := range tools {
		if rawString(tool["type"]) != "namespace" {
			used[rawString(tool["name"])] = true
		}
	}
	// Historical unqualified calls can collide even after their definitions
	// have been removed. Reserve those names before flattening any namespace.
	for _, item := range input {
		if rawString(item["type"]) == "function_call" && rawString(item["namespace"]) == "" {
			used[rawString(item["name"])] = true
		}
	}
	register := func(identity toolIdentity, definition bool) (string, *errclass.Error) {
		wireName := namespaceWireName(identity)
		previous, exists := identities[wireName]
		if used[wireName] && (definition || !exists || previous != identity) {
			return "", errclass.Translation("flattened namespace tool name collision")
		}
		used[wireName] = true
		identities[wireName] = identity
		if definition {
			declared[wireName] = true
		}
		return wireName, nil
	}
	flattenedTools := false
	for _, tool := range tools {
		if rawString(tool["type"]) != "namespace" {
			flat = append(flat, tool)
			continue
		}
		flattenedTools = true
		namespace := rawString(tool["name"])
		var children []map[string]json.RawMessage
		if namespace == "" || json.Unmarshal(tool["tools"], &children) != nil || len(children) == 0 {
			return nil, nil, errclass.Translation("namespace requires a name and function tools")
		}
		for _, child := range children {
			name := rawString(child["name"])
			if rawString(child["type"]) != "function" || name == "" {
				return nil, nil, &errclass.Error{Class: errclass.ClassUnsupported, Message: "only named function tools can be flattened from namespaces"}
			}
			wireName, eErr := register(toolIdentity{Name: name, Namespace: namespace}, true)
			if eErr != nil {
				return nil, nil, eErr
			}
			child["name"], _ = json.Marshal(wireName)
			flat = append(flat, child)
		}
	}
	resolve := func(item map[string]json.RawMessage, requireDefinition bool) *errclass.Error {
		namespace := rawString(item["namespace"])
		if namespace == "" {
			return nil
		}
		identity := toolIdentity{Name: rawString(item["name"]), Namespace: namespace}
		if identity.Name == "" {
			return errclass.Translation("namespaced function reference requires a name")
		}
		wireName := namespaceWireName(identity)
		if requireDefinition {
			if !declared[wireName] || identities[wireName] != identity {
				return errclass.Translation("namespaced function reference has no matching definition")
			}
		} else {
			// Completed calls remain replayable when tools are removed from the
			// current definitions. The wire name depends only on their identity.
			if _, eErr := register(identity, false); eErr != nil {
				return eErr
			}
		}
		item["name"], _ = json.Marshal(wireName)
		delete(item, "namespace")
		return nil
	}
	if inputIsArray {
		for _, item := range input {
			if rawString(item["type"]) == "function_call" {
				if eErr := resolve(item, false); eErr != nil {
					return nil, nil, eErr
				}
			}
		}
		request["input"], _ = json.Marshal(input)
	}
	var choice map[string]json.RawMessage
	if json.Unmarshal(request["tool_choice"], &choice) == nil && choice != nil {
		if eErr := resolve(choice, true); eErr != nil {
			return nil, nil, eErr
		}
		request["tool_choice"], _ = json.Marshal(choice)
	}
	if len(identities) == 0 {
		return body, nil, nil
	}
	if flattenedTools {
		request["tools"], _ = json.Marshal(flat)
	}
	out, _ := json.Marshal(request)
	return out, identities, nil
}

func namespaceWireName(identity toolIdentity) string {
	wireName := identity.Namespace + "__" + identity.Name
	if len(wireName) > 64 {
		digest := sha256.Sum256([]byte(identity.Namespace + "\x00" + identity.Name))
		wireName = "cpa_ns_" + hex.EncodeToString(digest[:24])
	}
	return wireName
}

func rawString(raw json.RawMessage) string {
	var value string
	_ = json.Unmarshal(raw, &value)
	return value
}

// Restore only protocol-owned function-call items. Tool argument strings and
// arbitrary application data must never be traversed or rewritten.
func (names toolNamespaces) restoreJSON(body []byte) []byte {
	if len(names) == 0 {
		return body
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(body, &object) != nil || object == nil {
		return body
	}
	if rawString(object["type"]) == "function_call" {
		if identity, ok := names[rawString(object["name"])]; ok {
			object["name"], _ = json.Marshal(identity.Name)
			object["namespace"], _ = json.Marshal(identity.Namespace)
		}
	}
	for _, key := range []string{"item", "response"} {
		if value, ok := object[key]; ok {
			object[key] = names.restoreJSON(value)
		}
	}
	var output []json.RawMessage
	if json.Unmarshal(object["output"], &output) == nil && output != nil {
		for i := range output {
			output[i] = names.restoreJSON(output[i])
		}
		object["output"], _ = json.Marshal(output)
	}
	result, _ := json.Marshal(object)
	return result
}

func (names toolNamespaces) restoreEvents(events [][]byte) [][]byte {
	if len(names) == 0 {
		return events
	}
	for i, event := range events {
		lines := bytes.Split(event, []byte("\n"))
		for j, line := range lines {
			if bytes.HasPrefix(line, []byte("data:")) {
				payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
				if strings.HasPrefix(string(payload), "{") {
					lines[j] = append([]byte("data: "), names.restoreJSON(payload)...)
				}
			}
		}
		events[i] = bytes.Join(lines, []byte("\n"))
	}
	return events
}

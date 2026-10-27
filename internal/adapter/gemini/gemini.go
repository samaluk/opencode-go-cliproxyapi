// Package gemini adapts OpenCode Go's native Google generateContent endpoints.
// CPA's exported translators own the Gemini wire format; the existing plugin
// adapters retain client tool identity and Messages/Responses event synthesis.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"

	"opencode-go-cliproxyapi/internal/adapter/chatcompletions"
	"opencode-go-cliproxyapi/internal/adapter/shared"
	"opencode-go-cliproxyapi/internal/errclass"
	"opencode-go-cliproxyapi/internal/thinking"
)

// Adapter holds state for one execution, never across accounts or requests.
type Adapter struct {
	model, sourceFormat string
	tools               *shared.ResponseTools
	chatRequest         []byte
	geminiRequest       []byte
	state               any
	framer              *shared.SSEFramer
	client              *chatcompletions.StreamConverter
	finished            bool
	finishReason        string
	usage               json.RawMessage
	responseID          string
	responseModel       string
	closed              bool
}

func NewAdapter(model, sourceFormat string, tools *shared.ResponseTools) *Adapter {
	return &Adapter{model: model, sourceFormat: sourceFormat, tools: tools,
		responseModel: model,
		framer:        shared.NewSSEFramer(false), client: chatcompletions.NewStreamConverter(sourceFormat, tools)}
}

// Endpoint substitutes only the escaped model ID, retaining any explicit path
// prefix. Gemini uses separate streaming and non-streaming method names.
func Endpoint(template, model string, stream bool) string {
	path := strings.ReplaceAll(template, "{model}", url.PathEscape(model))
	if stream {
		path = strings.TrimSuffix(path, ":generateContent") + ":streamGenerateContent"
		if strings.Contains(path, "?") {
			return path + "&alt=sse"
		}
		return path + "?alt=sse"
	}
	return path
}

func (a *Adapter) BuildRequest(body []byte, support *pluginapi.ThinkingSupport) ([]byte, *errclass.Error) {
	chat, eErr := chatcompletions.BuildRequest(a.model, a.sourceFormat, body, support, a.tools)
	if eErr != nil {
		return nil, eErr
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(chat, &input) != nil || input == nil {
		return nil, errclass.Translation("invalid Gemini source request")
	}
	// Older plugin Chat adapters do not carry Claude's explicit effort field.
	// Keep that control at this boundary instead of silently reverting to a
	// Gemini default or deriving a different level from a thinking budget.
	if a.sourceFormat == "claude" {
		var source struct {
			OutputConfig struct {
				Effort string `json:"effort"`
			} `json:"output_config"`
		}
		_ = json.Unmarshal(body, &source)
		if effort := strings.ToLower(strings.TrimSpace(source.OutputConfig.Effort)); effort != "" {
			if err := thinking.ValidateEffort(effort, support); err != nil {
				return nil, err
			}
			input["reasoning_effort"], _ = json.Marshal(effort)
			chat, _ = json.Marshal(input)
		}
	}
	var count int
	_ = json.Unmarshal(input["n"], &count)
	if count > 1 {
		return nil, errclass.Translation("Gemini client adapters support one candidate per request")
	}
	var stream bool
	_ = json.Unmarshal(input["stream"], &stream)
	a.chatRequest = chat
	out := translator.TranslateRequest(translator.FormatOpenAI, translator.FormatGemini, a.model, chat, stream)
	var native map[string]json.RawMessage
	var contents []json.RawMessage
	if json.Unmarshal(out, &native) != nil || native == nil || json.Unmarshal(native["contents"], &contents) != nil || len(contents) == 0 {
		return nil, errclass.Translation("Gemini request translation produced no contents")
	}
	// Google selects the model in the URL, not in generateContent's body.
	delete(native, "model")
	a.geminiRequest, _ = json.Marshal(native)
	return a.geminiRequest, nil
}

type candidate struct {
	FinishReason string          `json:"finishReason"`
	Content      json.RawMessage `json:"content"`
}

type response struct {
	Candidates     []candidate     `json:"candidates"`
	PromptFeedback json.RawMessage `json:"promptFeedback"`
	Error          json.RawMessage `json:"error"`
	Usage          json.RawMessage `json:"usageMetadata"`
}

func validateResponse(body []byte) (*response, *errclass.Error) {
	var data response
	if json.Unmarshal(body, &data) != nil || bytes.Equal(bytes.TrimSpace(body), []byte("null")) {
		return nil, errclass.Translation("malformed Gemini response")
	}
	if len(data.Error) != 0 && string(data.Error) != "null" {
		return nil, errclass.Translation("Gemini upstream returned an error")
	}
	if len(data.PromptFeedback) != 0 && len(data.Candidates) == 0 {
		return nil, errclass.Translation("Gemini blocked the prompt")
	}
	for _, c := range data.Candidates {
		switch c.FinishReason {
		case "", "STOP", "MAX_TOKENS":
		default:
			return nil, errclass.Translation("Gemini ended with an unsupported or blocked finish reason")
		}
	}
	return &data, nil
}

func (a *Adapter) ConvertNonStreamResponse(status int, body []byte) ([]byte, *errclass.Error) {
	if status >= http.StatusBadRequest {
		return nil, shared.UpstreamStatusError(status, body)
	}
	data, eErr := validateResponse(body)
	if eErr != nil {
		return nil, eErr
	}
	if len(data.Candidates) == 0 || data.Candidates[0].FinishReason == "" {
		return nil, errclass.Translation("Gemini response has no completed candidate")
	}
	chat := translator.TranslateNonStream(context.Background(), translator.FormatGemini, translator.FormatOpenAI,
		a.model, a.chatRequest, a.geminiRequest, body, &a.state)
	if !json.Valid(chat) {
		return nil, errclass.Translation("invalid translated Gemini response")
	}
	return chatcompletions.ConvertNonStreamResponse(a.sourceFormat, status, normalizeFinishReason(chat), a.tools)
}

// Feed accepts arbitrary network chunk boundaries and legal multi-line SSE.
// Completion waits for EOF so a separate final usage frame is not lost.
func (a *Adapter) Feed(chunk []byte) ([][]byte, bool, *errclass.Error) {
	if a.closed {
		return nil, true, nil
	}
	a.framer.Push(chunk)
	var events [][]byte
	for {
		_, payload, _, ok := a.framer.Next()
		if !ok {
			return events, false, nil
		}
		if payload == "" || payload == "[DONE]" {
			continue
		}
		data, eErr := validateResponse([]byte(payload))
		if eErr != nil {
			return events, false, eErr
		}
		for _, c := range data.Candidates {
			a.finished = a.finished || c.FinishReason != ""
			if c.FinishReason != "" {
				a.finishReason = c.FinishReason
			}
		}
		if len(data.Usage) != 0 {
			a.usage = append(a.usage[:0], data.Usage...)
		}
		// The SDK emits its terminal only when a finish reason and usage
		// occur together. Hold the reason until EOF so split usage trailers
		// work too, and no client sees an early terminal or stale totals.
		var held map[string]json.RawMessage
		_ = json.Unmarshal([]byte(payload), &held)
		if value := held["modelVersion"]; len(value) != 0 {
			_ = json.Unmarshal(value, &a.responseModel)
		}
		if value := held["responseId"]; len(value) != 0 {
			_ = json.Unmarshal(value, &a.responseID)
		}
		held["modelVersion"], _ = json.Marshal(a.responseModel)
		held["responseId"], _ = json.Marshal(a.responseID)
		if raw := held["candidates"]; len(raw) != 0 {
			var candidates []map[string]json.RawMessage
			_ = json.Unmarshal(raw, &candidates)
			for _, c := range candidates {
				delete(c, "finishReason")
			}
			held["candidates"], _ = json.Marshal(candidates)
		}
		intermediate, _ := json.Marshal(held)
		chunks := translator.TranslateStream(context.Background(), translator.FormatGemini, translator.FormatOpenAI,
			a.model, a.chatRequest, a.geminiRequest, intermediate, &a.state)
		for _, chat := range chunks {
			if len(chat) == 0 {
				continue
			}
			if !json.Valid(chat) {
				return events, false, errclass.Translation("invalid translated Gemini stream chunk")
			}
			chat = normalizeFinishReason(chat)
			frame := append(append([]byte("data: "), chat...), '\n', '\n')
			out, _, eErr := a.client.Feed(frame)
			if eErr != nil {
				return events, false, eErr
			}
			events = append(events, out...)
		}
	}
}

// Finish rejects a truncated stream instead of manufacturing a successful end.
// The plugin calls this only when the upstream transport reports EOF.
func (a *Adapter) Finish() ([][]byte, *errclass.Error) {
	if a.closed {
		return nil, nil
	}
	events, _, eErr := a.Feed([]byte("\n\n"))
	a.closed = true
	if eErr != nil {
		return nil, eErr
	}
	if !a.finished {
		return nil, errclass.Translation("Gemini stream closed before a final candidate")
	}
	usage := a.usage
	if len(usage) == 0 {
		usage = json.RawMessage(`{}`)
	}
	final, _ := json.Marshal(map[string]any{"candidates": []map[string]any{{"index": 0, "finishReason": a.finishReason}}, "usageMetadata": usage,
		"modelVersion": a.responseModel, "responseId": a.responseID})
	for _, chat := range translator.TranslateStream(context.Background(), translator.FormatGemini, translator.FormatOpenAI,
		a.model, a.chatRequest, a.geminiRequest, final, &a.state) {
		frame := append(append([]byte("data: "), normalizeFinishReason(chat)...), '\n', '\n')
		out, _, err := a.client.Feed(frame)
		if err != nil {
			return nil, err
		}
		events = append(events, out...)
	}
	out, _, eErr := a.client.Feed([]byte("data: [DONE]\n\n"))
	if eErr != nil {
		return nil, eErr
	}
	return append(events, out...), nil
}

// The Gemini SDK calls a token-limited Chat result "max_tokens"; Chat clients
// and the plugin's existing adapters use the standard "length" finish reason.
func normalizeFinishReason(body []byte) []byte {
	var data map[string]json.RawMessage
	if json.Unmarshal(body, &data) != nil {
		return body
	}
	var choices []map[string]json.RawMessage
	if json.Unmarshal(data["choices"], &choices) != nil {
		return body
	}
	changed := false
	for _, c := range choices {
		if string(c["finish_reason"]) == `"max_tokens"` {
			c["finish_reason"] = json.RawMessage(`"length"`)
			changed = true
		}
	}
	if !changed {
		return body
	}
	data["choices"], _ = json.Marshal(choices)
	out, _ := json.Marshal(data)
	return out
}

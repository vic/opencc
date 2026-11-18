// Package mapper turns OpenAI requests into one text body for a Claude session
// and a reply text back into OpenAI shapes. Pure functions, no I/O.
package mapper

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"opencc/internal/apierr"
	"opencc/internal/openai"
)

// ignored: accepted and dropped, because SDKs send them by default. The server
// lists the ones present in the X-Opencc-Ignored response header. The only place.
var ignored = []string{
	"temperature", "top_p", "max_tokens", "max_completion_tokens", "stop", "seed",
	"presence_penalty", "frequency_penalty", "logit_bias", "user", "stream_options",
	"store", "metadata", "parallel_tool_calls", "reasoning_effort", "service_tier",
	"best_of", "suffix", "echo",
}

// rejected: a value other than the listed neutral one is a 400 unsupported_parameter.
var rejected = map[string]func(json.RawMessage) bool{
	"n":                  isOne,
	"logprobs":           isFalseOrNull,
	"top_logprobs":       isNullOrZero,
	"audio":              isNull,
	"prediction":         isNull,
	"web_search_options": isNull,
	"modalities":         func(v json.RawMessage) bool { return isNull(v) || string(compact(v)) == `["text"]` },
}

func compact(v json.RawMessage) []byte {
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return v
	}
	return b.Bytes()
}
func isNull(v json.RawMessage) bool        { return string(compact(v)) == "null" }
func isOne(v json.RawMessage) bool         { return isNull(v) || string(compact(v)) == "1" }
func isFalseOrNull(v json.RawMessage) bool { return isNull(v) || string(compact(v)) == "false" }
func isNullOrZero(v json.RawMessage) bool  { return isNull(v) || string(compact(v)) == "0" }

// Check applies the two tables to the raw request object. It returns the
// ignored keys that were present, in sorted order.
func Check(raw []byte) ([]string, *apierr.Error) {
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, apierr.New(apierr.BadRequest, "", "body is not a JSON object")
	}
	for k, ok := range rejected {
		if v, has := keys[k]; has && !ok(v) {
			return nil, apierr.New(apierr.UnsupportedParam, k, "parameter "+k+" is not supported")
		}
	}
	var got []string
	for _, k := range ignored {
		if _, has := keys[k]; has {
			got = append(got, k)
		}
	}
	slices.Sort(got)
	return got, nil
}

// text flattens message content: a string, or an array of text parts.
func text(c json.RawMessage) (string, *apierr.Error) {
	if len(c) == 0 || isNull(c) {
		return "", nil
	}
	var s string
	if json.Unmarshal(c, &s) == nil {
		return s, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(c, &parts) != nil {
		return "", apierr.New(apierr.BadRequest, "messages", "content must be a string or an array of parts")
	}
	var b strings.Builder
	for _, p := range parts {
		if p.Type != "text" {
			e := apierr.New(apierr.BadRequest, "messages", "content part type "+p.Type+" is not supported")
			e.Code = "unsupported_content"
			return "", e
		}
		b.WriteString(p.Text)
	}
	return b.String(), nil
}

// Tools is the set of tool names a request offered. Only Render builds one.
type Tools map[string]struct{}

// Render builds the conversation text for the session. replyTo is the wire
// address the session must answer on; id is the request id.
func Render(req openai.ChatRequest, replyTo, id string) (string, Tools, *apierr.Error) {
	if len(req.Messages) == 0 {
		return "", nil, apierr.New(apierr.BadRequest, "messages", "messages is empty")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "[opencc request %s] This is an OpenAI chat completion request relayed by opencc. Answer the LAST message. Reply with SendMessage to %s, one message, only the assistant reply text, no preamble.\n", id, replyTo)
	offered := Tools{}
	if string(compact(req.ToolChoice)) != `"none"` && len(req.Tools) > 0 {
		b.WriteString("\nTOOLS: the tools below belong to the API client, not to you. You cannot run them and you do not need them. When the question needs one, do NOT say you lack it. Reply with ONLY one fenced ```json block holding {\"tool_calls\":[{\"name\":\"<tool>\",\"arguments\":{...}}]} (the send tool rejects a bare JSON object). The client runs the tool and sends you the result. If no tool is needed, reply with plain text.\n")
		for _, t := range req.Tools {
			if t.Type != "function" {
				return "", nil, apierr.New(apierr.UnsupportedParam, "tools", "tool type "+t.Type+" is not supported")
			}
			offered[t.Function.Name] = struct{}{}
			p, _ := json.Marshal(t.Function.Parameters)
			fmt.Fprintf(&b, "- %s: %s parameters=%s\n", t.Function.Name, t.Function.Description, p)
		}
		if tc := compact(req.ToolChoice); len(tc) > 0 && string(tc) != "null" && string(tc) != `"auto"` {
			fmt.Fprintf(&b, "tool_choice=%s\n", tc)
		}
	}
	if rf := req.ResponseFormat; rf != nil {
		switch rf.Type {
		case "json_object":
			b.WriteString("\nFORMAT: reply with ONLY one valid JSON object.\n")
		case "json_schema":
			fmt.Fprintf(&b, "\nFORMAT: reply with ONLY JSON valid for this json_schema: %s\n", compact(rf.JSONSchema))
		}
	}
	b.WriteString("\n--- conversation ---\n")
	for _, m := range req.Messages {
		t, err := text(m.Content)
		if err != nil {
			return "", nil, err
		}
		role := m.Role
		switch role {
		case "developer":
			role = "system"
		case "system", "user", "assistant":
		case "tool":
			role = "tool(" + m.ToolCallID + ")"
		default:
			return "", nil, apierr.New(apierr.BadRequest, "messages", "role "+m.Role+" is not supported")
		}
		if m.Name != "" {
			role += "/" + m.Name
		}
		fmt.Fprintf(&b, "%s: %s\n", role, t)
		for _, c := range m.ToolCalls {
			fmt.Fprintf(&b, "assistant called %s(%s) id=%s\n", c.Function.Name, c.Function.Arguments, c.ID)
		}
	}
	return b.String(), offered, nil
}

// RenderPrompt builds the body for a legacy completion prompt.
func RenderPrompt(prompt json.RawMessage, replyTo, id string) (string, *apierr.Error) {
	var s string
	var arr []string
	switch {
	case json.Unmarshal(prompt, &s) == nil && s != "":
	case json.Unmarshal(prompt, &arr) == nil && len(arr) == 1:
		s = arr[0]
	default:
		return "", apierr.New(apierr.BadRequest, "prompt", "prompt must be a non-empty string or a one-element string array")
	}
	return fmt.Sprintf("[opencc request %s] Continue or answer this prompt. Reply with SendMessage to %s, one message, only the completion text.\n\n%s\n", id, replyTo, s), nil
}

// Reply is a parsed session reply: plain text, or tool calls.
type Reply struct {
	Text  string
	Calls []openai.ToolCall
}

type toolReply struct {
	ToolCalls []struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"tool_calls"`
}

// ParseReply reads a reply. With offered tools, a strict JSON decode that
// matches the tool envelope gives calls; any decode failure gives plain text.
// A call to a tool that was not offered is an error.
func ParseReply(body string, offered Tools, newID func() string) (Reply, *apierr.Error) {
	if len(offered) == 0 {
		return Reply{Text: body}, nil
	}
	dec := json.NewDecoder(strings.NewReader(unfence(body)))
	dec.DisallowUnknownFields()
	var tr toolReply
	if dec.Decode(&tr) != nil || dec.More() || len(tr.ToolCalls) == 0 {
		return Reply{Text: body}, nil
	}
	var calls []openai.ToolCall
	for _, c := range tr.ToolCalls {
		if _, ok := offered[c.Name]; !ok {
			e := apierr.New(apierr.ReplyMalformed, "", "reply called unknown tool "+c.Name)
			e.Code = "unknown_tool"
			return Reply{}, e
		}
		args := string(compact(c.Arguments))
		if args == "" || args == "null" {
			args = "{}"
		}
		var oc openai.ToolCall
		oc.ID, oc.Type = "call_"+newID(), "function"
		oc.Function.Name, oc.Function.Arguments = c.Name, args
		calls = append(calls, oc)
	}
	return Reply{Calls: calls}, nil
}

// unfence trims body and removes one wrapping ```/```json code fence.
func unfence(body string) string {
	s := strings.TrimSpace(body)
	if !strings.HasPrefix(s, "```") || !strings.HasSuffix(s, "```") || len(s) < 6 {
		return s
	}
	s = strings.TrimSuffix(strings.TrimPrefix(s, "```"), "```")
	s = strings.TrimPrefix(s, "json")
	return strings.TrimSpace(s)
}

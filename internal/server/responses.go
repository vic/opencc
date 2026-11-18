package server

import (
	"encoding/json"
	"net/http"
	"sync"

	"opencc/internal/apierr"
	"opencc/internal/mapper"
	"opencc/internal/openai"
)

type respRequest struct {
	Model              string            `json:"model"`
	Input              json.RawMessage   `json:"input"`
	Instructions       string            `json:"instructions"`
	Stream             bool              `json:"stream"`
	Tools              []json.RawMessage `json:"tools"`
	ToolChoice         json.RawMessage   `json:"tool_choice"`
	PreviousResponseID string            `json:"previous_response_id"`
	Store              *bool             `json:"store"`
}

// respStore keeps finished responses for GET and DELETE /v1/responses/{id}. In memory, lost on restart.
type respStore struct {
	mu sync.Mutex
	m  map[string]map[string]any
}

func (s *respStore) put(id string, v map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]map[string]any{}
	}
	s.m[id] = v
}

func (s *respStore) get(id string) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[id]
	return v, ok
}

func (s *respStore) del(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[id]
	delete(s.m, id)
	return ok
}

// chatTools maps flat Responses tools to chat tools.
func chatTools(raw []json.RawMessage) ([]openai.Tool, *apierr.Error) {
	var out []openai.Tool
	for _, r := range raw {
		var t struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Parameters  json.RawMessage `json:"parameters"`
		}
		if json.Unmarshal(r, &t) != nil || t.Type != "function" {
			return nil, apierr.New(apierr.UnsupportedParam, "tools", "only function tools are supported")
		}
		var ct openai.Tool
		ct.Type, ct.Function.Name, ct.Function.Description, ct.Function.Parameters = "function", t.Name, t.Description, t.Parameters
		out = append(out, ct)
	}
	return out, nil
}

func textMsg(role, text string) openai.Message {
	c, _ := json.Marshal(text)
	return openai.Message{Role: role, Content: c}
}

// chatMessages maps Responses `input` (string, or items) to chat messages.
func chatMessages(r respRequest) ([]openai.Message, *apierr.Error) {
	var msgs []openai.Message
	if r.Instructions != "" {
		msgs = append(msgs, textMsg("system", r.Instructions))
	}
	var s string
	if json.Unmarshal(r.Input, &s) == nil && s != "" {
		return append(msgs, textMsg("user", s)), nil
	}
	var items []struct {
		Type      string          `json:"type"`
		Role      string          `json:"role"`
		Content   json.RawMessage `json:"content"`
		CallID    string          `json:"call_id"`
		Name      string          `json:"name"`
		Arguments string          `json:"arguments"`
		Output    string          `json:"output"`
	}
	if json.Unmarshal(r.Input, &items) != nil || len(items) == 0 {
		return nil, apierr.New(apierr.BadRequest, "input", "input must be a string or a non-empty array of items")
	}
	for _, it := range items {
		switch it.Type {
		case "function_call":
			var m openai.Message
			m.Role = "assistant"
			var tc openai.ToolCall
			tc.ID, tc.Type = it.CallID, "function"
			tc.Function.Name, tc.Function.Arguments = it.Name, it.Arguments
			m.ToolCalls = []openai.ToolCall{tc}
			msgs = append(msgs, m)
			continue
		case "function_call_output":
			m := textMsg("tool", it.Output)
			m.ToolCallID = it.CallID
			msgs = append(msgs, m)
			continue
		case "", "message":
		default:
			return nil, apierr.New(apierr.UnsupportedParam, "input", "input item type "+it.Type+" is not supported")
		}
		var parts []struct{ Type, Text string }
		c := it.Content
		if json.Unmarshal(it.Content, &parts) == nil {
			var tp []map[string]string
			for _, p := range parts {
				if p.Type != "input_text" && p.Type != "output_text" && p.Type != "text" {
					return nil, apierr.New(apierr.BadRequest, "input", "content part type "+p.Type+" is not supported")
				}
				tp = append(tp, map[string]string{"type": "text", "text": p.Text})
			}
			c, _ = json.Marshal(tp)
		}
		msgs = append(msgs, openai.Message{Role: it.Role, Content: c})
	}
	return msgs, nil
}

func (s *Server) responses(w http.ResponseWriter, r *http.Request) {
	raw, e := readBody(w, r)
	if e != nil {
		fail(w, e)
		return
	}
	ign, e := mapper.Check(raw)
	if e != nil {
		fail(w, e)
		return
	}
	var req respRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		fail(w, apierr.New(apierr.BadRequest, "", "invalid JSON: "+err.Error()))
		return
	}
	sess, e := s.session(req.Model)
	if e != nil {
		fail(w, e)
		return
	}
	if req.PreviousResponseID != "" {
		fail(w, apierr.New(apierr.UnsupportedParam, "previous_response_id", "send the whole conversation in input"))
		return
	}
	tools, e := chatTools(req.Tools)
	if e != nil {
		fail(w, e)
		return
	}
	msgs, e := chatMessages(req)
	if e != nil {
		fail(w, e)
		return
	}
	id := newID()
	body, offered, e := mapper.Render(openai.ChatRequest{Messages: msgs, Tools: tools, ToolChoice: req.ToolChoice}, s.ReplyTo, id)
	if e != nil {
		fail(w, e)
		return
	}
	txt, e := s.Bridge.Ask(r.Context(), sess.Sock, body)
	if e != nil {
		fail(w, e)
		return
	}
	rep, e := mapper.ParseReply(txt, offered, newID)
	if e != nil {
		fail(w, e)
		return
	}
	ignoredHeader(w, ign)
	model := req.Model
	created := s.now().Unix()
	var out []any
	if len(rep.Calls) > 0 {
		for _, c := range rep.Calls {
			out = append(out, map[string]any{"type": "function_call", "id": "fc_" + c.ID, "call_id": c.ID, "name": c.Function.Name,
				"arguments": c.Function.Arguments, "status": "completed"})
		}
	} else {
		out = append(out, map[string]any{"type": "message", "id": "msg_" + id, "status": "completed", "role": "assistant",
			"content": []any{map[string]any{"type": "output_text", "text": rep.Text, "annotations": []any{}}}})
	}
	resp := func(status string, o []any) map[string]any {
		return map[string]any{"id": "resp_" + id, "object": "response", "created_at": created, "status": status,
			"model": model, "output": o, "parallel_tool_calls": false, "tool_choice": "auto", "tools": []any{}, "error": nil,
			"incomplete_details": nil, "instructions": nil, "metadata": map[string]any{}}
	}
	final := resp("completed", out)
	if req.Store == nil || *req.Store {
		s.store.put("resp_"+id, final)
	}
	if !req.Stream {
		writeJSON(w, final)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	seq := 0
	ev := func(name string, v map[string]any) {
		v["type"], v["sequence_number"] = name, seq
		seq++
		b, _ := json.Marshal(v)
		_, _ = w.Write([]byte("event: " + name + "\ndata: " + string(b) + "\n\n"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}
	ev("response.created", map[string]any{"response": resp("in_progress", []any{})})
	for i, o := range out {
		item := o.(map[string]any)
		ev("response.output_item.added", map[string]any{"output_index": i, "item": item})
		if item["type"] == "message" {
			ev("response.output_text.delta", map[string]any{"item_id": item["id"], "output_index": i, "content_index": 0, "delta": rep.Text})
			ev("response.output_text.done", map[string]any{"item_id": item["id"], "output_index": i, "content_index": 0, "text": rep.Text})
		} else {
			ev("response.function_call_arguments.delta", map[string]any{"item_id": item["id"], "output_index": i, "delta": item["arguments"]})
			ev("response.function_call_arguments.done", map[string]any{"item_id": item["id"], "output_index": i, "arguments": item["arguments"]})
		}
		ev("response.output_item.done", map[string]any{"output_index": i, "item": item})
	}
	ev("response.completed", map[string]any{"response": final})
}

func (s *Server) getResponse(w http.ResponseWriter, r *http.Request) {
	if v, ok := s.store.get(r.PathValue("id")); ok {
		writeJSON(w, v)
		return
	}
	fail(w, apierr.New(apierr.RoleNotFound, "", "no stored response "+r.PathValue("id")).WithCode("not_found"))
}

func (s *Server) deleteResponse(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.store.del(id) {
		fail(w, apierr.New(apierr.RoleNotFound, "", "no stored response "+id).WithCode("not_found"))
		return
	}
	writeJSON(w, map[string]any{"id": id, "object": "response", "deleted": true})
}

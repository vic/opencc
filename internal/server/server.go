// Package server is the OpenAI HTTP surface. Handlers are thin: auth, role,
// map, ask, map, write.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"opencc/internal/apierr"
	"opencc/internal/mapper"
	"opencc/internal/openai"
	"opencc/internal/registry"
	"opencc/internal/role"
)

const maxBody = 2 << 20

type Server struct {
	SessDir, ProcRoot string
	Bridge            *Bridge
	ReplyTo           string // wire address of opencc's socket
	now               func() time.Time
	store             respStore
}

func New(sessDir, procRoot string, b *Bridge) *Server {
	return &Server{SessDir: sessDir, ProcRoot: procRoot, Bridge: b, ReplyTo: b.Own.String(), now: time.Now}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", s.chat)
	mux.HandleFunc("POST /v1/completions", s.completion)
	mux.HandleFunc("POST /v1/responses", s.responses)
	mux.HandleFunc("POST /v1/embeddings", s.embeddings)
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("GET /v1/models/{id}", s.model)
	mux.HandleFunc("DELETE /v1/models/{id}", s.deleteModel)
	mux.HandleFunc("GET /v1/responses/{id}", s.getResponse)
	mux.HandleFunc("DELETE /v1/responses/{id}", s.deleteResponse)
	mux.HandleFunc("/v1/", s.catchAll)
	return s.outer(mux)
}

func fail(w http.ResponseWriter, e *apierr.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status())
	_, _ = w.Write(e.Body())
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// session authenticates the key as a role and resolves it to one live session.
func (s *Server) session(r *http.Request) (registry.Session, *apierr.Error) {
	key, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || key == "" {
		return registry.Session{}, apierr.New(apierr.NoAuth, "", "send the session name as Authorization: Bearer <role>")
	}
	rl, err := role.Parse(key)
	if err != nil {
		return registry.Session{}, apierr.New(apierr.BadRequest, "Authorization", "role name: "+err.Error())
	}
	ss, err := registry.List(s.SessDir, s.ProcRoot)
	if err != nil {
		return registry.Session{}, apierr.New(apierr.SocketSend, "", "session registry unreadable")
	}
	hit, n := registry.Resolve(ss, rl)
	switch n {
	case 0:
		return registry.Session{}, apierr.New(apierr.RoleNotFound, "", "no live session named "+rl.String())
	case 1:
		return hit, nil
	}
	return registry.Session{}, apierr.New(apierr.RoleAmbiguous, "", "more than one live session named "+rl.String())
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, *apierr.Error) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		return nil, apierr.New(apierr.BadRequest, "", "body unreadable or larger than 2 MiB")
	}
	return b, nil
}

func ignoredHeader(w http.ResponseWriter, keys []string) {
	if len(keys) > 0 {
		w.Header().Set("X-Opencc-Ignored", strings.Join(keys, ","))
	}
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	sess, e := s.session(r)
	if e != nil {
		fail(w, e)
		return
	}
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
	var req openai.ChatRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		fail(w, apierr.New(apierr.BadRequest, "", "invalid JSON: "+err.Error()))
		return
	}
	id := newID()
	body, offered, e := mapper.Render(req, s.ReplyTo, id)
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
	if model == "" {
		model = sess.Role.String()
	}
	created := s.now().Unix()
	if req.Stream {
		streamChat(w, "chatcmpl-"+id, model, created, rep)
		return
	}
	msg := openai.RespMessage{Role: "assistant", ToolCalls: rep.Calls}
	fin := "stop"
	if len(rep.Calls) > 0 {
		fin = "tool_calls"
	} else {
		msg.Content = &rep.Text
	}
	writeJSON(w, openai.ChatResponse{ID: "chatcmpl-" + id, Object: "chat.completion", Created: created, Model: model,
		Choices: []openai.ChatChoice{{Message: msg, FinishReason: fin}}})
}

func sse(w http.ResponseWriter, v any) {
	b, _ := json.Marshal(v)
	_, _ = w.Write([]byte("data: " + string(b) + "\n\n"))
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// streamChat writes the whole reply as one content event, a finish event, then [DONE].
func streamChat(w http.ResponseWriter, id, model string, created int64, rep mapper.Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunk := func(d openai.Delta, fin *string) openai.ChatChunk {
		return openai.ChatChunk{ID: id, Object: "chat.completion.chunk", Created: created, Model: model,
			Choices: []openai.ChunkChoice{{Delta: d, FinishReason: fin}}}
	}
	fin := "stop"
	if len(rep.Calls) > 0 {
		fin = "tool_calls"
		var dt []openai.DeltaToolCall
		for i, c := range rep.Calls {
			d := openai.DeltaToolCall{Index: i, ID: c.ID, Type: c.Type}
			d.Function.Name, d.Function.Arguments = c.Function.Name, c.Function.Arguments
			dt = append(dt, d)
		}
		sse(w, chunk(openai.Delta{Role: "assistant", ToolCalls: dt}, nil))
	} else {
		sse(w, chunk(openai.Delta{Role: "assistant", Content: &rep.Text}, nil))
	}
	sse(w, chunk(openai.Delta{}, &fin))
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func (s *Server) completion(w http.ResponseWriter, r *http.Request) {
	sess, e := s.session(r)
	if e != nil {
		fail(w, e)
		return
	}
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
	var req openai.CompletionRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		fail(w, apierr.New(apierr.BadRequest, "", "invalid JSON: "+err.Error()))
		return
	}
	id := newID()
	body, e := mapper.RenderPrompt(req.Prompt, s.ReplyTo, id)
	if e != nil {
		fail(w, e)
		return
	}
	txt, e := s.Bridge.Ask(r.Context(), sess.Sock, body)
	if e != nil {
		fail(w, e)
		return
	}
	ignoredHeader(w, ign)
	model := req.Model
	if model == "" {
		model = sess.Role.String()
	}
	fin := "stop"
	resp := openai.CompletionResponse{ID: "cmpl-" + id, Object: "text_completion", Created: s.now().Unix(), Model: model,
		Choices: []openai.CompletionChoice{{Text: txt, FinishReason: &fin}}}
	if !req.Stream {
		writeJSON(w, resp)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	sse(w, resp)
	_, _ = w.Write([]byte("data: [DONE]\n\n"))
}

func (s *Server) embeddings(w http.ResponseWriter, r *http.Request) {
	if _, e := s.session(r); e != nil {
		fail(w, e)
		return
	}
	fail(w, apierr.New(apierr.NotImplemented, "", "embeddings have no backend: a Claude session cannot make vectors"))
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	ss, err := registry.List(s.SessDir, s.ProcRoot)
	if err != nil {
		fail(w, apierr.New(apierr.SocketSend, "", "session registry unreadable"))
		return
	}
	out := openai.ModelList{Object: "list", Data: []openai.Model{}}
	seen := map[string]bool{}
	for _, x := range ss {
		if n := x.Role.String(); !seen[n] {
			seen[n] = true
			out.Data = append(out.Data, openai.Model{ID: n, Object: "model", Created: s.now().Unix(), OwnedBy: "claude-code"})
		}
	}
	writeJSON(w, out)
}

func (s *Server) model(w http.ResponseWriter, r *http.Request) {
	rl, err := role.Parse(r.PathValue("id"))
	ss, lerr := registry.List(s.SessDir, s.ProcRoot)
	if err != nil || lerr != nil {
		fail(w, apierr.New(apierr.RoleNotFound, "", "no live session named "+r.PathValue("id")))
		return
	}
	if _, n := registry.Resolve(ss, rl); n == 0 {
		fail(w, apierr.New(apierr.RoleNotFound, "", "no live session named "+rl.String()))
		return
	}
	writeJSON(w, openai.Model{ID: rl.String(), Object: "model", Created: s.now().Unix(), OwnedBy: "claude-code"})
}

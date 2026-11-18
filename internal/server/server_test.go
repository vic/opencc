package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"opencc/internal/peer"
)

type env struct {
	srv  *httptest.Server
	sess string
	dir  string
}

// fakeRole registers a session named name (pid) and answers every frame with reply(body).
// reply == nil: never answers.
func (e *env) fakeRole(t *testing.T, name string, pid int, reply func(string) string) {
	t.Helper()
	sock := filepath.Join(e.dir, fmt.Sprintf("%d.sock", pid))
	l, err := peer.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	addr, _ := peer.PathAddr(sock)
	j := fmt.Sprintf(`{"pid":%d,"name":%q,"status":"idle","messagingSocketPath":%q}`, pid, name, sock)
	if err := os.WriteFile(filepath.Join(e.sess, fmt.Sprintf("%d.json", pid)), []byte(j), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			sc := c.Lines()
			for sc.Scan() {
				in, err := peer.ParseFrame(sc.Bytes())
				if err != nil || reply == nil {
					continue
				}
				f, _ := peer.NewUserFrame(addr, name, peer.Prompting, reply(in.Body))
				_ = peer.Send(in.From.Path(), f.Bytes())
			}
			c.Close()
		}
	}()
}

func newEnv(t *testing.T, timeout time.Duration) *env {
	t.Helper()
	dir, err := os.MkdirTemp("", "oc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sess := filepath.Join(dir, "sessions")
	_ = os.Mkdir(sess, 0o700)
	own, _ := peer.PathAddr(filepath.Join(dir, "opencc.sock"))
	l, err := peer.Listen(own.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	b := NewBridge(own, "opencc", peer.Prompting, timeout)
	go b.Serve(l)
	e := &env{httptest.NewServer(New(sess, "/proc", b).Handler()), sess, dir}
	t.Cleanup(e.srv.Close)
	return e
}

func (e *env) post(t *testing.T, path, key, body string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(body))
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, b
}

const chatBody = `{"model":"gpt-4o","temperature":0.2,"messages":[{"role":"system","content":"be brief"},{"role":"user","content":"hi"}]}`

func errCode(t *testing.T, b []byte) string {
	t.Helper()
	var x struct{ Error struct{ Code, Type string } }
	if json.Unmarshal(b, &x) != nil || x.Error.Type == "" {
		t.Fatalf("not an OpenAI error object: %s", b)
	}
	return x.Error.Code
}

func TestChatOK(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	var got string
	e.fakeRole(t, "reviewer", os.Getpid(), func(b string) string { got = b; return "hello there" })
	st, h, b := e.post(t, "/v1/chat/completions", "reviewer", chatBody)
	if st != 200 {
		t.Fatalf("status %d %s", st, b)
	}
	var r struct {
		Object  string
		Model   string
		Choices []struct {
			Message      struct{ Role, Content string }
			FinishReason string `json:"finish_reason"`
		}
	}
	if err := json.Unmarshal(b, &r); err != nil || len(r.Choices) != 1 {
		t.Fatalf("bad body %s", b)
	}
	if r.Object != "chat.completion" || r.Model != "gpt-4o" || r.Choices[0].Message.Content != "hello there" || r.Choices[0].FinishReason != "stop" {
		t.Fatalf("bad body %s", b)
	}
	if h.Get("X-Opencc-Ignored") != "temperature" {
		t.Fatalf("ignored header %q", h.Get("X-Opencc-Ignored"))
	}
	if !strings.Contains(got, "system: be brief") || !strings.Contains(got, "user: hi") {
		t.Fatalf("body to role: %s", got)
	}
}

func TestAuthErrors(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "reviewer", os.Getpid(), func(string) string { return "x" })
	for _, c := range []struct {
		key  string
		st   int
		code string
	}{{"", 401, "invalid_api_key"}, {"nobody", 404, "model_not_found"}, {"<x>", 400, "invalid_request"}} {
		st, _, b := e.post(t, "/v1/chat/completions", c.key, chatBody)
		if st != c.st || errCode(t, b) != c.code {
			t.Errorf("key %q: %d %s", c.key, st, b)
		}
	}
}

func TestAmbiguousRole(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "dup", os.Getpid(), func(string) string { return "x" })
	e.fakeRole(t, "dup", 1, func(string) string { return "x" })
	st, _, b := e.post(t, "/v1/chat/completions", "dup", chatBody)
	if st != 409 || errCode(t, b) != "role_ambiguous" {
		t.Fatalf("%d %s", st, b)
	}
}

func TestTimeout(t *testing.T) {
	e := newEnv(t, 300*time.Millisecond)
	e.fakeRole(t, "mute", os.Getpid(), nil)
	t0 := time.Now()
	st, _, b := e.post(t, "/v1/chat/completions", "mute", chatBody)
	if st != 504 || errCode(t, b) != "reply_timeout" || time.Since(t0) > 1300*time.Millisecond {
		t.Fatalf("%d %s after %s", st, b, time.Since(t0))
	}
}

func TestRejectedParam(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string { return "x" })
	st, _, b := e.post(t, "/v1/chat/completions", "r", `{"n":2,"messages":[{"role":"user","content":"a"}]}`)
	if st != 400 || errCode(t, b) != "unsupported_parameter" {
		t.Fatalf("%d %s", st, b)
	}
	st, _, b = e.post(t, "/v1/chat/completions", "r", `{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}`)
	if st != 400 || errCode(t, b) != "unsupported_content" {
		t.Fatalf("%d %s", st, b)
	}
}

func TestToolCalls(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string {
		return `{"tool_calls":[{"name":"get_time","arguments":{"tz":"UTC"}}]}`
	})
	body := `{"messages":[{"role":"user","content":"time?"}],"tools":[{"type":"function","function":{"name":"get_time","parameters":{"type":"object"}}}]}`
	st, _, b := e.post(t, "/v1/chat/completions", "r", body)
	if st != 200 || !strings.Contains(string(b), `"finish_reason":"tool_calls"`) || !strings.Contains(string(b), `"arguments":"{\"tz\":\"UTC\"}"`) {
		t.Fatalf("%d %s", st, b)
	}
	// negative control: unknown tool is a typed error
	e2 := newEnv(t, 2*time.Second)
	e2.fakeRole(t, "r", os.Getpid(), func(string) string { return `{"tool_calls":[{"name":"rm","arguments":{}}]}` })
	st, _, b = e2.post(t, "/v1/chat/completions", "r", body)
	if st != 502 || errCode(t, b) != "unknown_tool" {
		t.Fatalf("%d %s", st, b)
	}
}

func TestStream(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string { return "streamed" })
	st, h, b := e.post(t, "/v1/chat/completions", "r", `{"stream":true,"messages":[{"role":"user","content":"a"}]}`)
	s := string(b)
	if st != 200 || h.Get("Content-Type") != "text/event-stream" || !strings.Contains(s, `"content":"streamed"`) ||
		!strings.Contains(s, `"finish_reason":"stop"`) || !strings.HasSuffix(s, "data: [DONE]\n\n") {
		t.Fatalf("%d %s", st, s)
	}
}

func TestLegacyCompletionAndModels(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string { return "done" })
	st, _, b := e.post(t, "/v1/completions", "r", `{"prompt":"say"}`)
	if st != 200 || !strings.Contains(string(b), `"text":"done"`) || !strings.Contains(string(b), `"object":"text_completion"`) {
		t.Fatalf("%d %s", st, b)
	}
	r, _ := http.Get(e.srv.URL + "/v1/models")
	mb, _ := io.ReadAll(r.Body)
	if !strings.Contains(string(mb), `"id":"r"`) {
		t.Fatalf("models %s", mb)
	}
	r, _ = http.Get(e.srv.URL + "/v1/models/ghost")
	if r.StatusCode != 404 {
		t.Fatalf("model ghost %d", r.StatusCode)
	}
	st, _, b = e.post(t, "/v1/embeddings", "r", `{}`)
	if st != 501 || errCode(t, b) != "not_implemented" {
		t.Fatalf("%d %s", st, b)
	}
}

func TestToolCallFenced(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string {
		return "```json\n{\"tool_calls\":[{\"name\":\"get_time\",\"arguments\":{}}]}\n```"
	})
	body := `{"messages":[{"role":"user","content":"t"}],"tools":[{"type":"function","function":{"name":"get_time"}}]}`
	st, _, b := e.post(t, "/v1/chat/completions", "r", body)
	if st != 200 || !strings.Contains(string(b), `"finish_reason":"tool_calls"`) {
		t.Fatalf("%d %s", st, b)
	}
	// control: fenced prose that is not the envelope stays text
	e2 := newEnv(t, 2*time.Second)
	e2.fakeRole(t, "r", os.Getpid(), func(string) string { return "```\nhello\n```" })
	_, _, b = e2.post(t, "/v1/chat/completions", "r", body)
	if !strings.Contains(string(b), `"finish_reason":"stop"`) {
		t.Fatalf("%s", b)
	}
}

func TestResponses(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	var got string
	e.fakeRole(t, "r", os.Getpid(), func(b string) string { got = b; return "resp text" })
	st, _, b := e.post(t, "/v1/responses", "r", `{"model":"m","instructions":"terse","input":[{"role":"user","content":[{"type":"input_text","text":"yo"}]}]}`)
	if st != 200 || !strings.Contains(string(b), `"object":"response"`) || !strings.Contains(string(b), `"text":"resp text"`) ||
		!strings.Contains(got, "system: terse") || !strings.Contains(got, "user: yo") {
		t.Fatalf("%d %s\n%s", st, b, got)
	}
	st, _, b = e.post(t, "/v1/responses", "r", `{"input":"s","stream":true}`)
	if st != 200 || !strings.Contains(string(b), "event: response.output_text.delta") || !strings.Contains(string(b), "event: response.completed") {
		t.Fatalf("%d %s", st, b)
	}
	// control: a hosted tool type is refused
	st, _, b = e.post(t, "/v1/responses", "r", `{"input":"s","tools":[{"type":"web_search"}]}`)
	if st != 400 || errCode(t, b) != "unsupported_parameter" {
		t.Fatalf("%d %s", st, b)
	}
}

func (e *env) do(t *testing.T, method, path, key string) (int, http.Header, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, r.Header, b
}

func TestEveryRouteAnswersOpenAIShape(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string { return "ok" })
	for _, c := range []struct {
		method, path string
		st           int
		code         string
	}{
		{"POST", "/v1/audio/speech", 501, "not_implemented"},
		{"POST", "/v1/audio/transcriptions", 501, "not_implemented"},
		{"POST", "/v1/images/generations", 501, "not_implemented"},
		{"POST", "/v1/moderations", 501, "not_implemented"},
		{"GET", "/v1/files", 501, "not_implemented"},
		{"POST", "/v1/batches", 501, "not_implemented"},
		{"POST", "/v1/fine_tuning/jobs", 501, "not_implemented"},
		{"POST", "/v1/assistants", 501, "not_implemented"},
		{"POST", "/v1/threads/t1/runs", 501, "not_implemented"},
		{"POST", "/v1/vector_stores", 501, "not_implemented"},
		{"DELETE", "/v1/models/r", 501, "not_implemented"},
		{"GET", "/v1/chat/completions", 405, "method_not_allowed"},
		{"GET", "/v1/nonsense", 404, "unknown_url"},
		{"GET", "/v1/responses/resp_nope", 404, "not_found"},
	} {
		st, _, b := e.do(t, c.method, c.path, "r")
		if st != c.st || errCode(t, b) != c.code {
			t.Errorf("%s %s: %d %s", c.method, c.path, st, b)
		}
	}
}

func TestBaseURLWithoutV1AndCORS(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(string) string { return "ok" })
	st, h, b := e.post(t, "/chat/completions", "r", `{"messages":[{"role":"user","content":"a"}]}`)
	if st != 200 || !strings.Contains(string(b), `"content":"ok"`) || !strings.HasPrefix(h.Get("X-Request-Id"), "req_") || h.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("%d %v %s", st, h, b)
	}
	st, h, _ = e.do(t, "OPTIONS", "/v1/chat/completions", "")
	if st != 204 || h.Get("Access-Control-Allow-Methods") == "" {
		t.Fatalf("preflight %d %v", st, h)
	}
}

func TestResponsesToolsAndStore(t *testing.T) {
	e := newEnv(t, 2*time.Second)
	e.fakeRole(t, "r", os.Getpid(), func(b string) string {
		if strings.Contains(b, "tool(call_1): 72F") {
			return "It is 72F."
		}
		return "```json\n{\"tool_calls\":[{\"name\":\"get_weather\",\"arguments\":{\"city\":\"Rome\"}}]}\n```"
	})
	tools := `"tools":[{"type":"function","name":"get_weather","description":"w","parameters":{"type":"object"}}]`
	st, _, b := e.post(t, "/v1/responses", "r", `{"input":"weather in Rome?",`+tools+`}`)
	var r struct {
		ID     string
		Output []struct{ Type, Name string }
	}
	if st != 200 || json.Unmarshal(b, &r) != nil || len(r.Output) != 1 || r.Output[0].Type != "function_call" || r.Output[0].Name != "get_weather" {
		t.Fatalf("%d %s", st, b)
	}
	// second turn: send the call and its output back
	in := fmt.Sprintf(`{"input":[{"role":"user","content":"weather in Rome?"},{"type":"function_call","call_id":"call_1","name":"get_weather","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"72F"}],%s}`, tools)
	st, _, b = e.post(t, "/v1/responses", "r", in)
	if st != 200 || !strings.Contains(string(b), `"text":"It is 72F."`) {
		t.Fatalf("%d %s", st, b)
	}
	// stored: GET returns it, DELETE removes it, GET again is 404 (control)
	if st, _, b = e.do(t, "GET", "/v1/responses/"+r.ID, "r"); st != 200 || !strings.Contains(string(b), r.ID) {
		t.Fatalf("get %d %s", st, b)
	}
	if st, _, b = e.do(t, "DELETE", "/v1/responses/"+r.ID, "r"); st != 200 || !strings.Contains(string(b), `"deleted":true`) {
		t.Fatalf("delete %d %s", st, b)
	}
	if st, _, _ = e.do(t, "GET", "/v1/responses/"+r.ID, "r"); st != 404 {
		t.Fatalf("get after delete %d", st)
	}
}

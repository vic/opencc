package server

import (
	"net/http"
	"strings"

	"opencc/internal/apierr"
)

// unsupported lists every other OpenAI API path family. A Claude session makes
// text only, so each answers a typed 501 instead of a bare 404.
var unsupported = []string{
	"/v1/audio/", "/v1/images/", "/v1/moderations", "/v1/files", "/v1/uploads", "/v1/batches",
	"/v1/fine_tuning/", "/v1/assistants", "/v1/threads", "/v1/vector_stores", "/v1/evals",
	"/v1/realtime", "/v1/containers", "/v1/organization", "/v1/conversations", "/v1/chatkit",
	"/v1/videos", "/v1/skills", "/v1/responses/compact", "/v1/fine-tunes", "/v1/engines",
}

// served maps each served path to its methods, for 405 answers.
var served = map[string]string{
	"/v1/chat/completions": "POST", "/v1/completions": "POST", "/v1/responses": "POST", "/v1/embeddings": "POST",
	"/v1/models": "GET",
}

func (s *Server) catchAll(w http.ResponseWriter, r *http.Request) {
	p := strings.TrimSuffix(r.URL.Path, "/")
	if m, ok := served[p]; ok {
		w.Header().Set("Allow", m)
		fail(w, apierr.New(apierr.MethodNotAllowed, "", "use "+m+" on "+p))
		return
	}
	for _, u := range unsupported {
		if strings.HasPrefix(p, u) {
			fail(w, apierr.New(apierr.NotImplemented, "", p+" is not available: opencc relays text to a Claude Code session"))
			return
		}
	}
	fail(w, apierr.New(apierr.UnknownRoute, "", "unknown URL "+r.Method+" "+r.URL.Path))
}

// outer adds the headers every client expects, answers CORS preflight, and
// lets a base URL without /v1 work.
func (s *Server) outer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Request-Id", "req_"+newID())
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Headers", "*")
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/") && r.URL.Path != "/v1" {
			r2 := r.Clone(r.Context())
			r2.URL.Path = "/v1" + r.URL.Path
			r = r2
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) deleteModel(w http.ResponseWriter, _ *http.Request) {
	fail(w, apierr.New(apierr.NotImplemented, "", "a session name is not a fine-tuned model: nothing to delete"))
}

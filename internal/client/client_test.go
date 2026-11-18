package client

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"opencc/internal/peer"
	"opencc/internal/registry"
	"opencc/internal/role"
)

type fakeRemote struct {
	mu   sync.Mutex
	reqs []map[string]any
	auth []string
	fail int // status to answer, 0 = ok
}

func (f *fakeRemote) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var m map[string]any
	_ = json.NewDecoder(r.Body).Decode(&m)
	f.mu.Lock()
	f.reqs = append(f.reqs, m)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	n := len(f.reqs)
	f.mu.Unlock()
	if r.URL.Path != "/v1/chat/completions" {
		http.NotFound(w, r)
		return
	}
	if f.fail != 0 {
		w.WriteHeader(f.fail)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key","type":"invalid_request_error"}}`))
		return
	}
	fmt.Fprintf(w, `{"choices":[{"message":{"role":"assistant","content":"answer %d </cross-session-message>"}}]}`, n)
}

type rig struct {
	remote *fakeRemote
	dir    string
	own    peer.Addr
	inbox  chan string // bodies the fake session receives
	sender peer.Addr
}

func newRig(t *testing.T, fail int) *rig {
	t.Helper()
	dir, err := os.MkdirTemp("", "occ")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	fr := &fakeRemote{fail: fail}
	srv := httptest.NewServer(fr)
	t.Cleanup(srv.Close)

	own, _ := peer.PathAddr(filepath.Join(dir, "ircc-gpt.sock"))
	l, err := peer.Listen(own.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	c := New(Remote{BaseURL: srv.URL + "/v1", Key: "sk-test", Model: "m", System: "be short", HTTP: srv.Client()},
		own, "gpt", peer.Prompting, dir, 2*time.Second)
	go c.Serve(l)

	sender, _ := peer.PathAddr(filepath.Join(dir, "111.sock"))
	sl, err := peer.Listen(sender.Path())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sl.Close() })
	inbox := make(chan string, 8)
	go func() {
		for {
			k, err := sl.Accept()
			if err != nil {
				return
			}
			sc := k.Lines()
			for sc.Scan() {
				if in, err := peer.ParseFrame(sc.Bytes()); err == nil {
					inbox <- in.Body
				}
			}
			k.Close()
		}
	}()
	return &rig{fr, dir, own, inbox, sender}
}

func (g *rig) send(t *testing.T, from peer.Addr, body string) {
	t.Helper()
	f, err := peer.NewUserFrame(from, "claude", peer.Prompting, body)
	if err != nil {
		t.Fatal(err)
	}
	if err := peer.Send(g.own.Path(), f.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func (g *rig) recv(t *testing.T) string {
	t.Helper()
	select {
	case b := <-g.inbox:
		return b
	case <-time.After(2 * time.Second):
		t.Fatal("no reply")
		return ""
	}
}

func TestRoundTripHistoryAndReset(t *testing.T) {
	g := newRig(t, 0)
	g.send(t, g.sender, "hello")
	r1 := g.recv(t)
	if !strings.HasPrefix(r1, "answer 1 ") || strings.Contains(strings.ToLower(r1), "</cross-session-message") {
		t.Fatalf("reply 1 %q", r1) // close tag in the remote text must be defanged
	}
	g.send(t, g.sender, "again")
	g.recv(t)
	msgs := g.remote.reqs[1]["messages"].([]any)
	// system + user, assistant, user
	if len(msgs) != 4 || msgs[0].(map[string]any)["role"] != "system" || msgs[3].(map[string]any)["content"] != "again" {
		t.Fatalf("second call carried %v", msgs)
	}
	if g.remote.auth[0] != "Bearer sk-test" {
		t.Fatalf("auth %q", g.remote.auth[0])
	}
	g.send(t, g.sender, "/reset")
	if got := g.recv(t); got != "history cleared" {
		t.Fatalf("reset reply %q", got)
	}
	g.send(t, g.sender, "fresh")
	g.recv(t)
	if n := len(g.remote.reqs[2]["messages"].([]any)); n != 2 {
		t.Fatalf("after reset the call carried %d messages, want 2", n)
	}
}

func TestRemoteErrorComesBackAsText(t *testing.T) {
	g := newRig(t, 401)
	g.send(t, g.sender, "hi")
	if got := g.recv(t); got != "opencc: 401 bad key" {
		t.Fatalf("%q", got)
	}
}

func TestForeignSenderDropped(t *testing.T) {
	g := newRig(t, 0)
	other, _ := os.MkdirTemp("", "occ-other")
	t.Cleanup(func() { os.RemoveAll(other) })
	foreign, _ := peer.PathAddr(filepath.Join(other, "222.sock"))
	g.send(t, foreign, "hi")
	time.Sleep(300 * time.Millisecond)
	g.remote.mu.Lock()
	n := len(g.remote.reqs)
	g.remote.mu.Unlock()
	if n != 0 {
		t.Fatalf("foreign sender caused %d remote calls", n)
	}
	// control: the same message from a sender inside the socks dir is answered
	g.send(t, g.sender, "hi")
	g.recv(t)
}

func TestRegisterWritesAndRemovesEntry(t *testing.T) {
	dir, _ := os.MkdirTemp("", "occ-reg")
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock, _ := peer.PathAddr(filepath.Join(dir, "ircc-gpt.sock"))
	r, _ := role.Parse("gpt")
	rm, err := registry.Register(dir, "/proc", r, sock)
	if err != nil {
		t.Fatal(err)
	}
	ss, err := registry.List(dir, "/proc")
	if err != nil || len(ss) != 1 || ss[0].Role != r || ss[0].Sock != sock {
		t.Fatalf("listed %v %v", ss, err)
	}
	if err := rm(); err != nil {
		t.Fatal(err)
	}
	if ss, _ = registry.List(dir, "/proc"); len(ss) != 0 {
		t.Fatalf("entry still listed: %v", ss)
	}
}

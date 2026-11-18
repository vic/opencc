package client

import (
	"context"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"opencc/internal/peer"
)

const (
	maxHistory = 40      // messages kept per sender
	maxCalls   = 4       // remote calls at once
	maxBody    = 1 << 19 // reply bytes sent back; the frame limit is 1 MiB
)

// Client answers Claude peers with a Remote.
type Client struct {
	Remote   Remote
	Own      peer.Addr
	Name     string
	Mode     peer.Class
	SocksDir string
	Timeout  time.Duration

	mu    sync.Mutex
	conv  map[string]*conv
	calls chan struct{}
}

type conv struct {
	mu   sync.Mutex // one request at a time per sender
	msgs []Msg
}

func New(r Remote, own peer.Addr, name string, mode peer.Class, socksDir string, timeout time.Duration) *Client {
	return &Client{Remote: r, Own: own, Name: name, Mode: mode, SocksDir: socksDir, Timeout: timeout,
		conv: map[string]*conv{}, calls: make(chan struct{}, maxCalls)}
}

// Serve reads frames from l until it closes.
func (c *Client) Serve(l *peer.Listener) {
	for {
		k, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer k.Close()
			_ = k.SetDeadline(time.Now().Add(10 * time.Second))
			sc := k.Lines()
			for sc.Scan() {
				in, err := peer.ParseFrame(sc.Bytes())
				if err != nil || !c.allowed(in.From) {
					continue
				}
				go c.handle(in)
			}
		}()
	}
}

// allowed: a reply only goes to a socket in our socks dir, never to ourselves.
func (c *Client) allowed(from peer.Addr) bool {
	return filepath.Dir(from.Path()) == c.SocksDir && from != c.Own
}

func (c *Client) conversation(sender string) *conv {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.conv[sender]
	if !ok {
		v = &conv{}
		c.conv[sender] = v
	}
	return v
}

func (c *Client) handle(in peer.Inbound) {
	v := c.conversation(in.From.Path())
	v.mu.Lock()
	defer v.mu.Unlock()
	c.reply(in.From, c.answer(v, in.Body))
}

func (c *Client) answer(v *conv, body string) string {
	if strings.TrimSpace(body) == "/reset" {
		v.msgs = nil
		return "history cleared"
	}
	c.calls <- struct{}{}
	defer func() { <-c.calls }()
	ctx, cancel := context.WithTimeout(context.Background(), c.Timeout)
	defer cancel()
	next := append(append([]Msg(nil), v.msgs...), Msg{"user", body})
	text, err := c.Remote.Complete(ctx, next)
	if err != nil {
		return err.Error() // history stays as it was: the failed turn is not kept
	}
	v.msgs = append(next, Msg{"assistant", text})
	if len(v.msgs) > maxHistory {
		v.msgs = v.msgs[len(v.msgs)-maxHistory:]
	}
	return text
}

var closeTag = regexp.MustCompile(`(?i)</cross-session-message`)

// frameText makes text safe for a frame body: no close tag, at most maxBody bytes.
func frameText(s string) string {
	s = closeTag.ReplaceAllStringFunc(s, func(m string) string { return "<​" + m[1:] })
	if len(s) > maxBody {
		s = strings.ToValidUTF8(s[:maxBody], "") + "\n[opencc: reply cut at 512 KiB]"
	}
	if strings.TrimSpace(s) == "" {
		s = "[opencc: the remote model sent an empty reply]"
	}
	return s
}

func (c *Client) reply(to peer.Addr, text string) {
	f, err := peer.NewUserFrame(c.Own, c.Name, c.Mode, frameText(text))
	if err != nil {
		return
	}
	_ = peer.Send(to.Path(), f.Bytes())
}

package server

import (
	"context"
	"sync"
	"time"

	"opencc/internal/apierr"
	"opencc/internal/peer"
)

// Bridge sends one frame to a session and waits for the reply frame on opencc's
// own socket. The session replies with SendMessage to our address, so the reply
// is a frame whose `from` is the session socket. There is no reply-to field,
// so at most one request per session is in flight (slot) and the first frame
// from that socket is the reply. A late second frame of an old request would
// be taken for the next reply: known limit (DESIGN OPEN-1).
type Bridge struct {
	Own      peer.Addr
	FromName string
	Mode     peer.Class
	Timeout  time.Duration

	mu    sync.Mutex
	slots map[string]chan struct{}
	waits map[string]chan string
}

func NewBridge(own peer.Addr, fromName string, mode peer.Class, timeout time.Duration) *Bridge {
	return &Bridge{Own: own, FromName: fromName, Mode: mode, Timeout: timeout,
		slots: map[string]chan struct{}{}, waits: map[string]chan string{}}
}

// Serve reads reply frames from l until it closes.
func (b *Bridge) Serve(l *peer.Listener) {
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go b.read(c)
	}
}

func (b *Bridge) read(c *peer.Conn) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	sc := c.Lines()
	for sc.Scan() {
		in, err := peer.ParseFrame(sc.Bytes())
		if err != nil {
			continue
		}
		b.mu.Lock()
		w := b.waits[in.From.Path()]
		b.mu.Unlock()
		if w != nil {
			select {
			case w <- in.Body:
			default: // second frame of one request: dropped
			}
		}
	}
}

func (b *Bridge) slot(sock string) chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	s, ok := b.slots[sock]
	if !ok {
		s = make(chan struct{}, 1)
		b.slots[sock] = s
	}
	return s
}

// Ask sends body to sock and returns the reply text.
func (b *Bridge) Ask(ctx context.Context, sock peer.Addr, body string) (string, *apierr.Error) {
	f, err := peer.NewUserFrame(b.Own, b.FromName, b.Mode, body)
	if err != nil {
		return "", apierr.New(apierr.BadRequest, "messages", err.Error())
	}
	if len(f.Bytes()) > 1<<20 {
		e := apierr.New(apierr.BadRequest, "messages", "conversation is larger than 1 MiB")
		e.Code = "context_length_exceeded"
		return "", e
	}
	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()
	s := b.slot(sock.Path())
	select {
	case s <- struct{}{}:
		defer func() { <-s }()
	case <-ctx.Done():
		return "", apierr.New(apierr.ReplyTimeout, "", "session is busy with another request")
	}
	w := make(chan string, 1)
	b.mu.Lock()
	b.waits[sock.Path()] = w
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waits, sock.Path())
		b.mu.Unlock()
	}()
	if err := peer.Send(sock.Path(), f.Bytes()); err != nil {
		return "", apierr.New(apierr.SocketSend, "", err.Error())
	}
	select {
	case r := <-w:
		return r, nil
	case <-ctx.Done():
		return "", apierr.New(apierr.ReplyTimeout, "", "no reply from the session in time")
	}
}

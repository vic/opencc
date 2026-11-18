// Command opencc serves the OpenAI API on loopback and relays each request to
// the Claude Code session named by the API key.
package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"opencc/internal/peer"
	"opencc/internal/server"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opencc:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 && os.Args[1] == "client" {
		return runClient(os.Args[2:])
	}
	home, _ := os.UserHomeDir()
	rt := os.Getenv("XDG_RUNTIME_DIR")
	listen := flag.String("listen", "127.0.0.1:8080", "loopback address")
	socks := flag.String("socks-dir", filepath.Join(rt, "cc-socks"), "dir of the Claude session sockets (replies only reach sockets in it)")
	sess := flag.String("sessions-dir", filepath.Join(home, ".claude", "sessions"), "Claude session registry")
	timeout := flag.Duration("timeout", 120*time.Second, "reply timeout per request")
	mode := flag.String("from-mode", "prompting", "sender class on frames: prompting|bypass")
	flag.Parse()

	host, _, err := net.SplitHostPort(*listen)
	if err != nil {
		return err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen %q: only loopback addresses are allowed", *listen)
	}
	class, err := peer.ClassOf(map[string]string{"prompting": "default", "bypass": "bypassPermissions"}[*mode])
	if err != nil {
		return fmt.Errorf("--from-mode %q: want prompting or bypass", *mode)
	}
	sock, err := peer.EndpointPath(*socks, "opencc")
	if err != nil {
		return err
	}
	own, err := peer.PathAddr(sock)
	if err != nil {
		return err
	}
	l, err := peer.Listen(sock)
	if err != nil {
		return err
	}
	defer l.Close()
	b := server.NewBridge(own, "opencc", class, *timeout)
	go b.Serve(l)

	srv := &http.Server{Addr: *listen, Handler: server.New(*sess, "/proc", b).Handler(), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-done; _ = srv.Close() }()
	fmt.Fprintf(os.Stderr, "opencc: http://%s  socket %s\n", *listen, sock)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

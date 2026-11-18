package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"opencc/internal/client"
	"opencc/internal/peer"
	"opencc/internal/registry"
	"opencc/internal/role"
)

// runClient: `opencc client` makes a Claude peer that answers with a remote OpenAI-compatible API.
func runClient(args []string) error {
	home, _ := os.UserHomeDir()
	rt := os.Getenv("XDG_RUNTIME_DIR")
	fs := flag.NewFlagSet("opencc client", flag.ExitOnError)
	name := fs.String("name", "", "name Claude sessions send to (required)")
	base := fs.String("base-url", "https://api.openai.com/v1", "OpenAI-compatible API, up to and including /v1")
	keyEnv := fs.String("api-key-env", "OPENAI_API_KEY", "environment variable that holds the API key (empty value: no key sent)")
	model := fs.String("model", "", "remote model id (required)")
	system := fs.String("system", "", "system prompt for every conversation")
	socks := fs.String("socks-dir", filepath.Join(rt, "cc-socks"), "dir of the Claude session sockets")
	sess := fs.String("sessions-dir", filepath.Join(home, ".claude", "sessions"), "Claude session registry")
	timeout := fs.Duration("timeout", 120*time.Second, "remote call timeout")
	mode := fs.String("from-mode", "prompting", "sender class on frames: prompting|bypass")
	_ = fs.Parse(args)

	if *model == "" {
		return fmt.Errorf("--model is required")
	}
	r, err := role.Parse(*name)
	if err != nil {
		return fmt.Errorf("--name: %w", err)
	}
	class, err := peer.ClassOf(map[string]string{"prompting": "default", "bypass": "bypassPermissions"}[*mode])
	if err != nil {
		return fmt.Errorf("--from-mode %q: want prompting or bypass", *mode)
	}
	sock, err := peer.EndpointPath(*socks, r.String())
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
	unregister, err := registry.Register(*sess, "/proc", r, own)
	if err != nil {
		return err
	}
	defer unregister()

	c := client.New(client.Remote{BaseURL: *base, Key: os.Getenv(*keyEnv), Model: *model, System: *system,
		HTTP: &http.Client{}}, own, r.String(), class, *socks, *timeout)
	go c.Serve(l)
	fmt.Fprintf(os.Stderr, "opencc client: %s -> %s (%s), socket %s\n", r, *base, *model, sock)
	done := make(chan os.Signal, 1)
	signal.Notify(done, syscall.SIGINT, syscall.SIGTERM)
	<-done
	return nil
}

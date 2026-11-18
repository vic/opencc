// Package client is opencc client mode: a Claude Code peer answered by a remote OpenAI-compatible API.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Msg is one chat message of a conversation.
type Msg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Remote is an OpenAI-compatible chat endpoint.
type Remote struct {
	BaseURL string // up to and including /v1
	Key     string // empty: no Authorization header
	Model   string
	System  string // empty: none
	HTTP    *http.Client
}

// RemoteError is a failed call: the HTTP status (0 = no response) and the message the remote gave.
type RemoteError struct {
	Status  int
	Message string
}

func (e *RemoteError) Error() string {
	if e.Status == 0 {
		return "opencc: " + e.Message
	}
	return fmt.Sprintf("opencc: %d %s", e.Status, e.Message)
}

// Complete sends msgs and returns the first choice's text.
func (r Remote) Complete(ctx context.Context, msgs []Msg) (string, *RemoteError) {
	all := msgs
	if r.System != "" {
		all = append([]Msg{{"system", r.System}}, msgs...)
	}
	body, _ := json.Marshal(map[string]any{"model": r.Model, "messages": all})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(r.BaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", &RemoteError{Message: err.Error()}
	}
	req.Header.Set("Content-Type", "application/json")
	if r.Key != "" {
		req.Header.Set("Authorization", "Bearer "+r.Key)
	}
	resp, err := r.HTTP.Do(req)
	if err != nil {
		// the key is only in a header, never in the URL, so the error text cannot leak it
		return "", &RemoteError{Message: err.Error()}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		var e struct{ Error struct{ Message string } }
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error.Message != "" {
			msg = e.Error.Message
		}
		if len(msg) > 500 {
			msg = msg[:500]
		}
		return "", &RemoteError{Status: resp.StatusCode, Message: msg}
	}
	var ok struct {
		Choices []struct {
			Message struct{ Content *string }
		}
	}
	if json.Unmarshal(b, &ok) != nil || len(ok.Choices) == 0 || ok.Choices[0].Message.Content == nil {
		return "", &RemoteError{Status: resp.StatusCode, Message: "reply has no choices[0].message.content"}
	}
	return *ok.Choices[0].Message.Content, nil
}

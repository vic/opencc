// Package openai holds the wire types of the OpenAI paths opencc serves.
package openai

import "encoding/json"

// ChatRequest is POST /v1/chat/completions. Fields opencc ignores or rejects
// are decided from the raw key set in package mapper (one table).
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Stream         bool            `json:"stream"`
	Tools          []Tool          `json:"tools"`
	ToolChoice     json.RawMessage `json:"tool_choice"`
	ResponseFormat *struct {
		Type       string          `json:"type"`
		JSONSchema json.RawMessage `json:"json_schema"`
	} `json:"response_format"`
}

// CompletionRequest is POST /v1/completions (legacy). Prompt is a string or a one-element string array.
type CompletionRequest struct {
	Model  string          `json:"model"`
	Prompt json.RawMessage `json:"prompt"`
	Stream bool            `json:"stream"`
}

type Message struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"` // string, array of parts, or null
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
}

type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatResponse is the non-stream chat completion. Usage is omitted: no count is known.
type ChatResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []ChatChoice `json:"choices"`
}

type ChatChoice struct {
	Index        int         `json:"index"`
	Logprobs     *struct{}   `json:"logprobs"` // always null: a session has no token logprobs
	Message      RespMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type RespMessage struct {
	Role      string     `json:"role"`
	Content   *string    `json:"content"`
	Refusal   *string    `json:"refusal"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ChatChunk is one stream event of chat.completion.chunk.
type ChatChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

type ChunkChoice struct {
	Index        int     `json:"index"`
	Delta        Delta   `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type Delta struct {
	Role      string          `json:"role,omitempty"`
	Content   *string         `json:"content,omitempty"`
	ToolCalls []DeltaToolCall `json:"tool_calls,omitempty"`
}

type DeltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// CompletionResponse is the legacy text_completion body (also the stream chunk shape).
type CompletionResponse struct {
	ID      string             `json:"id"`
	Object  string             `json:"object"`
	Created int64              `json:"created"`
	Model   string             `json:"model"`
	Choices []CompletionChoice `json:"choices"`
}

type CompletionChoice struct {
	Index        int     `json:"index"`
	Text         string  `json:"text"`
	Logprobs     *string `json:"logprobs"`
	FinishReason *string `json:"finish_reason"`
}

type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type ModelList struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

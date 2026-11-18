// Package apierr holds the closed set of opencc errors and their OpenAI wire shape.
package apierr

import (
	"encoding/json"
	"net/http"
)

// Kind is the closed set of error cases. Add a case here and in table.
type Kind uint8

const (
	NoAuth Kind = iota
	RoleNotFound
	RoleAmbiguous
	BadRequest
	UnsupportedParam
	NotImplemented
	SocketSend
	ReplyTimeout
	ReplyMalformed
	UnknownRoute
	MethodNotAllowed
)

var table = [...]struct {
	status int
	typ    string
	code   string
}{
	NoAuth:           {http.StatusUnauthorized, "invalid_request_error", "invalid_api_key"},
	RoleNotFound:     {http.StatusNotFound, "invalid_request_error", "model_not_found"},
	RoleAmbiguous:    {http.StatusConflict, "invalid_request_error", "role_ambiguous"},
	BadRequest:       {http.StatusBadRequest, "invalid_request_error", "invalid_request"},
	UnsupportedParam: {http.StatusBadRequest, "invalid_request_error", "unsupported_parameter"},
	NotImplemented:   {http.StatusNotImplemented, "server_error", "not_implemented"},
	SocketSend:       {http.StatusBadGateway, "server_error", "socket_send_failed"},
	ReplyTimeout:     {http.StatusGatewayTimeout, "server_error", "reply_timeout"},
	ReplyMalformed:   {http.StatusBadGateway, "server_error", "reply_malformed"},
	UnknownRoute:     {http.StatusNotFound, "invalid_request_error", "unknown_url"},
	MethodNotAllowed: {http.StatusMethodNotAllowed, "invalid_request_error", "method_not_allowed"},
}

// Error is one typed failure. Build it with New.
type Error struct {
	Kind    Kind
	Param   string
	Message string
	Code    string // overrides the default code of Kind when set
}

func New(k Kind, param, msg string) *Error { return &Error{Kind: k, Param: param, Message: msg} }

// WithCode overrides the default wire code.
func (e *Error) WithCode(c string) *Error { e.Code = c; return e }

func (e *Error) Error() string { return e.Message }
func (e *Error) Status() int   { return table[e.Kind].status }

// Body is the OpenAI error object.
func (e *Error) Body() []byte {
	code := e.Code
	if code == "" {
		code = table[e.Kind].code
	}
	var param any
	if e.Param != "" {
		param = e.Param
	}
	b, _ := json.Marshal(map[string]any{"error": map[string]any{
		"message": e.Message, "type": table[e.Kind].typ, "param": param, "code": code,
	}})
	return b
}

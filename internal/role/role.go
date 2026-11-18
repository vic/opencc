// Package role holds Role: a session name that passed the peer nick rules.
package role

import (
	"errors"

	"opencc/internal/peer"
)

// Role is a Claude session name. Only Parse builds one.
type Role struct{ name string }

// Parse accepts a non-empty name that peer.ParseNick accepts.
func Parse(s string) (Role, error) {
	if s == "" {
		return Role{}, errors.New("empty role")
	}
	if err := peer.ParseNick(s); err != nil {
		return Role{}, err
	}
	return Role{s}, nil
}

func (r Role) String() string { return r.name }

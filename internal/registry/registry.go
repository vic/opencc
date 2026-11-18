// Package registry finds live Claude sessions by name in the Claude session registry.
package registry

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"

	"opencc/internal/peer"
	"opencc/internal/role"
)

// Session is one live named Claude session.
type Session struct {
	Role role.Role
	Sock peer.Addr
}

var entryName = regexp.MustCompile(`^(\d+)\.json$`)

// List returns live named sessions in sessDir, sorted by role then socket.
// A file is skipped when it is not <pid>.json, does not parse, has no valid
// name or socket, or its pid has no /proc/<pid> under procRoot.
func List(sessDir, procRoot string) ([]Session, error) {
	des, err := os.ReadDir(sessDir)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, de := range des {
		m := entryName.FindStringSubmatch(de.Name())
		if m == nil || !de.Type().IsRegular() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(sessDir, de.Name()))
		if err != nil {
			continue
		}
		var e struct {
			PID    int    `json:"pid"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Sock   string `json:"messagingSocketPath"`
		}
		if json.Unmarshal(b, &e) != nil || strconv.Itoa(e.PID) != m[1] || (e.Status != "idle" && e.Status != "busy") {
			continue
		}
		if _, err := os.Stat(filepath.Join(procRoot, m[1])); err != nil {
			continue
		}
		r, err := role.Parse(e.Name)
		if err != nil {
			continue
		}
		a, err := peer.PathAddr(e.Sock)
		if err != nil {
			continue
		}
		out = append(out, Session{r, a})
	}
	slices.SortFunc(out, func(a, b Session) int {
		if c := compare(a.Role.String(), b.Role.String()); c != 0 {
			return c
		}
		return compare(a.Sock.Path(), b.Sock.Path())
	})
	return out, nil
}

func compare(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// Resolution of a role: exactly one session, or the count seen.
func Resolve(ss []Session, r role.Role) (Session, int) {
	var hit []Session
	for _, s := range ss {
		if s.Role == r {
			hit = append(hit, s)
		}
	}
	if len(hit) == 1 {
		return hit[0], 1
	}
	return Session{}, len(hit)
}

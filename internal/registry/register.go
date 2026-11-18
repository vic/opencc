package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"opencc/internal/peer"
	"opencc/internal/role"
)

// procStart is field 22 of <procRoot>/<pid>/stat. comm (field 2) may hold
// spaces and parens, so fields are split after the last ')'.
func procStart(procRoot string, pid int) (string, error) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	s := string(b)
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return "", errors.New("stat has no comm terminator")
	}
	f := strings.Fields(s[i+1:])
	if len(f) < 20 {
		return "", errors.New("stat has too few fields")
	}
	return f[19], nil
}

// Register writes the entry that makes this process findable by name as a
// Claude peer (the shape ircc writes for its endpoints). It returns a func that
// removes the entry, but only if it is still ours.
func Register(sessDir, procRoot string, name role.Role, sock peer.Addr) (func() error, error) {
	pid := os.Getpid()
	start, err := procStart(procRoot, pid)
	if err != nil {
		return nil, fmt.Errorf("register %s: %w", name, err)
	}
	b, err := json.Marshal(map[string]any{
		"pid": pid, "sessionId": "", "cwd": "", "name": name.String(), "kind": "bg", "status": "idle",
		"messagingSocketPath": sock.Path(), "peerProtocol": 1, "peerFeatures": []string{},
		"procStart": start, "pidDomain": "", "startedAt": time.Now().UnixMilli(),
	})
	if err != nil {
		return nil, err
	}
	file := filepath.Join(sessDir, strconv.Itoa(pid)+".json")
	tmp, err := os.CreateTemp(sessDir, "."+strconv.Itoa(pid)+".json.*.tmp")
	if err != nil {
		return nil, fmt.Errorf("register %s: %w", name, err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	if err := os.Rename(tmp.Name(), file); err != nil {
		os.Remove(tmp.Name())
		return nil, err
	}
	return func() error {
		cur, err := os.ReadFile(file)
		if err != nil || !strings.Contains(string(cur), `"procStart":"`+start+`"`) {
			return nil
		}
		return os.Remove(file)
	}, nil
}

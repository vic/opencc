package main

import "testing"

func TestCheckListen(t *testing.T) {
	for _, c := range []struct {
		addr, key string
		ok        bool
	}{
		{"127.0.0.1:8080", "", true},
		{"[::1]:8080", "", true},
		{"0.0.0.0:8080", "", false},
		{"192.168.1.5:8080", "", false},
		{"localhost:8080", "", false}, // a name is not an address we can prove is loopback
		{"0.0.0.0:8080", "k", true},
		{"nonsense", "", false},
	} {
		if err := checkListen(c.addr, c.key); (err == nil) != c.ok {
			t.Errorf("checkListen(%q, key=%q) = %v, want ok=%v", c.addr, c.key, err, c.ok)
		}
	}
}

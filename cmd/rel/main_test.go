package main

import "testing"

func TestWantsHelp(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{}, false},
		{[]string{"--query.host=foo"}, false},
		{[]string{"--help"}, true},
		{[]string{"-h"}, true},
		{[]string{"--query.host=foo", "--help"}, true},
		{[]string{"--help", "--query.host=foo"}, true},
		// Not bare tokens — must not false-positive on a flag whose VALUE
		// happens to contain "--help" or on --config's own path argument.
		{[]string{"--some.key=--help"}, false},
		{[]string{"--config", "--help-shaped-path"}, false},
	}
	for _, c := range cases {
		if got := wantsHelp(c.args); got != c.want {
			t.Errorf("wantsHelp(%v) = %v, want %v", c.args, got, c.want)
		}
	}
}

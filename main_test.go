package main

import (
	"strings"
	"testing"
)

func TestIsAgentCommand(t *testing.T) {
	cases := map[string]bool{
		"":                           false,
		"comments list":              true,
		"--workspace /x comments":    true,
		"--endpoint=http://h health": true,
		"HEAD~":                      false,
		"--devtools":                 false,
		"-- comments":                false,
		"--cached":                   false,
		"main...HEAD":                false,
	}
	for in, want := range cases {
		if got := isAgentCommand(strings.Fields(in)); got != want {
			t.Errorf("isAgentCommand(%q) = %v, want %v", in, got, want)
		}
	}
}

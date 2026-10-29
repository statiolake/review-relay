package app

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeDiffArgs(t *testing.T) {
	cases := []struct {
		in, want []string
	}{
		{nil, nil},
		{[]string{}, []string{}},
		{[]string{"--"}, []string{}},
		{[]string{"HEAD~"}, []string{"HEAD~"}},
		{[]string{"HEAD~", "--"}, []string{"HEAD~"}},
		{[]string{"--staged"}, []string{"--cached"}},
		{[]string{"--staged", "--", "--staged"}, []string{"--cached", "--", "--staged"}},
		{[]string{"main...HEAD", "--", "a.go"}, []string{"main...HEAD", "--", "a.go"}},
	}
	for _, c := range cases {
		got := NormalizeDiffArgs(c.in)
		if !reflect.DeepEqual(got, c.want) || (got == nil) != (c.want == nil) {
			t.Errorf("NormalizeDiffArgs(%#v) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestSpecKey(t *testing.T) {
	same := [][2]Spec{
		{{"/r", []string{"--staged"}}, {"/r", []string{"--cached"}}},
		{{"/r", []string{"HEAD", "--"}}, {"/r", []string{"HEAD"}}},
		{{"/r", []string{}}, {"/r", []string{"--"}}},
	}
	for _, p := range same {
		if p[0].Key() != p[1].Key() || p[0].FrameName() != p[1].FrameName() {
			t.Errorf("%v and %v should share a key", p[0], p[1])
		}
	}
	distinct := []Spec{
		{"/r", nil},
		{"/r", []string{}},
		{"/r", []string{"HEAD"}},
		{"/r", []string{"HEAD", "x"}},
		{"/r", []string{"HEAD x"}},
		{"/s", nil},
	}
	seen := map[string]Spec{}
	for _, s := range distinct {
		if prev, ok := seen[s.Key()]; ok {
			t.Errorf("%v and %v share key %q", prev, s, s.Key())
		}
		seen[s.Key()] = s
		if !strings.HasPrefix(s.FrameName(), "review-relay ") {
			t.Errorf("FrameName = %q", s.FrameName())
		}
	}
}

func TestSpecTitle(t *testing.T) {
	if got := (Spec{"/src/proj", nil}).Title(); got != "proj" {
		t.Errorf("file mode title = %q", got)
	}
	if got := (Spec{"/src/proj", []string{"main...HEAD"}}).Title(); got != "proj — git diff main...HEAD" {
		t.Errorf("diff title = %q", got)
	}
	t.Setenv("HOME", "/home/u")
	if got := (Spec{"/home/u/proj", []string{}}).MenuTitle(); got != "proj — git diff   (~/proj)" {
		t.Errorf("menu title = %q", got)
	}
}

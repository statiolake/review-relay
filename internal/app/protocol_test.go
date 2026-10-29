package app

import (
	"bytes"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func TestMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	c := newConn(&buf)
	sent := []Message{
		{Type: TypeHello, Protocol: ProtocolVersion, Build: "b"},
		{Type: TypeOpen, Dir: "/r", DiffArgs: nil, Wait: true},
		{Type: TypeOpen, Dir: "/r", DiffArgs: []string{}, Env: []string{"GIT_DIR=/g"}},
		{Type: TypeOpen, Dir: "/r", DiffArgs: []string{"HEAD~", "--", "a b"}, Devtools: true},
		{Type: TypeResult, Error: "nope"},
		{Type: TypeClosed},
	}
	for _, m := range sent {
		if err := c.send(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range sent {
		got, err := c.receive()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, want) || (got.DiffArgs == nil) != (want.DiffArgs == nil) {
			t.Errorf("got %#v, want %#v", got, want)
		}
	}
	if _, err := c.receive(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last message: %v", err)
	}
}

func TestMessageLimits(t *testing.T) {
	c := newConn(bytes.NewBufferString(`{"type":` + strings.Repeat(" ", maxMessage+1) + `"x"}` + "\n"))
	if _, err := c.receive(); !errors.Is(err, errTooLong) {
		t.Errorf("long line: %v", err)
	}
	c = newConn(bytes.NewBufferString("not json\n"))
	if _, err := c.receive(); err == nil {
		t.Error("garbage accepted")
	}
}

func TestGitEnv(t *testing.T) {
	environ := []string{"PATH=/bin", "GIT_DIR=.git-alt", "GIT_WORK_TREE=/abs/tree", "GIT_AUTHOR_NAME=x", "GIT_INDEX_FILE=", "HOME=/h"}
	got := GitEnv(environ, "/work")
	want := []string{"GIT_DIR=/work/.git-alt", "GIT_WORK_TREE=/abs/tree", "GIT_AUTHOR_NAME=x", "GIT_INDEX_FILE="}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("GitEnv = %q, want %q", got, want)
	}
	if got := WithoutGitEnv(environ); !reflect.DeepEqual(got, []string{"PATH=/bin", "HOME=/h"}) {
		t.Errorf("WithoutGitEnv = %q", got)
	}
}

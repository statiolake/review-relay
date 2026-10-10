package review

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func intp(v int) *int { return &v }

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	return s, file
}

func TestAddReplyMovesThreadAndPersists(t *testing.T) {
	s, file := openTemp(t)
	root, err := s.Add(CommentInput{Location: Location{Path: "a.go", Line: 3}, Body: "  why?  ", Source: SourceHuman})
	if err != nil {
		t.Fatal(err)
	}
	if root.Body != "why?" || root.Author != "Human" || root.EndLine != 3 {
		t.Fatalf("unexpected root %+v", root)
	}
	reply, err := s.Reply(root.ID, CommentInput{Location: Location{Path: "b.go", Line: 7, EndLine: intp(9)}, Body: "moved"})
	if err != nil {
		t.Fatal(err)
	}
	if reply.ParentID != root.ID || reply.Author != "AI" || reply.Source != SourceAgent {
		t.Fatalf("unexpected reply %+v", reply)
	}
	// Replying to a reply attaches to the root.
	nested, err := s.Reply(reply.ID, CommentInput{Location: Location{Path: "b.go", Line: 7, EndLine: intp(9)}, Body: "again"})
	if err != nil {
		t.Fatal(err)
	}
	if nested.ParentID != root.ID {
		t.Fatalf("nested reply parent = %s, want root", nested.ParentID)
	}

	reopened, err := Open(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range reopened.Snapshot().Comments {
		if c.Path != "b.go" || c.Line != 7 || c.EndLine != 9 {
			t.Fatalf("thread not moved: %+v", c)
		}
	}
}

func TestRemoveRootRemovesThreadButReplyOnlyItself(t *testing.T) {
	s, _ := openTemp(t)
	loc := Location{Path: "a.go", Line: 0}
	root, _ := s.Add(CommentInput{Location: loc, Body: "r"})
	r1, _ := s.Reply(root.ID, CommentInput{Location: loc, Body: "1"})
	s.Reply(root.ID, CommentInput{Location: loc, Body: "2"})
	if remaining, err := s.Remove(r1.ID); err != nil || remaining != 2 {
		t.Fatalf("remove reply: remaining=%d err=%v", remaining, err)
	}
	if remaining, err := s.Remove(root.ID); err != nil || remaining != 0 {
		t.Fatalf("remove root: remaining=%d err=%v", remaining, err)
	}
	if _, err := s.Remove(root.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second remove: %v", err)
	}
}

func TestValidationErrors(t *testing.T) {
	s, _ := openTemp(t)
	cases := []CommentInput{
		{Location: Location{Path: "", Line: 0}, Body: "x"},
		{Location: Location{Path: "../etc/passwd", Line: 0}, Body: "x"},
		{Location: Location{Path: "/abs", Line: 0}, Body: "x"},
		{Location: Location{Path: "a", Line: -1}, Body: "x"},
		{Location: Location{Path: "a", Line: 5, EndLine: intp(4)}, Body: "x"},
		{Location: Location{Path: "a", Line: 0}, Body: "   "},
		{Location: Location{Path: "a", Line: 0}, Body: "x", Source: "robot"},
	}
	for _, input := range cases {
		_, err := s.Add(input)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("Add(%+v) = %v, want ValidationError", input, err)
		}
	}
	if _, err := s.Update("not-a-uuid", "x"); err == nil {
		t.Error("Update accepted an invalid id")
	}
}

func TestOpenRejectsCorruptState(t *testing.T) {
	file := filepath.Join(t.TempDir(), "state.json")
	os.WriteFile(file, []byte(`{"comments":[{"id":"x"}],"overall":""}`), 0o600)
	if _, err := Open(file); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("Open = %v, want corruption error", err)
	}
}

func TestSubscribersRunAfterPersist(t *testing.T) {
	s, file := openTemp(t)
	calls := 0
	s.Subscribe(func() {
		calls++
		if _, err := os.Stat(file); err != nil {
			t.Errorf("listener ran before the state was written: %v", err)
		}
	})
	s.SetOverall("hello")
	s.ClearReview()
	if calls != 2 {
		t.Fatalf("calls = %d", calls)
	}
}

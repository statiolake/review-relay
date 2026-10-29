package app

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRecent(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.json")
	r, err := OpenRecent(file)
	if err != nil || len(r.Entries()) != 0 {
		t.Fatalf("new list: %v %v", r.Entries(), err)
	}
	a := RecentEntry{Spec: Spec{"/a", nil}}
	b := RecentEntry{Spec: Spec{"/b", []string{"HEAD~"}}, Env: []string{"GIT_DIR=/g"}}
	for _, e := range []RecentEntry{a, b, a, {Spec: Spec{"/b", []string{"HEAD~", "--"}}, Env: b.Env}} {
		if err := r.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	// Same key replaces and moves to the front; the latest spelling wins.
	got := r.Entries()
	if len(got) != 2 || got[0].Root != "/b" || got[1].Root != "/a" {
		t.Fatalf("entries = %v", got)
	}

	reloaded, err := OpenRecent(file)
	if err != nil {
		t.Fatal(err)
	}
	re := reloaded.Entries()
	if len(re) != 2 || re[1].DiffArgs != nil || re[0].DiffArgs == nil || re[0].Env[0] != "GIT_DIR=/g" {
		t.Fatalf("reloaded = %#v", re)
	}

	for i := range MaxRecent + 3 {
		r.Add(RecentEntry{Spec: Spec{fmt.Sprintf("/r%d", i), nil}})
	}
	if got := r.Entries(); len(got) != MaxRecent || got[0].Root != fmt.Sprintf("/r%d", MaxRecent+2) {
		t.Fatalf("capped = %v", got)
	}
	if err := r.Clear(); err != nil || len(r.Entries()) != 0 {
		t.Fatalf("clear: %v %v", r.Entries(), err)
	}
	if reloaded, _ := OpenRecent(file); len(reloaded.Entries()) != 0 {
		t.Fatal("clear did not persist")
	}
}

func TestRecentCorrupted(t *testing.T) {
	file := filepath.Join(t.TempDir(), "app.json")
	os.WriteFile(file, []byte("{"), 0o600)
	r, err := OpenRecent(file)
	if err == nil || r == nil || len(r.Entries()) != 0 {
		t.Fatalf("corrupted: %v %v", r, err)
	}
	if err := r.Add(RecentEntry{Spec: Spec{"/a", nil}}); err != nil {
		t.Fatal(err)
	}
}

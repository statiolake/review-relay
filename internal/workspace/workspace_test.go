package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "keep.txt", "same\n")
	write(t, dir, "edit.txt", "a\nb\nc\n")
	write(t, dir, "gone.txt", "bye\n")
	write(t, dir, "old name.txt", "renamed content\nline2\nline3\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func TestParseUnified(t *testing.T) {
	out := "diff --git a/f b/f\nindex 1..2 100644\n--- a/f\n+++ b/f\n@@ -1,3 +1,3 @@\n a\n-b\n+B\n c\n\\ No newline at end of file\n"
	lines, binary, err := ParseUnified([]byte(out))
	if err != nil || binary {
		t.Fatal(err, binary)
	}
	want := []Line{{" ", 1, 1, "a"}, {"-", 2, 0, "b"}, {"+", 0, 2, "B"}, {" ", 3, 3, "c"}}
	if !slices.Equal(lines, want) {
		t.Fatalf("got %+v", lines)
	}
	if _, binary, _ := ParseUnified([]byte("diff --git a/x b/x\nBinary files a/x and b/x differ\n")); !binary {
		t.Fatal("binary not detected")
	}
}

func TestDiffModeListsChangesAndUntracked(t *testing.T) {
	dir := repo(t)
	write(t, dir, "edit.txt", "a\nB\nc\nd\n")
	os.Remove(filepath.Join(dir, "gone.txt"))
	git(t, dir, "mv", "old name.txt", "new name.txt")
	write(t, dir, "sub/new.txt", "fresh\n")

	w, err := Open(dir, []string{"HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	files, err := w.Files()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range files {
		got[f.Path] = f.Status + ":" + f.OldPath
		if f.Hash == "" {
			t.Errorf("%s has no hash", f.Path)
		}
	}
	want := map[string]string{"edit.txt": "M:", "gone.txt": "D:", "new name.txt": "R:old name.txt", "sub/new.txt": "A:"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s: got %q want %q", k, got[k], v)
		}
	}

	v, err := w.View("edit.txt")
	if err != nil {
		t.Fatal(err)
	}
	kinds := ""
	for _, l := range v.Lines {
		kinds += l.Kind
	}
	if kinds != " -+ +" || v.Lines[4].New != 4 {
		t.Fatalf("edit.txt lines %+v", v.Lines)
	}
	if v, _ := w.View("sub/new.txt"); len(v.Lines) != 1 || v.Lines[0].Kind != "+" {
		t.Fatalf("untracked view %+v", v)
	}
	if v, _ := w.View("gone.txt"); len(v.Lines) != 1 || v.Lines[0].Kind != "-" {
		t.Fatalf("deleted view %+v", v)
	}
	if v, err := w.View("new name.txt"); err != nil || len(v.Lines) != 3 || v.Status != "R" {
		t.Fatalf("renamed view %+v %v", v, err)
	}
	if v, _ := w.View("keep.txt"); v.Status != "" || len(v.Lines) != 1 {
		t.Fatalf("unchanged view %+v", v)
	}
}

func TestHashChangesWhenFileChangesAgain(t *testing.T) {
	dir := repo(t)
	write(t, dir, "edit.txt", "x\n")
	w, err := Open(dir, []string{})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := w.Files()
	again, _ := w.Files()
	write(t, dir, "edit.txt", "y\n")
	changed, _ := w.Files()
	if first[0].Hash != again[0].Hash || first[0].Hash == changed[0].Hash {
		t.Fatalf("hashes %s %s %s", first[0].Hash, again[0].Hash, changed[0].Hash)
	}
}

func TestEmptyDiffIsAnEmptyList(t *testing.T) {
	w, err := Open(repo(t), []string{"HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	files, err := w.Files()
	if err != nil || files == nil || len(files) != 0 {
		t.Fatalf("Files() = %#v, %v; want a non-nil empty list", files, err)
	}
}

func TestRangeDiffExcludesUntracked(t *testing.T) {
	dir := repo(t)
	write(t, dir, "edit.txt", "z\n")
	git(t, dir, "commit", "-qam", "second")
	write(t, dir, "untracked.txt", "u\n")
	w, err := Open(dir, []string{"HEAD~..HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	files, _ := w.Files()
	if len(files) != 1 || files[0].Path != "edit.txt" {
		t.Fatalf("got %+v", files)
	}
}

func TestUserPathspecIsReplacedPerFile(t *testing.T) {
	dir := repo(t)
	write(t, dir, "edit.txt", "z\n")
	write(t, dir, "keep.txt", "changed\n")
	w, err := Open(dir, []string{"HEAD", "--", "."})
	if err != nil {
		t.Fatal(err)
	}
	if v, err := w.View("edit.txt"); err != nil || v.Status != "M" {
		t.Fatalf("view %+v %v", v, err)
	}
}

func TestInvalidRevisionFailsAtOpen(t *testing.T) {
	dir := repo(t)
	if _, err := Open(dir, []string{"no-such-rev"}); err == nil || !strings.Contains(err.Error(), "no-such-rev") {
		t.Fatalf("Open = %v", err)
	}
}

func TestFileModeAndMissingFiles(t *testing.T) {
	dir := repo(t)
	write(t, dir, "bin.dat", "a\x00b")
	w, err := Open(filepath.Join(dir), nil)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := w.Files()
	if len(files) != 5 {
		t.Fatalf("files %+v", files)
	}
	if v, _ := w.View("bin.dat"); v.Notice != "Binary file." {
		t.Fatalf("bin view %+v", v)
	}
	if v, err := w.View("missing.txt"); err != nil || !strings.Contains(v.Notice, "does not exist") {
		t.Fatalf("missing view %+v %v", v, err)
	}
	if _, err := Open(t.TempDir(), []string{"HEAD"}); err == nil {
		t.Fatal("diff mode outside git accepted")
	}
}

func TestSplitLines(t *testing.T) {
	for in, want := range map[string][]string{"": {}, "a": {"a"}, "a\n": {"a"}, "a\r\nb\n\n": {"a", "b", ""}} {
		if got := SplitLines(in); !slices.Equal(got, want) {
			t.Errorf("SplitLines(%q) = %q", in, got)
		}
	}
}

func TestOpenEnvReachesGit(t *testing.T) {
	r := repo(t)
	tree := t.TempDir() // not a repository on its own
	write(t, tree, "edit.txt", "a\nB\nc\n")
	w, err := OpenEnv(tree, []string{"HEAD"}, []string{"GIT_DIR=" + filepath.Join(r, ".git"), "GIT_WORK_TREE=" + tree})
	if err != nil {
		t.Fatal(err)
	}
	files, err := w.Files()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(files, func(e Entry) bool { return e.Path == "edit.txt" }) {
		t.Fatalf("files = %v", files)
	}
	if _, err := Open(tree, []string{"HEAD"}); err == nil {
		t.Fatal("without the environment the tree is not a repository")
	}
}

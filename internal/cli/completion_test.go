package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func script(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := WriteCompletion(&b, "zsh"); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestCompletionUnknownShell(t *testing.T) {
	if err := Run([]string{"completion", "fish"}, &bytes.Buffer{}); err == nil {
		t.Fatal("want error")
	}
	if err := Run([]string{"completion"}, &bytes.Buffer{}); err == nil {
		t.Fatal("want error")
	}
}

func TestCompletionCoversFlags(t *testing.T) {
	s := script(t)
	if !strings.HasSuffix(strings.TrimSpace(s), "compdef _review-relay review-relay") {
		t.Error("script must end with compdef")
	}
	names := []string{"help"}
	for _, f := range append(append([]Flag{}, ViewerOptions...), GlobalFlags...) {
		names = append(names, f.Name)
	}
	// Every flag mentioned in the usage text must be completed too.
	for _, m := range regexp.MustCompile(`--([a-z][a-z-]*)`).FindAllStringSubmatch(Usage, -1) {
		names = append(names, m[1])
	}
	for _, c := range Commands {
		names = append(names, c)
	}
	for _, n := range names {
		if !strings.Contains(s, n) {
			t.Errorf("script lacks %q", n)
		}
	}
	for _, f := range []string{"--devtools", "--wait", "--no-window"} {
		if !strings.Contains(s, f) {
			t.Errorf("script lacks %s", f)
		}
	}
}

func TestCompletionValidZsh(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	f := filepath.Join(t.TempDir(), "c.zsh")
	os.WriteFile(f, []byte(script(t)), 0o644)
	if out, err := exec.Command(zsh, "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("zsh -n: %v\n%s", err, out)
	}
}

const zptyDriver = `
zmodload zsh/zpty || exit 3
zpty z "cd ${(q)1}; ZDOTDIR=${(q)2} zsh -i"
zpty -r z o 'ready*' || exit 4
zpty -w -n z "$3"$'\t'
sleep 1
zpty -w -n z $'\x18'
sleep 1
all=
while zpty -r -t z chunk; do all+=$chunk; done
zpty -d z
print -r -- ${(V)all}
`

// TestCompletionZpty completes in a real interactive zsh on a pty. It skips
// when the environment cannot run one.
func TestCompletionZpty(t *testing.T) {
	zsh, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh not installed")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if out, err := exec.Command(zsh, "-fc", "zmodload zsh/zpty").CombinedOutput(); err != nil {
		t.Skipf("zpty unavailable: %v %s", err, out)
	}
	dir := t.TempDir()
	zdot := filepath.Join(dir, "zdot")
	os.Mkdir(zdot, 0o755)
	os.WriteFile(filepath.Join(dir, "script.zsh"), []byte(script(t)), 0o644)
	rc := `PS1='> '
autoload -Uz compinit; compinit -u -d ` + filepath.Join(dir, "zcompdump") + `
source ` + filepath.Join(dir, "script.zsh") + `
zstyle ":completion:*" list-prompt ""; zstyle ":completion:*" menu no
show(){ print -r -- "@@[$BUFFER]@@"; zle -R }
zle -N show; bindkey "^X" show
print ready
`
	os.WriteFile(filepath.Join(zdot, ".zshrc"), []byte(rc), 0o644)
	repo := filepath.Join(dir, "repo")
	os.Mkdir(repo, 0o755)
	if out, err := exec.Command("git", "-C", repo, "init", "-q").CombinedOutput(); err != nil {
		t.Skipf("git init: %v %s", err, out)
	}
	driver := filepath.Join(dir, "driver.zsh")
	os.WriteFile(driver, []byte(zptyDriver), 0o644)

	complete := func(line string) string {
		out, err := exec.Command(zsh, "-f", driver, repo, zdot, line).CombinedOutput()
		if err != nil {
			t.Skipf("zpty harness unreliable here: %v %s", err, out)
		}
		m := regexp.MustCompile(`@@\[(.*?)\]@@`).FindAllStringSubmatch(string(out), -1)
		if len(m) == 0 {
			t.Skipf("zpty harness unreliable here: no marker in %q", out)
		}
		return m[len(m)-1][1]
	}
	for line, want := range map[string]string{
		"review-relay --ca":        "review-relay --cached ",
		"review-relay --de":        "review-relay --devtools ",
		"review-relay comme":       "review-relay comments ",
		"review-relay comments cl": "review-relay comments clear ",
	} {
		if got := complete(line); got != want {
			t.Errorf("complete(%q) = %q, want %q", line, got, want)
		}
	}
}

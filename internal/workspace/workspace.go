// Package workspace answers what the viewer shows: which files to list, and
// the lines of one file, either as plain text or as a diff against a git
// revision given in `git diff` syntax.
package workspace

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// MaxFileSize is the largest file the viewer renders.
const MaxFileSize = 4 << 20

// Workspace is a directory being reviewed, optionally in diff mode.
type Workspace struct {
	Root string // absolute, symlinks resolved
	Git  bool   // Root is the top level of a git work tree
	// DiffArgs are passed to `git diff`. nil means plain file mode.
	DiffArgs []string
	// includeUntracked is set when the diff's new side is the work tree, so
	// files git does not know yet are shown as added.
	includeUntracked bool
}

// Open resolves the workspace containing dir. diffArgs == nil selects plain
// file mode; a non-nil (possibly empty) slice selects diff mode.
func Open(dir string, diffArgs []string) (*Workspace, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	w := &Workspace{Root: abs}
	top, err := runGit(abs, "rev-parse", "--show-toplevel")
	if err == nil {
		w.Root, err = filepath.EvalSymlinks(strings.TrimSpace(string(top)))
		if err != nil {
			return nil, err
		}
		w.Git = true
	}
	if diffArgs == nil {
		return w, nil
	}
	if !w.Git {
		return nil, fmt.Errorf("diff mode needs a git repository, but %s is not inside one", abs)
	}
	w.DiffArgs = slices.Clone(diffArgs)
	w.includeUntracked = comparesWithWorkTree(diffArgs)
	// Validate the arguments once up front so a typo fails at startup.
	if _, err := runGit(w.Root, w.diffCommand("--raw")...); err != nil {
		return nil, err
	}
	return w, nil
}

// comparesWithWorkTree guesses whether `git diff args` has the work tree as its
// new side: no --cached/--staged, no range, and at most one revision.
func comparesWithWorkTree(args []string) bool {
	revisions := 0
	for _, a := range args {
		switch {
		case a == "--":
			return revisions <= 1
		case a == "--cached" || a == "--staged" || strings.Contains(a, ".."):
			return false
		case !strings.HasPrefix(a, "-"):
			revisions++
		}
	}
	return revisions <= 1
}

// DiffMode reports whether the workspace compares against a revision.
func (w *Workspace) DiffMode() bool { return w.DiffArgs != nil }

// Entry is one file in the tree.
type Entry struct {
	Path    string `json:"path"`
	OldPath string `json:"oldPath,omitempty"`
	// Status is a git status letter (A, M, D, R, C, T, U) in diff mode, and
	// empty in file mode.
	Status string `json:"status,omitempty"`
	// Hash identifies the change in diff mode; it differs whenever the change
	// itself differs, so a "reviewed" mark can be invalidated.
	Hash string `json:"hash,omitempty"`
}

// Files lists the files to show in the tree, sorted by path.
func (w *Workspace) Files() ([]Entry, error) {
	var entries []Entry
	var err error
	switch {
	case w.DiffMode():
		entries, err = w.changedFiles()
	case w.Git:
		entries, err = w.gitFiles()
	default:
		entries, err = w.walkFiles()
	}
	if err != nil {
		return nil, err
	}
	if entries == nil {
		entries = []Entry{} // an empty list, never JSON null
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Path, b.Path) })
	return entries, nil
}

func (w *Workspace) gitFiles() ([]Entry, error) {
	out, err := runGit(w.Root, "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--deduplicate")
	if err != nil {
		return nil, err
	}
	var entries []Entry
	for _, p := range splitZ(out) {
		// Tracked files deleted from the work tree are still listed.
		if info, err := os.Lstat(w.abs(p)); err == nil && !info.IsDir() {
			entries = append(entries, Entry{Path: p})
		}
	}
	return entries, nil
}

func (w *Workspace) walkFiles() ([]Entry, error) {
	var entries []Entry
	err := filepath.WalkDir(w.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(w.Root, p)
		if err != nil {
			return err
		}
		entries = append(entries, Entry{Path: filepath.ToSlash(rel)})
		return nil
	})
	return entries, err
}

func (w *Workspace) changedFiles() ([]Entry, error) {
	out, err := runGit(w.Root, w.diffCommand("--raw", "-z", "--no-abbrev")...)
	if err != nil {
		return nil, err
	}
	fields := splitZ(out)
	var entries []Entry
	for i := 0; i < len(fields); {
		// ":oldmode newmode oldsha newsha STATUS" then one or two paths.
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) != 5 {
			return nil, fmt.Errorf("unexpected git diff --raw record %q", fields[i])
		}
		status := meta[4][:1]
		e := Entry{Status: status}
		if status == "R" || status == "C" {
			if i+2 >= len(fields) {
				return nil, fmt.Errorf("truncated git diff --raw output")
			}
			e.OldPath, e.Path = fields[i+1], fields[i+2]
			i += 3
		} else {
			if i+1 >= len(fields) {
				return nil, fmt.Errorf("truncated git diff --raw output")
			}
			e.Path = fields[i+1]
			i += 2
		}
		h := sha256.New()
		h.Write([]byte(fields[i-1] + "\x00" + strings.Join(meta, " ")))
		// git reports an all-zero object id for the work tree side; hash the
		// file itself so that edits change the hash.
		if strings.Trim(meta[3], "0") == "" && status != "D" {
			content, err := os.ReadFile(w.abs(e.Path))
			if err != nil {
				return nil, err
			}
			h.Write(content)
		}
		e.Hash = hex.EncodeToString(h.Sum(nil))[:16]
		entries = append(entries, e)
	}
	if w.includeUntracked {
		untracked, err := w.untracked()
		if err != nil {
			return nil, err
		}
		for _, p := range untracked {
			content, err := os.ReadFile(w.abs(p))
			if err != nil {
				return nil, err
			}
			sum := sha256.Sum256(append([]byte("untracked\x00"), content...))
			entries = append(entries, Entry{Path: p, Status: "A", Hash: hex.EncodeToString(sum[:])[:16]})
		}
	}
	return entries, nil
}

func (w *Workspace) untracked() ([]string, error) {
	args := append([]string{"ls-files", "-z", "--others", "--exclude-standard", "--"}, w.userPathspec()...)
	out, err := runGit(w.Root, args...)
	if err != nil {
		return nil, err
	}
	return splitZ(out), nil
}

// revisionArgs are the user's diff arguments before any "--".
func (w *Workspace) revisionArgs() []string {
	if i := slices.Index(w.DiffArgs, "--"); i >= 0 {
		return w.DiffArgs[:i]
	}
	return w.DiffArgs
}

// userPathspec is what the user passed after "--".
func (w *Workspace) userPathspec() []string {
	if i := slices.Index(w.DiffArgs, "--"); i >= 0 {
		return w.DiffArgs[i+1:]
	}
	return nil
}

// Line is one row of a file view. Kind is ' ' (unchanged), '+' (added) or '-'
// (deleted). Old and New are one-based line numbers, 0 when absent; New-1 is
// the zero-based line comments attach to.
type Line struct {
	Kind string `json:"k"`
	Old  int    `json:"o,omitempty"`
	New  int    `json:"n,omitempty"`
	Text string `json:"t"`
}

// View is the rendered content of one file.
type View struct {
	Path    string `json:"path"`
	Status  string `json:"status,omitempty"`
	OldPath string `json:"oldPath,omitempty"`
	// Notice explains why Lines is empty or incomplete (binary, too large...).
	Notice string `json:"notice,omitempty"`
	Lines  []Line `json:"lines"`
	// Version changes whenever anything above changes.
	Version string `json:"version"`
}

// View renders one file. In diff mode, changed files show the whole new file
// with deletions interleaved; unchanged files show their plain content.
func (w *Workspace) View(p string) (View, error) {
	v, err := w.view(p)
	if err != nil {
		return View{}, err
	}
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00", v.Path, v.Status, v.OldPath, v.Notice)
	for _, l := range v.Lines {
		fmt.Fprintf(h, "%s%d,%d\x00%s\x00", l.Kind, l.Old, l.New, l.Text)
	}
	v.Version = hex.EncodeToString(h.Sum(nil))[:16]
	return v, nil
}

func (w *Workspace) view(p string) (View, error) {
	if w.DiffMode() {
		entries, err := w.changedFiles()
		if err != nil {
			return View{}, err
		}
		i := slices.IndexFunc(entries, func(e Entry) bool { return e.Path == p })
		if i >= 0 {
			return w.diffView(entries[i])
		}
	}
	v := View{Path: p, Lines: []Line{}}
	lines, notice, err := w.ReadLines(p)
	if err != nil {
		return View{}, err
	}
	v.Notice = notice
	for i, t := range lines {
		v.Lines = append(v.Lines, Line{Kind: " ", Old: i + 1, New: i + 1, Text: t})
	}
	return v, nil
}

func (w *Workspace) diffView(e Entry) (View, error) {
	v := View{Path: e.Path, Status: e.Status, OldPath: e.OldPath, Lines: []Line{}}
	if e.Status == "U" {
		v.Notice = "Unmerged file; resolve the conflict to see its diff."
		return v, nil
	}
	if w.includeUntracked && e.Status == "A" && !w.tracked(e.Path) {
		lines, notice, err := w.ReadLines(e.Path)
		if err != nil {
			return View{}, err
		}
		v.Notice = notice
		for i, t := range lines {
			v.Lines = append(v.Lines, Line{Kind: "+", New: i + 1, Text: t})
		}
		return v, nil
	}
	pathspec := []string{":(literal)" + e.Path}
	if e.OldPath != "" {
		pathspec = append(pathspec, ":(literal)"+e.OldPath)
	}
	// Replace the user's pathspec, if any, with exactly this file's paths.
	args := []string{"-c", "core.quotePath=false", "diff", "-M", "--no-color", "--no-ext-diff", "--no-textconv", "-U2147483647"}
	args = append(append(append(args, w.revisionArgs()...), "--"), pathspec...)
	out, err := runGit(w.Root, args...)
	if err != nil {
		return View{}, err
	}
	if len(out) > 4*MaxFileSize {
		v.Notice = "Diff is too large to display."
		return v, nil
	}
	lines, binary, err := ParseUnified(out)
	if err != nil {
		return View{}, fmt.Errorf("%s: %w", e.Path, err)
	}
	if binary {
		v.Notice = "Binary file."
		return v, nil
	}
	if len(lines) == 0 && e.Status != "D" {
		// Pure renames and mode changes have no hunks; show the content.
		plain, notice, err := w.ReadLines(e.Path)
		if err != nil {
			return View{}, err
		}
		v.Notice = notice
		for i, t := range plain {
			lines = append(lines, Line{Kind: " ", Old: i + 1, New: i + 1, Text: t})
		}
	}
	v.Lines = lines
	return v, nil
}

func (w *Workspace) tracked(p string) bool {
	_, err := runGit(w.Root, "ls-files", "--error-unmatch", "--", ":(literal)"+p)
	return err == nil
}

// ReadLines returns the lines of a work tree file. When the file cannot be
// shown as text (missing, binary or too large) it returns no lines and a
// notice; a missing file is a normal state while reviewing, not an error.
func (w *Workspace) ReadLines(p string) (lines []string, notice string, err error) {
	root, err := os.OpenRoot(w.Root)
	if err != nil {
		return nil, "", err
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(p))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "File does not exist in the work tree.", nil
	}
	if err != nil {
		return nil, "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if info.IsDir() {
		return nil, "", fmt.Errorf("%s is a directory", p)
	}
	if info.Size() > MaxFileSize {
		return nil, "File is too large to display.", nil
	}
	content := make([]byte, info.Size())
	if _, err := f.ReadAt(content, 0); err != nil && info.Size() > 0 {
		return nil, "", err
	}
	if bytes.IndexByte(content[:min(len(content), 8000)], 0) >= 0 {
		return nil, "Binary file.", nil
	}
	return SplitLines(string(content)), "", nil
}

// SplitLines splits text the way editors number lines: a trailing newline does
// not start a new line, and CRLF counts as one line break.
func SplitLines(text string) []string {
	if text == "" {
		return []string{}
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// ParseUnified converts `git diff` output for one file into view lines.
func ParseUnified(out []byte) (lines []Line, binary bool, err error) {
	lines = []Line{}
	text := string(out)
	if text == "" {
		return lines, false, nil
	}
	rows := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	i := 0
	for ; i < len(rows) && !strings.HasPrefix(rows[i], "@@"); i++ {
		if strings.HasPrefix(rows[i], "Binary files ") || rows[i] == "GIT binary patch" {
			return nil, true, nil
		}
		if strings.HasPrefix(rows[i], "diff --cc ") || strings.HasPrefix(rows[i], "diff --combined ") {
			return nil, false, errors.New("combined diffs are not supported")
		}
	}
	var oldNo, newNo int
	for ; i < len(rows); i++ {
		row := rows[i]
		if strings.HasPrefix(row, "@@") {
			if _, err := fmt.Sscanf(hunkRanges(row), "-%d +%d", &oldNo, &newNo); err != nil {
				return nil, false, fmt.Errorf("malformed hunk header %q", row)
			}
			if oldNo == 0 {
				oldNo = 1
			}
			if newNo == 0 {
				newNo = 1
			}
			continue
		}
		if strings.HasPrefix(row, "diff --git ") {
			return nil, false, errors.New("diff output covers more than one file")
		}
		if row == "" {
			// Only the final context line of a file with blank content can
			// lose its leading space to trimming; treat it as context.
			row = " "
		}
		t := strings.TrimSuffix(row[1:], "\r")
		switch row[0] {
		case ' ':
			lines = append(lines, Line{Kind: " ", Old: oldNo, New: newNo, Text: t})
			oldNo++
			newNo++
		case '+':
			lines = append(lines, Line{Kind: "+", New: newNo, Text: t})
			newNo++
		case '-':
			lines = append(lines, Line{Kind: "-", Old: oldNo, Text: t})
			oldNo++
		case '\\':
			// "\ No newline at end of file"
		default:
			return nil, false, fmt.Errorf("unexpected diff line %q", row)
		}
	}
	return lines, false, nil
}

// hunkRanges turns "@@ -1,3 +1,4 @@ func" into "-1,3 +1,4" with counts dropped
// so Sscanf sees "-1 +1".
func hunkRanges(header string) string {
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return ""
	}
	strip := func(r string) string { s, _, _ := strings.Cut(r, ","); return s }
	return strip(fields[1]) + " " + strip(fields[2])
}

func (w *Workspace) diffCommand(extra ...string) []string {
	args := []string{"-c", "core.quotePath=false", "diff", "-M"}
	args = append(args, extra...)
	// The user's arguments go last so they can end with their own "--".
	return append(args, w.DiffArgs...)
}

func (w *Workspace) abs(p string) string { return filepath.Join(w.Root, filepath.FromSlash(p)) }

func splitZ(out []byte) []string {
	s := strings.TrimSuffix(string(out), "\x00")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\x00")
}

func runGit(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

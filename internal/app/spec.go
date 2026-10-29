// Package app is the resident macOS app's platform-independent core: the
// request protocol spoken over its UNIX socket, the single-instance logic,
// the set of open reviews and the recent list. The Cocoa windows themselves
// live in internal/macapp; tests drive the core with a fake UI.
package app

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Spec identifies one review: the workspace root and the `git diff`
// arguments (nil selects file mode). Two requests with the same Spec share
// one window.
type Spec struct {
	Root     string   `json:"root"`
	DiffArgs []string `json:"diffArgs"`
}

// NormalizeDiffArgs rewrites git diff arguments to a canonical spelling, so
// that equivalent invocations find the same window: --staged becomes
// --cached and a trailing "--" with no paths after it is dropped. nil (file
// mode) stays nil and any other input stays non-nil.
func NormalizeDiffArgs(args []string) []string {
	if args == nil {
		return nil
	}
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...) // paths follow; keep them verbatim
			break
		}
		if a == "--staged" {
			a = "--cached"
		}
		out = append(out, a)
	}
	if len(out) > 0 && out[len(out)-1] == "--" {
		out = out[:len(out)-1]
	}
	return out
}

// Key is the identity of a review; equal Keys mean the same window.
func (s Spec) Key() string {
	if s.DiffArgs == nil {
		return s.Root + "\x00files"
	}
	return s.Root + "\x00diff\x00" + strings.Join(NormalizeDiffArgs(s.DiffArgs), "\x00")
}

// FrameName names the saved window frame of this review.
func (s Spec) FrameName() string {
	sum := sha256.Sum256([]byte(s.Key()))
	return "review-relay " + hex.EncodeToString(sum[:8])
}

// Title is the window title: the repository name and the diff, if any.
func (s Spec) Title() string {
	title := filepath.Base(s.Root)
	if s.DiffArgs != nil {
		title += " — git diff " + strings.Join(s.DiffArgs, " ")
	}
	return title
}

// MenuTitle is the Open Recent entry: the title plus where the repository
// is, with the home directory abbreviated.
func (s Spec) MenuTitle() string {
	dir := s.Root
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if dir == home {
			dir = "~"
		} else if rest, ok := strings.CutPrefix(dir, home+string(filepath.Separator)); ok {
			dir = "~" + string(filepath.Separator) + rest
		}
	}
	return s.Title() + "  (" + dir + ")"
}

func (s Spec) clone() Spec { return Spec{Root: s.Root, DiffArgs: slices.Clone(s.DiffArgs)} }

package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"sync"

	"github.com/statiolake/review-relay/internal/review"
)

// MaxRecent is how many reviews File > Open Recent lists.
const MaxRecent = 10

// RecentEntry is enough to open a review again: its Spec and the GIT_*
// variables it was opened with.
type RecentEntry struct {
	Spec
	Env []string `json:"env,omitempty"`
}

// Recent is the most-recently-opened-first list of reviews, persisted in the
// app's preferences file.
type Recent struct {
	file string

	mu      sync.Mutex
	entries []RecentEntry
}

type recentFile struct {
	Recent []RecentEntry `json:"recent"`
}

// OpenRecent loads the list from file. A missing file is an empty list; a
// corrupted one is reported alongside an empty list, since losing the
// history must not keep the app from starting.
func OpenRecent(file string) (*Recent, error) {
	r := &Recent{file: file}
	encoded, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	var data recentFile
	if err := json.Unmarshal(encoded, &data); err != nil {
		return r, fmt.Errorf("%s is corrupted, starting with an empty recent list: %w", file, err)
	}
	for _, e := range data.Recent {
		if e.Root != "" && len(r.entries) < MaxRecent {
			r.entries = append(r.entries, e)
		}
	}
	return r, nil
}

// Entries returns a copy of the list, most recent first.
func (r *Recent) Entries() []RecentEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]RecentEntry, len(r.entries))
	for i, e := range r.entries {
		out[i] = RecentEntry{Spec: e.Spec.clone(), Env: slices.Clone(e.Env)}
	}
	return out
}

// Add moves e to the front, replacing an entry with the same key.
func (r *Recent) Add(e RecentEntry) error {
	return r.update(func(entries []RecentEntry) []RecentEntry {
		key := e.Key()
		entries = slices.DeleteFunc(entries, func(x RecentEntry) bool { return x.Key() == key })
		entries = slices.Insert(entries, 0, RecentEntry{Spec: e.Spec.clone(), Env: slices.Clone(e.Env)})
		if len(entries) > MaxRecent {
			entries = entries[:MaxRecent]
		}
		return entries
	})
}

// Clear empties the list.
func (r *Recent) Clear() error {
	return r.update(func([]RecentEntry) []RecentEntry { return nil })
}

func (r *Recent) update(fn func([]RecentEntry) []RecentEntry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := fn(slices.Clone(r.entries))
	if next == nil {
		next = []RecentEntry{}
	}
	if err := review.WriteJSONFile(r.file, recentFile{Recent: next}); err != nil {
		return err
	}
	r.entries = next
	return nil
}

package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"sync"

	"github.com/statiolake/review-relay/internal/review"
)

// Prefs is per-workspace viewer state that is not part of the review: which
// diffs were marked reviewed, and the diff layout.
type Prefs struct {
	file string

	mu   sync.Mutex
	data prefsData
}

type prefsData struct {
	// Reviewed maps a path to the change hash it had when marked reviewed.
	// The mark only counts while the file's current hash still matches.
	Reviewed map[string]string `json:"reviewed"`
	Layout   string            `json:"layout"`
}

// OpenPrefs loads prefs from file, or starts with defaults if it is missing.
func OpenPrefs(file string) (*Prefs, error) {
	p := &Prefs{file: file, data: prefsData{Reviewed: map[string]string{}, Layout: "split"}}
	encoded, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &p.data); err != nil {
		return nil, fmt.Errorf("%s is corrupted: %w", file, err)
	}
	if p.data.Reviewed == nil || (p.data.Layout != "inline" && p.data.Layout != "split") {
		return nil, fmt.Errorf("%s is corrupted: missing reviewed map or invalid layout", file)
	}
	return p, nil
}

func (p *Prefs) Reviewed() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return maps.Clone(p.data.Reviewed)
}

func (p *Prefs) Layout() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data.Layout
}

// SetReviewed marks path reviewed at hash, or clears the mark if hash is "".
func (p *Prefs) SetReviewed(path, hash string) error {
	return p.update(func(d *prefsData) {
		if hash == "" {
			delete(d.Reviewed, path)
		} else {
			d.Reviewed[path] = hash
		}
	})
}

func (p *Prefs) SetLayout(layout string) error {
	return p.update(func(d *prefsData) { d.Layout = layout })
}

func (p *Prefs) update(fn func(*prefsData)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := prefsData{Reviewed: maps.Clone(p.data.Reviewed), Layout: p.data.Layout}
	fn(&next)
	if err := review.WriteJSONFile(p.file, next); err != nil {
		return err
	}
	p.data = next
	return nil
}

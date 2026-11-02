package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/statiolake/review-relay/internal/review"
)

// Prefs is per-workspace viewer state that is not part of the review: the
// diff layout.
type Prefs struct {
	file string

	mu   sync.Mutex
	data prefsData
}

type prefsData struct {
	Layout string `json:"layout"`
}

// OpenPrefs loads prefs from file, or starts with defaults if it is missing.
func OpenPrefs(file string) (*Prefs, error) {
	p := &Prefs{file: file, data: prefsData{Layout: "split"}}
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
	if p.data.Layout != "inline" && p.data.Layout != "split" {
		return nil, fmt.Errorf("%s is corrupted: invalid layout", file)
	}
	return p, nil
}

func (p *Prefs) Layout() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.data.Layout
}

func (p *Prefs) SetLayout(layout string) error {
	return p.update(func(d *prefsData) { d.Layout = layout })
}

func (p *Prefs) update(fn func(*prefsData)) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	next := p.data
	fn(&next)
	if err := review.WriteJSONFile(p.file, next); err != nil {
		return err
	}
	p.data = next
	return nil
}

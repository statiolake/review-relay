package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/statiolake/review-relay/internal/review"
	"github.com/statiolake/review-relay/internal/workspace"
)

// Marks holds one diff's reviewed files: for each path, the id of the content
// it had when marked, plus a snapshot of that content. A file whose content
// has moved on since is "changed since review" and is diffed against its
// snapshot. Each diff has its own directory, so reviews do not share marks.
type Marks struct {
	dir string

	mu    sync.Mutex
	marks map[string]string // path -> content id
}

// OpenMarks loads the marks in dir, or starts empty if there are none yet.
func OpenMarks(dir string) (*Marks, error) {
	m := &Marks{dir: dir, marks: map[string]string{}}
	encoded, err := os.ReadFile(m.file())
	if errors.Is(err, fs.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(encoded, &m.marks); err != nil || m.marks == nil {
		return nil, fmt.Errorf("%s is corrupted: %v", m.file(), err)
	}
	return m, nil
}

func (m *Marks) file() string { return filepath.Join(m.dir, "marks.json") }

func (m *Marks) snapshot(id string) string { return filepath.Join(m.dir, "snapshots", id) }

// All returns path -> content id for every mark.
func (m *Marks) All() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.marks)
}

// Set marks path reviewed with this content. The snapshot is written first,
// so a stored mark always has one.
func (m *Marks) Set(path, id string, content []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id != workspace.DeletedID {
		if err := writeFileAtomic(m.snapshot(id), content); err != nil {
			return err
		}
	}
	next := maps.Clone(m.marks)
	next[path] = id
	return m.commit(next)
}

// Clear forgets path's mark.
func (m *Marks) Clear(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	next := maps.Clone(m.marks)
	id, ok := next[path]
	if !ok {
		return nil
	}
	delete(next, path)
	if err := m.commit(next); err != nil {
		return err
	}
	if id == workspace.DeletedID || slices.Contains(slices.Collect(maps.Values(next)), id) {
		return nil
	}
	return os.Remove(m.snapshot(id))
}

// ClearAll forgets every mark and snapshot.
func (m *Marks) ClearAll() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := os.RemoveAll(m.dir); err != nil {
		return err
	}
	m.marks = map[string]string{}
	return nil
}

// Snapshot returns the content path was marked with; ok is false without a mark.
func (m *Marks) Snapshot(path string) (id string, content []byte, ok bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok = m.marks[path]
	if !ok || id == workspace.DeletedID {
		return id, nil, ok, nil
	}
	content, err = os.ReadFile(m.snapshot(id))
	return id, content, true, err
}

func (m *Marks) commit(next map[string]string) error {
	if err := review.WriteJSONFile(m.file(), next); err != nil {
		return err
	}
	m.marks = next
	return nil
}

func writeFileAtomic(file string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly after the rename
	if _, err := tmp.Write(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

// Package session lets the CLI find the running viewer for a directory. Each
// viewer writes a descriptor file naming its workspace root and endpoint.
package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Dir is where descriptors live. Tests override it.
var Dir = filepath.Join(os.TempDir(), "review-relay", "sessions")

type descriptor struct {
	Version  int    `json:"version"`
	Endpoint string `json:"endpoint"`
	Root     string `json:"root"`
}

// Registration is this process's descriptor file.
type Registration struct{ file string }

// Register publishes endpoint as the viewer for root.
func Register(root, endpoint string) (*Registration, error) {
	if err := os.MkdirAll(Dir, 0o700); err != nil {
		return nil, err
	}
	var id [16]byte
	rand.Read(id[:]) // crypto/rand.Read never returns an error
	file := filepath.Join(Dir, hex.EncodeToString(id[:])+".json")
	encoded, err := json.Marshal(descriptor{Version: 2, Endpoint: endpoint, Root: root})
	if err != nil {
		return nil, err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, append(encoded, '\n'), 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, file); err != nil {
		return nil, err
	}
	return &Registration{file: file}, nil
}

// Close removes the descriptor.
func (r *Registration) Close() error { return os.Remove(r.file) }

// Find returns the endpoint of the live viewer whose root is the deepest one
// containing dir. Descriptors of viewers that no longer answer are skipped:
// they are left behind when a viewer is killed.
func Find(dir string) (endpoint, root string, err error) {
	requested, err := canonical(dir)
	if err != nil {
		return "", "", fmt.Errorf("resolve %q: %w", dir, err)
	}
	entries, err := os.ReadDir(Dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", errors.New("no review-relay viewer is running; start one with `review-relay` in the workspace")
	}
	if err != nil {
		return "", "", err
	}
	var best []descriptor
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		encoded, err := os.ReadFile(filepath.Join(Dir, entry.Name()))
		if err != nil {
			continue // removed by its viewer while we were listing
		}
		var d descriptor
		if json.Unmarshal(encoded, &d) != nil || d.Version != 2 || !isLoopback(d.Endpoint) {
			continue // written by an incompatible version
		}
		if !contains(d.Root, requested) || !healthy(d.Endpoint) {
			continue
		}
		switch {
		case len(best) == 0 || len(d.Root) > len(best[0].Root):
			best = []descriptor{d}
		case len(d.Root) == len(best[0].Root):
			best = append(best, d)
		}
	}
	switch len(best) {
	case 0:
		return "", "", fmt.Errorf("no running review-relay viewer contains %q", requested)
	case 1:
		return best[0].Endpoint, best[0].Root, nil
	}
	return "", "", fmt.Errorf("multiple review-relay viewers contain %q; use --endpoint to choose one", requested)
}

func canonical(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}

func contains(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func isLoopback(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" && u.Port() != ""
}

func healthy(endpoint string) bool {
	resp, err := (&http.Client{Timeout: 500 * time.Millisecond}).Get(strings.TrimRight(endpoint, "/") + "/health")
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/statiolake/review-relay/internal/review"
	"github.com/statiolake/review-relay/internal/server"
	"github.com/statiolake/review-relay/internal/session"
	"github.com/statiolake/review-relay/internal/workspace"
)

// Review is one running viewer: its workspace, its loopback server and the
// session descriptor agents find it by. The single-window viewer on Linux
// and Windows runs one; the macOS app runs one per window.
type Review struct {
	Spec     Spec
	Endpoint string

	srv    *server.Server
	reg    *session.Registration
	served chan error
}

// Resolve finds the workspace containing dir and the Spec it would be
// reviewed under. env ("KEY=value") is added to every git command. The
// arguments reach git as given; only the Spec's Key normalises them.
func Resolve(dir string, diffArgs, env []string) (*workspace.Workspace, Spec, error) {
	ws, err := workspace.OpenEnv(dir, diffArgs, env)
	if err != nil {
		return nil, Spec{}, err
	}
	return ws, Spec{Root: ws.Root, DiffArgs: ws.DiffArgs}, nil
}

// Start opens ws's stored review and serves it. Only one viewer may run per
// workspace root, in this process or another.
func Start(ws *workspace.Workspace) (*Review, error) {
	if _, root, err := session.Find(ws.Root); err == nil && root == ws.Root {
		return nil, fmt.Errorf("a viewer for %s is already running", ws.Root)
	}
	dir, err := DataDir(ws.Root)
	if err != nil {
		return nil, err
	}
	store, err := review.Open(filepath.Join(dir, "state.json"))
	if err != nil {
		return nil, fmt.Errorf("%w\nmove the file away to start with an empty review", err)
	}
	prefs, err := server.OpenPrefs(filepath.Join(dir, "viewer.json"))
	if err != nil {
		return nil, fmt.Errorf("%w\nmove the file away to reset the viewer state", err)
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	srv := server.New(ws, store, prefs, exe)
	endpoint, serve, err := srv.Listen()
	if err != nil {
		return nil, err
	}
	reg, err := session.Register(ws.Root, endpoint)
	if err != nil {
		srv.Close()
		return nil, err
	}
	r := &Review{
		Spec:     Spec{Root: ws.Root, DiffArgs: ws.DiffArgs},
		Endpoint: endpoint,
		srv:      srv,
		reg:      reg,
		served:   make(chan error, 1),
	}
	go func() {
		err := serve()
		if !errors.Is(err, http.ErrServerClosed) {
			r.served <- fmt.Errorf("server stopped: %w", err)
		}
		close(r.served)
	}()
	return r, nil
}

// URL is the page to load in the review's window.
func (r *Review) URL() string { return strings.TrimRight(r.Endpoint, "/") + "/" }

// Failed yields the error that stopped the server, if it stops on its own;
// it is closed without a value once Close stops it.
func (r *Review) Failed() <-chan error { return r.served }

// Close unregisters the review and stops its server. The review's comments
// are already on disk, so opening the same workspace again restores them.
func (r *Review) Close() error {
	return errors.Join(r.reg.Close(), r.srv.Close())
}

// DataDir is where a workspace's review lives, outside the workspace so that
// it never ends up in a commit.
func DataDir(root string) (string, error) {
	config, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(config, "review-relay", "workspaces", hex.EncodeToString(sum[:8])), nil
}

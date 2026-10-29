package app

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"path/filepath"
	"sync"
	"time"

	"github.com/statiolake/review-relay/internal/workspace"
)

// UI is the windowing side of the app. Every method may be called from any
// goroutine and must not block on the UI thread: implementations hand the
// work to it and return.
type UI interface {
	// OpenWindow shows a new window loading url and brings the app forward.
	// frameName names the saved window frame.
	OpenWindow(id int, title, url, frameName string, devtools bool)
	// FocusWindow brings an open window and the app forward.
	FocusWindow(id int)
	// CloseWindow closes a window; the UI then reports WindowClosed.
	CloseWindow(id int)
	// SetRecent replaces the titles under File > Open Recent.
	SetRecent(titles []string)
	// ShowError reports a failure that has no terminal to go to.
	ShowError(message string)
	// Terminate quits the app; the UI calls Shutdown before exiting.
	Terminate()
}

// App owns the open reviews: one window and one Review per Spec.
type App struct {
	ui     UI
	build  string
	recent *Recent

	mu       sync.Mutex
	nextID   int
	windows  map[int]*window
	byKey    map[string]int
	quitting bool
	waiting  sync.WaitGroup // connections about to report a closed window
	shutdown sync.Once
}

type window struct {
	review *Review
	env    []string
	closed chan struct{} // closed once the window and its review are gone
}

// New creates the app core. build is reported to clients (see BuildID).
func New(ui UI, build string, recent *Recent) *App {
	a := &App{ui: ui, build: build, recent: recent, windows: map[int]*window{}, byKey: map[string]int{}}
	a.publishRecent()
	return a
}

// openResult is the outcome of one open request.
type openResult struct {
	id      int
	focused bool
	title   string
	url     string
	closed  <-chan struct{}
}

// Open shows the review of dir and diffArgs: a new window, or the existing
// one for the same Spec brought forward. A different review of the same
// workspace is refused (one workspace has one review state), and its window
// is brought forward instead. With wait, the caller must call a.waiting.Done
// once it has reported the window's closing.
func (a *App) Open(dir string, diffArgs, env []string, devtools, wait bool) (openResult, error) {
	ws, spec, err := Resolve(dir, diffArgs, env)
	if err != nil {
		return openResult{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.quitting {
		return openResult{}, errors.New("the app is quitting")
	}
	res, err := a.openLocked(ws, spec, env, devtools)
	if err == nil && wait {
		a.waiting.Add(1) // under a.mu and before quitting, so before Shutdown waits
	}
	return res, err
}

func (a *App) openLocked(ws *workspace.Workspace, spec Spec, env []string, devtools bool) (openResult, error) {
	key := spec.Key()
	if id, ok := a.byKey[key]; ok {
		w := a.windows[id]
		a.ui.FocusWindow(id)
		a.remember(RecentEntry{Spec: w.review.Spec, Env: w.env})
		return openResult{id: id, focused: true, title: spec.Title(), url: w.review.URL(), closed: w.closed}, nil
	}
	for id, w := range a.windows {
		if w.review.Spec.Root == spec.Root {
			a.ui.FocusWindow(id)
			return openResult{}, fmt.Errorf("%s is already open as %q; close that window (⌘W) first", spec.Root, w.review.Spec.Title())
		}
	}
	r, err := Start(ws)
	if err != nil {
		return openResult{}, err
	}
	a.nextID++
	id := a.nextID
	w := &window{review: r, env: env, closed: make(chan struct{})}
	a.windows[id] = w
	a.byKey[key] = id
	a.ui.OpenWindow(id, spec.Title(), r.URL(), spec.FrameName(), devtools)
	a.remember(RecentEntry{Spec: spec, Env: env})
	go func() {
		if err, failed := <-r.Failed(); failed {
			log.Printf("%s: %v", spec.Root, err)
			a.ui.CloseWindow(id)
		}
	}()
	return openResult{id: id, title: spec.Title(), url: r.URL(), closed: w.closed}, nil
}

// remember records e as the most recent review; called with a.mu held.
func (a *App) remember(e RecentEntry) {
	if a.recent == nil {
		return
	}
	if err := a.recent.Add(e); err != nil {
		log.Printf("recent list: %v", err)
	}
	a.publishRecent()
}

func (a *App) publishRecent() {
	if a.recent == nil {
		a.ui.SetRecent(nil)
		return
	}
	var titles []string
	for _, e := range a.recent.Entries() {
		titles = append(titles, e.MenuTitle())
	}
	a.ui.SetRecent(titles)
}

// WindowClosed is called by the UI once a window has closed (⌘W, the close
// button, or CloseWindow): the review is unregistered and its server stops.
func (a *App) WindowClosed(id int) {
	a.mu.Lock()
	w, ok := a.windows[id]
	if ok {
		delete(a.windows, id)
		delete(a.byKey, w.review.Spec.Key())
	}
	a.mu.Unlock()
	if !ok {
		return
	}
	if err := w.review.Close(); err != nil {
		log.Printf("close %s: %v", w.review.Spec.Root, err)
	}
	close(w.closed)
}

// OpenRecent opens entry i of the recent list; -1 clears the list. It is
// called from the menu, so failures are shown in the UI.
func (a *App) OpenRecent(i int) {
	if a.recent == nil {
		return
	}
	if i < 0 {
		if err := a.recent.Clear(); err != nil {
			a.ui.ShowError(err.Error())
		}
		a.publishRecent()
		return
	}
	entries := a.recent.Entries()
	if i >= len(entries) {
		return
	}
	go func() {
		e := entries[i]
		if _, err := a.Open(e.Root, e.DiffArgs, e.Env, false, false); err != nil {
			a.ui.ShowError(err.Error())
		}
	}()
}

// Reopen handles a click on the Dock icon while no window is open: the most
// recent review opens again.
func (a *App) Reopen() {
	a.mu.Lock()
	open := len(a.windows)
	a.mu.Unlock()
	if open == 0 {
		a.OpenRecent(0)
	}
}

// Shutdown closes every review and gives waiting clients a moment to hear
// that their windows closed. The UI calls it right before the process exits;
// later calls do nothing.
func (a *App) Shutdown() {
	a.shutdown.Do(func() {
		a.mu.Lock()
		a.quitting = true
		ids := make([]int, 0, len(a.windows))
		for id := range a.windows {
			ids = append(ids, id)
		}
		a.mu.Unlock()
		for _, id := range ids {
			a.WindowClosed(id)
		}
		done := make(chan struct{})
		go func() { a.waiting.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
}

// Handle serves one client connection until it is done.
func (a *App) Handle(nc net.Conn) {
	defer nc.Close()
	c := newConn(nc)
	for {
		m, err := c.receive()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("client: %v", err)
			}
			return
		}
		switch m.Type {
		case TypeHello:
			a.mu.Lock()
			n := len(a.windows)
			a.mu.Unlock()
			c.send(Message{Type: TypeHello, Protocol: ProtocolVersion, Build: a.build, Reviews: n})
		case TypeQuit:
			// Only an idle app quits on request: a newer client replaces it,
			// but never by closing reviews someone is looking at.
			a.mu.Lock()
			busy := len(a.windows) > 0 || a.quitting
			a.mu.Unlock()
			if busy {
				c.send(Message{Type: TypeResult, Error: "reviews are open"})
				continue
			}
			c.send(Message{Type: TypeResult})
			a.ui.Terminate()
			return
		case TypeOpen:
			a.handleOpen(c, nc, m)
			return
		default:
			c.send(Message{Type: TypeResult, Error: fmt.Sprintf("unknown request %q", m.Type)})
			return
		}
	}
}

func (a *App) handleOpen(c *conn, nc net.Conn, m Message) {
	if !filepath.IsAbs(m.Dir) {
		c.send(Message{Type: TypeResult, Error: "the working directory must be absolute"})
		return
	}
	res, err := a.Open(m.Dir, m.DiffArgs, m.Env, m.Devtools, m.Wait)
	if err != nil {
		c.send(Message{Type: TypeResult, Error: err.Error()})
		return
	}
	if !m.Wait {
		c.send(Message{Type: TypeResult, Focused: res.focused, Title: res.title, URL: res.url})
		return
	}
	defer a.waiting.Done()
	if err := c.send(Message{Type: TypeResult, Focused: res.focused, Title: res.title, URL: res.url}); err != nil {
		return
	}
	gone := make(chan struct{})
	go func() {
		io.Copy(io.Discard, nc) // returns when the client hangs up
		close(gone)
	}()
	select {
	case <-res.closed:
		c.send(Message{Type: TypeClosed})
	case <-gone:
	}
}

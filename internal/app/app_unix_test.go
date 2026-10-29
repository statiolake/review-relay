//go:build unix

package app

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/statiolake/review-relay/internal/session"
)

// fakeUI records what the app asked of the windowing side.
type fakeUI struct {
	mu        sync.Mutex
	opened    []int
	titles    map[int]string
	focused   []int
	recent    []string
	errors    []string
	terminate func()
	app       *App
}

func (u *fakeUI) OpenWindow(id int, title, url, frameName string, devtools bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.opened = append(u.opened, id)
	if u.titles == nil {
		u.titles = map[int]string{}
	}
	u.titles[id] = title
}
func (u *fakeUI) FocusWindow(id int) { u.mu.Lock(); u.focused = append(u.focused, id); u.mu.Unlock() }
func (u *fakeUI) CloseWindow(id int) { go u.app.WindowClosed(id) }
func (u *fakeUI) SetRecent(titles []string) {
	u.mu.Lock()
	u.recent = titles
	u.mu.Unlock()
}
func (u *fakeUI) ShowError(msg string) { u.mu.Lock(); u.errors = append(u.errors, msg); u.mu.Unlock() }
func (u *fakeUI) Terminate()           { go u.terminate() }

func (u *fakeUI) snapshot() (opened, focused []int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]int(nil), u.opened...), append([]int(nil), u.focused...)
}

// env isolates the session directory, the review data and the socket.
type env struct {
	sock string
	mu   sync.Mutex
	apps []*fakeUI // every app started, in order
}

func newEnv(t *testing.T) *env {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	old := session.Dir
	session.Dir = filepath.Join(home, "sessions")
	t.Cleanup(func() { session.Dir = old })
	// Short, so the socket path fits sun_path even under macOS's $TMPDIR.
	sock, err := os.MkdirTemp("/tmp", "rr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sock) })
	if err := ensurePrivateDir(sock); err != nil {
		t.Fatal(err)
	}
	return &env{sock: sock}
}

// spawn starts an app in this process, the way SpawnDetached starts one in
// a new process: it takes the lock or exits.
func (e *env) spawn(t *testing.T, build string) func() (<-chan struct{}, error) {
	return func() (<-chan struct{}, error) {
		exited := make(chan struct{})
		go func() {
			inst, err := Acquire(e.sock, 300*time.Millisecond)
			if err != nil {
				close(exited)
				return
			}
			ui := &fakeUI{}
			recent, _ := OpenRecent(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "review-relay", "app.json"))
			a := New(ui, build, recent)
			ui.app = a
			var once sync.Once
			ui.terminate = func() {
				once.Do(func() {
					a.Shutdown()
					inst.Close()
					close(exited)
				})
			}
			t.Cleanup(ui.terminate)
			e.mu.Lock()
			e.apps = append(e.apps, ui)
			e.mu.Unlock()
			inst.Serve(a.Handle)
		}()
		return exited, nil
	}
}

func (e *env) client(t *testing.T, build string) (*Client, *bytes.Buffer) {
	notice := &bytes.Buffer{}
	return &Client{Dir: e.sock, Build: build, Spawn: e.spawn(t, build), Notice: notice, StartTimeout: 5 * time.Second}, notice
}

func (e *env) app(i int) *fakeUI {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.apps[i]
}

func (e *env) count() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.apps)
}

func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Skipf("git unavailable: %v %s", err, out)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	return dir
}

func TestOpenAndFocus(t *testing.T) {
	e := newEnv(t)
	c, _ := e.client(t, "b1")
	repo := gitRepo(t)

	res, err := c.Invoke(Request{Dir: repo, DiffArgs: []string{"--staged"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Focused || !strings.Contains(res.Title, "git diff --staged") || !strings.HasPrefix(res.URL, "http://127.0.0.1:") {
		t.Fatalf("first open: %#v", res)
	}
	// Agents find the review exactly as they find a standalone viewer.
	if endpoint, _, err := session.Find(filepath.Join(repo)); err != nil || endpoint+"/" != res.URL {
		t.Fatalf("session.Find = %q, %v", endpoint, err)
	}

	// Same review, spelled differently, from a subdirectory: focus.
	sub := filepath.Join(repo, "sub")
	os.Mkdir(sub, 0o755)
	res, err = c.Invoke(Request{Dir: sub, DiffArgs: []string{"--cached", "--"}})
	if err != nil || !res.Focused {
		t.Fatalf("second open: %#v, %v", res, err)
	}
	if e.count() != 1 {
		t.Fatalf("%d apps started", e.count())
	}
	opened, focused := e.app(0).snapshot()
	if len(opened) != 1 || len(focused) != 1 || focused[0] != opened[0] {
		t.Fatalf("opened %v, focused %v", opened, focused)
	}

	// A different review of the same workspace is refused, and the open
	// one comes forward.
	if _, err := c.Invoke(Request{Dir: repo, DiffArgs: nil}); err == nil || !strings.Contains(err.Error(), "already open") {
		t.Fatalf("other review of the same root: %v", err)
	}
	if _, focused := e.app(0).snapshot(); len(focused) != 2 {
		t.Fatalf("focused %v", focused)
	}

	// Errors from the workspace reach the client.
	if _, err := c.Invoke(Request{Dir: t.TempDir(), DiffArgs: []string{"HEAD"}}); err == nil || !strings.Contains(err.Error(), "git repository") {
		t.Fatalf("diff outside git: %v", err)
	}
	if _, err := c.Invoke(Request{Dir: "relative"}); err == nil {
		t.Fatal("relative directory accepted")
	}
}

func TestCloseUnregistersAndReopenRestores(t *testing.T) {
	e := newEnv(t)
	c, _ := e.client(t, "b1")
	dir := t.TempDir()
	if _, err := c.Invoke(Request{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	ui := e.app(0)
	opened, _ := ui.snapshot()
	ui.app.WindowClosed(opened[0])
	if _, _, err := session.Find(dir); err == nil {
		t.Fatal("closed review is still registered")
	}
	// The recent list remembers it; the Dock's reopen opens it again.
	ui.mu.Lock()
	recent := ui.recent
	ui.mu.Unlock()
	if len(recent) != 1 || !strings.HasPrefix(recent[0], filepath.Base(dir)) {
		t.Fatalf("recent = %q", recent)
	}
	ui.app.Reopen()
	waitFor(t, func() bool { o, _ := ui.snapshot(); return len(o) == 2 })
	if _, _, err := session.Find(dir); err != nil {
		t.Fatalf("reopened review: %v", err)
	}
	// With a window open, a Dock click does nothing.
	ui.app.Reopen()
	time.Sleep(50 * time.Millisecond)
	if o, _ := ui.snapshot(); len(o) != 2 {
		t.Fatalf("opened %v", o)
	}
	ui.app.OpenRecent(-1)
	ui.mu.Lock()
	recent = ui.recent
	ui.mu.Unlock()
	if len(recent) != 0 {
		t.Fatalf("cleared recent = %q", recent)
	}
}

func TestWait(t *testing.T) {
	e := newEnv(t)
	c, _ := e.client(t, "b1")
	dir := t.TempDir()
	done := make(chan error, 1)
	go func() { _, err := c.Invoke(Request{Dir: dir, Wait: true}); done <- err }()
	waitFor(t, func() bool { return e.count() == 1 && len(first(e.app(0).snapshot())) == 1 })
	select {
	case err := <-done:
		t.Fatalf("returned before the window closed: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	ui := e.app(0)
	opened, _ := ui.snapshot()
	ui.app.WindowClosed(opened[0])
	if err := <-done; err != nil {
		t.Fatalf("wait: %v", err)
	}

	// Quitting the app (⌘Q) closes the window too: still a normal return.
	go func() { _, err := c.Invoke(Request{Dir: dir, Wait: true}); done <- err }()
	waitFor(t, func() bool { return len(first(ui.snapshot())) == 2 })
	ui.Terminate()
	if err := <-done; err != nil {
		t.Fatalf("wait across quit: %v", err)
	}
}

func TestWaitAppDied(t *testing.T) {
	e := newEnv(t)
	// An app that accepts the review and then vanishes.
	inst, err := Acquire(e.sock, 0)
	if err != nil {
		t.Fatal(err)
	}
	go inst.Serve(func(nc net.Conn) {
		c := newConn(nc)
		c.receive()
		c.send(Message{Type: TypeHello, Protocol: ProtocolVersion, Build: "b1"})
		c.receive()
		c.send(Message{Type: TypeResult})
		nc.Close()
	})
	defer inst.Close()
	c, _ := e.client(t, "b1")
	if _, err := c.Invoke(Request{Dir: "/", Wait: true}); !errors.Is(err, ErrAppDied) {
		t.Fatalf("err = %v", err)
	}
}

func TestStaleSocket(t *testing.T) {
	e := newEnv(t)
	// A crashed app leaves its socket behind, with nobody listening.
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(e.sock, socketName), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ln.SetUnlinkOnClose(false)
	ln.Close()
	c, _ := e.client(t, "b1")
	if _, err := c.Invoke(Request{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	if e.count() != 1 {
		t.Fatalf("%d apps", e.count())
	}
}

func TestAcquireExclusive(t *testing.T) {
	e := newEnv(t)
	first, err := Acquire(e.sock, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(e.sock, 50*time.Millisecond); !errors.Is(err, ErrRunning) {
		t.Fatalf("second Acquire: %v", err)
	}
	// Released on Close, which also removes the socket.
	first.Close()
	if _, err := os.Stat(filepath.Join(e.sock, socketName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket after Close: %v", err)
	}
	second, err := Acquire(e.sock, 0)
	if err != nil {
		t.Fatal(err)
	}
	second.Close()
}

func TestRacingFirstInvocations(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	const n = 6
	errs := make(chan error, n)
	results := make(chan Message, n)
	for range n {
		go func() {
			c, _ := e.client(t, "b1")
			res, err := c.Invoke(Request{Dir: dir})
			errs <- err
			results <- res
		}()
	}
	focused := 0
	for range n {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		if (<-results).Focused {
			focused++
		}
	}
	// Several apps may have been spawned, but one served every request.
	served := 0
	for i := range e.count() {
		if o, _ := e.app(i).snapshot(); len(o) > 0 {
			served++
			if len(o) != 1 {
				t.Fatalf("app %d opened %v", i, o)
			}
		}
	}
	if served != 1 || focused != n-1 {
		t.Fatalf("%d apps served, %d focused", served, focused)
	}
}

func TestSocketRecreated(t *testing.T) {
	e := newEnv(t)
	inst, err := Acquire(e.sock, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	ui := &fakeUI{}
	a := New(ui, "b1", nil)
	ui.app = a
	go inst.Serve(a.Handle)
	time.Sleep(20 * time.Millisecond)
	os.Remove(filepath.Join(e.sock, socketName)) // a temp cleaner strikes
	inst.checkSocket()
	c := &Client{Dir: e.sock, Build: "b1", Spawn: func() (<-chan struct{}, error) { return nil, errors.New("must not spawn") }}
	if _, err := c.Invoke(Request{Dir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestVersionMismatch(t *testing.T) {
	e := newEnv(t)
	old, _ := e.client(t, "old")
	dir := t.TempDir()

	// An idle old app is replaced by one from the new executable.
	if _, err := old.Invoke(Request{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	ui := e.app(0)
	o, _ := ui.snapshot()
	ui.app.WindowClosed(o[0])
	newer, notice := e.client(t, "new")
	if _, err := newer.Invoke(Request{Dir: dir}); err != nil {
		t.Fatal(err)
	}
	if e.count() != 2 || len(first(e.app(1).snapshot())) != 1 || notice.Len() != 0 {
		t.Fatalf("apps %d, notice %q", e.count(), notice)
	}

	// A busy app of another build keeps serving, with a notice.
	other, notice := e.client(t, "other")
	res, err := other.Invoke(Request{Dir: dir})
	if err != nil || !res.Focused {
		t.Fatalf("busy mismatch: %#v %v", res, err)
	}
	if e.count() != 2 || !strings.Contains(notice.String(), "different build") {
		t.Fatalf("apps %d, notice %q", e.count(), notice)
	}
}

func TestIncompatibleProtocol(t *testing.T) {
	e := newEnv(t)
	inst, err := Acquire(e.sock, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inst.Close()
	go inst.Serve(func(nc net.Conn) {
		defer nc.Close()
		c := newConn(nc)
		for {
			m, err := c.receive()
			if err != nil {
				return
			}
			if m.Type == TypeHello {
				c.send(Message{Type: TypeHello, Protocol: ProtocolVersion + 1, Build: "future", Reviews: 1})
			}
		}
	})
	c, _ := e.client(t, "b1")
	if _, err := c.Invoke(Request{Dir: "/"}); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("err = %v", err)
	}
}

func TestSocketDirPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "s")
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	os.Chmod(dir, 0o755)
	if err := ensurePrivateDir(dir); err != nil {
		t.Fatal(err)
	}
	if info, _ := os.Stat(dir); info.Mode().Perm() != 0o700 {
		t.Fatalf("mode %v", info.Mode())
	}
	link := filepath.Join(t.TempDir(), "link")
	os.Symlink(dir, link)
	if err := ensurePrivateDir(link); err == nil {
		t.Fatal("symlink accepted")
	}
	got, err := SocketDir()
	if err != nil || len(filepath.Join(got, socketName)) > maxSocketPath {
		t.Fatalf("SocketDir = %q, %v", got, err)
	}
}

func first(a, _ []int) []int { return a }

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatal("timed out")
}

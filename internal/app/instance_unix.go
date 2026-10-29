//go:build unix

package app

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

// Files in the socket directory.
const (
	socketName = "app.sock"
	lockName   = "app.lock"
	logName    = "app.log"
)

// maxSocketPath is the longest path a UNIX socket address can hold on macOS
// (sun_path is 104 bytes there, 108 on Linux), less the terminating NUL.
const maxSocketPath = 103

// SocketDir returns the per-user directory holding the app's socket, lock
// and log: $TMPDIR/review-relay-<uid> (or /tmp/review-relay-<uid> if that
// path is too long for a socket address). It is created mode 0700 and must
// be a real directory owned by this user.
func SocketDir() (string, error) {
	name := fmt.Sprintf("review-relay-%d", os.Getuid())
	dir := filepath.Join(os.TempDir(), name)
	if len(filepath.Join(dir, socketName)) > maxSocketPath {
		dir = filepath.Join("/tmp", name)
	}
	return dir, ensurePrivateDir(dir)
}

func ensurePrivateDir(dir string) error {
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user", dir)
	}
	if info.Mode().Perm() != 0o700 {
		return os.Chmod(dir, 0o700)
	}
	return nil
}

// ErrRunning is returned by Acquire when another app holds the lock.
var ErrRunning = errors.New("another review-relay app is already running")

// Instance is the running app's claim on the socket directory: an exclusive
// lock held for the app's lifetime, and the socket it listens on. The lock,
// not the socket file, decides which app is alive, so a socket left behind
// by a crashed app is recognised as stale and replaced.
type Instance struct {
	dir  string
	lock *os.File

	mu       sync.Mutex
	ln       *net.UnixListener
	closed   bool
	handle   func(net.Conn)
	stopWait chan struct{}
}

// Acquire takes the lock in dir, retrying for up to wait (a replaced app may
// still be exiting), and listens on the socket.
func Acquire(dir string, wait time.Duration) (*Instance, error) {
	lock, err := os.OpenFile(filepath.Join(dir, lockName), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(wait)
	for {
		err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EINTR) {
			lock.Close()
			return nil, fmt.Errorf("lock %s: %w", lock.Name(), err)
		}
		if time.Now().After(deadline) {
			lock.Close()
			return nil, ErrRunning
		}
		time.Sleep(25 * time.Millisecond)
	}
	i := &Instance{dir: dir, lock: lock, stopWait: make(chan struct{})}
	ln, err := i.listen()
	if err != nil {
		lock.Close()
		return nil, err
	}
	i.ln = ln
	return i, nil
}

// listen replaces whatever is at the socket path (we hold the lock, so it is
// stale) with a new listener.
func (i *Instance) listen() (*net.UnixListener, error) {
	path := filepath.Join(i.dir, socketName)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve accepts connections, each handled on its own goroutine, until Close.
// While serving it checks the socket file now and then and recreates it if
// something (a temp directory cleaner) removed it.
func (i *Instance) Serve(handle func(net.Conn)) {
	i.mu.Lock()
	i.handle = handle
	ln := i.ln
	i.mu.Unlock()
	go i.watch(time.Minute)
	i.accept(ln)
}

func (i *Instance) accept(ln *net.UnixListener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			i.mu.Lock()
			current := i.ln == ln && !i.closed
			i.mu.Unlock()
			if current && !errors.Is(err, net.ErrClosed) {
				log.Printf("accept: %v", err)
				time.Sleep(100 * time.Millisecond)
				continue
			}
			return
		}
		go i.handle(c)
	}
}

func (i *Instance) watch(every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-i.stopWait:
			return
		case <-ticker.C:
			i.checkSocket()
		}
	}
}

// checkSocket refreshes the socket's timestamps, or listens again if the
// socket file is gone or is no longer ours.
func (i *Instance) checkSocket() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return
	}
	path := filepath.Join(i.dir, socketName)
	now := time.Now()
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSocket != 0 && i.answers(path) {
		os.Chtimes(path, now, now)
		return
	}
	ln, err := i.listen()
	if err != nil {
		log.Printf("recreate socket: %v", err)
		return
	}
	old := i.ln
	old.SetUnlinkOnClose(false) // the path is the new listener's now
	old.Close()
	i.ln = ln
	go i.accept(ln)
}

// answers reports whether path still reaches this instance (rather than a
// file that replaced it). Called with i.mu held; the handler answers on its
// own goroutine, so a hello round trip does not deadlock.
func (i *Instance) answers(path string) bool {
	c, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// Close stops listening, removes the socket and releases the lock.
func (i *Instance) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	close(i.stopWait)
	err := i.ln.Close() // also removes the socket file
	return errors.Join(err, i.lock.Close())
}

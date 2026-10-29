//go:build unix

package app

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

// Request is what one invocation asks the app for.
type Request struct {
	Dir      string   // absolute working directory
	DiffArgs []string // nil: file mode
	Env      []string // GIT_* variables, see GitEnv
	Devtools bool
	Wait     bool // block until the window closes
}

// Client hands requests to the resident app, starting it if needed.
type Client struct {
	Dir   string // see SocketDir
	Build string // this executable's BuildID
	// Spawn starts the app detached and returns a channel closed when the
	// spawned process exits (which a losing racer does at once).
	Spawn func() (exited <-chan struct{}, err error)
	// Notice receives messages for the user that are not errors.
	Notice io.Writer
	// StartTimeout bounds how long a freshly spawned app may take to listen.
	StartTimeout time.Duration
}

// ErrAppDied is returned by Invoke with Wait when the app goes away before
// the window closes.
var ErrAppDied = errors.New("the review-relay app exited before the window was closed")

// Invoke sends req to the running app (starting one if none answers) and,
// with req.Wait, returns once the review's window has closed.
//
// If the running app is a different build, an idle app (no windows) is
// asked to quit and a new one is started from this executable; a busy app
// keeps serving, as long as it speaks the same protocol, with a notice that
// quitting it (⌘Q) picks up the new build.
func (c *Client) Invoke(req Request) (Message, error) {
	conn, hello, err := c.connect()
	if err != nil {
		return Message{}, err
	}
	if hello.Build != c.Build && hello.Build != "unknown" && c.Build != "unknown" {
		if hello.Reviews == 0 {
			quit := c.askQuit(conn)
			if quit {
				if err := c.waitGone(); err != nil {
					return Message{}, err
				}
			}
			// Either a new app from this executable, or the old one that
			// refused because a window opened meanwhile.
			if conn, hello, err = c.connect(); err != nil {
				return Message{}, err
			}
		}
		if hello.Build != c.Build {
			if hello.Protocol != ProtocolVersion {
				conn.close()
				return Message{}, fmt.Errorf("the running review-relay app is an incompatible version (protocol %d, this executable %d); quit it (⌘Q) and try again", hello.Protocol, ProtocolVersion)
			}
			fmt.Fprintln(c.Notice, "review-relay: the running app is a different build; quit it (⌘Q) to use this executable's")
		}
	}
	defer conn.close()
	if err := conn.send(Message{Type: TypeOpen, Dir: req.Dir, DiffArgs: req.DiffArgs, Env: req.Env, Devtools: req.Devtools, Wait: req.Wait}); err != nil {
		return Message{}, err
	}
	res, err := conn.receive()
	if err != nil {
		return Message{}, fmt.Errorf("no answer from the review-relay app: %w", err)
	}
	if res.Error != "" {
		return res, errors.New(res.Error)
	}
	if !req.Wait {
		return res, nil
	}
	closed, err := conn.receive()
	if err != nil || closed.Type != TypeClosed {
		return res, ErrAppDied
	}
	return res, nil
}

type clientConn struct {
	*conn
	nc net.Conn
}

func (c clientConn) close() { c.nc.Close() }

func (c *Client) socket() string { return filepath.Join(c.Dir, socketName) }

// connect dials the app and exchanges hellos, spawning the app first if
// nothing answers.
func (c *Client) connect() (clientConn, Message, error) {
	if cc, hello, err := c.dial(); err == nil {
		return cc, hello, nil
	}
	exited, err := c.Spawn()
	if err != nil {
		return clientConn{}, Message{}, fmt.Errorf("start the review-relay app: %w", err)
	}
	timeout := c.StartTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		cc, hello, err := c.dial()
		if err == nil {
			return cc, hello, nil
		}
		last = err
		select {
		case <-exited:
			// Our app lost a start race or failed. A winner may still be
			// coming up; give it a little longer, not the whole timeout.
			if grace := time.Now().Add(2 * time.Second); grace.Before(deadline) {
				deadline = grace
			}
			exited = nil
		case <-time.After(25 * time.Millisecond):
		}
	}
	return clientConn{}, Message{}, fmt.Errorf("the review-relay app did not start (%v); see %s", last, filepath.Join(c.Dir, logName))
}

func (c *Client) dial() (clientConn, Message, error) {
	nc, err := net.DialTimeout("unix", c.socket(), time.Second)
	if err != nil {
		return clientConn{}, Message{}, err
	}
	cc := clientConn{conn: newConn(nc), nc: nc}
	nc.SetDeadline(time.Now().Add(5 * time.Second))
	if err := cc.send(Message{Type: TypeHello, Protocol: ProtocolVersion, Build: c.Build}); err != nil {
		nc.Close()
		return clientConn{}, Message{}, err
	}
	hello, err := cc.receive()
	if err != nil || hello.Type != TypeHello {
		nc.Close()
		return clientConn{}, Message{}, fmt.Errorf("unexpected answer from %s: %v", c.socket(), err)
	}
	nc.SetDeadline(time.Time{})
	return cc, hello, nil
}

// askQuit asks an idle app to quit and reports whether it agreed. It hangs
// up either way.
func (c *Client) askQuit(cc clientConn) bool {
	defer cc.close()
	if cc.send(Message{Type: TypeQuit}) != nil {
		return false
	}
	res, err := cc.receive()
	return err == nil && res.Type == TypeResult && res.Error == ""
}

// waitGone waits until the old app no longer answers.
func (c *Client) waitGone() error {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		nc, err := net.DialTimeout("unix", c.socket(), time.Second)
		if err != nil {
			return nil
		}
		nc.Close()
		time.Sleep(25 * time.Millisecond)
	}
	return errors.New("the old review-relay app did not quit; quit it (⌘Q) and try again")
}

// SpawnDetached starts exe as the resident app in its own session (so it
// outlives the terminal and gets no SIGHUP or Ctrl-C from it), with no
// terminal attached, logging to the socket directory, the way `open` would
// launch an app. GIT_* variables are dropped from its environment: each
// request brings its own.
func SpawnDetached(exe, dir string) func() (<-chan struct{}, error) {
	return func() (<-chan struct{}, error) {
		logFile := filepath.Join(dir, logName)
		if info, err := os.Stat(logFile); err == nil && info.Size() > 1<<20 {
			os.Remove(logFile)
		}
		out, err := os.OpenFile(logFile, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
		if err != nil {
			return nil, err
		}
		defer out.Close()
		null, err := os.Open(os.DevNull)
		if err != nil {
			return nil, err
		}
		defer null.Close()
		cmd := exec.Command(exe, ResidentFlag)
		cmd.Dir = "/"
		cmd.Env = WithoutGitEnv(os.Environ())
		cmd.Stdin, cmd.Stdout, cmd.Stderr = null, out, out
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		exited := make(chan struct{})
		go func() { cmd.Wait(); close(exited) }()
		return exited, nil
	}
}

package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// ProtocolVersion changes whenever a message changes incompatibly. The
// hello exchange (Type, Protocol, Build, Reviews) must stay readable by
// every version, so that a new client can recognise an old app.
const ProtocolVersion = 1

// ResidentFlag is the hidden argument that makes the executable run as the
// resident app instead of as a client.
const ResidentFlag = "--resident-app"

// Message types. A client sends hello, then open (or quit); the app answers
// hello with hello, open and quit with result, and, for open with Wait,
// sends closed once that review's window has closed.
const (
	TypeHello  = "hello"
	TypeOpen   = "open"
	TypeQuit   = "quit"
	TypeResult = "result"
	TypeClosed = "closed"
)

// Message is one line of JSON on the socket. Fields not used by a type are
// left empty.
type Message struct {
	Type string `json:"type"`

	// hello, both ways
	Protocol int    `json:"protocol,omitempty"`
	Build    string `json:"build,omitempty"`
	Reviews  int    `json:"reviews,omitempty"` // open windows (app to client)

	// open
	Dir      string   `json:"dir,omitempty"`      // absolute working directory
	DiffArgs []string `json:"diffArgs"`           // nil: file mode
	Env      []string `json:"env,omitempty"`      // KEY=value for git
	Devtools bool     `json:"devtools,omitempty"` // enable the web inspector
	Wait     bool     `json:"wait,omitempty"`     // send closed when the window closes

	// result
	Error   string `json:"error,omitempty"`
	Focused bool   `json:"focused,omitempty"` // an existing window was brought forward
	Title   string `json:"title,omitempty"`
	URL     string `json:"url,omitempty"`
}

// maxMessage bounds one line, so a confused peer cannot exhaust memory.
const maxMessage = 1 << 20

// conn reads and writes Messages over a stream.
type conn struct {
	r  *bufio.Reader
	w  io.Writer
	mu sync.Mutex // serialises writes
}

func newConn(rw io.ReadWriter) *conn {
	return &conn{r: bufio.NewReaderSize(rw, 4096), w: rw}
}

func (c *conn) send(m Message) error {
	encoded, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.w.Write(append(encoded, '\n'))
	return err
}

// errTooLong is returned for a line over maxMessage.
var errTooLong = errors.New("message too long")

func (c *conn) receive() (Message, error) {
	var line []byte
	for {
		chunk, isPrefix, err := c.r.ReadLine()
		if err != nil {
			return Message{}, err
		}
		line = append(line, chunk...)
		if len(line) > maxMessage {
			return Message{}, errTooLong
		}
		if !isPrefix {
			break
		}
	}
	var m Message
	if err := json.Unmarshal(line, &m); err != nil {
		return Message{}, fmt.Errorf("invalid message: %w", err)
	}
	return m, nil
}

// gitEnvPaths are the GIT_* variables naming paths, which git resolves
// against its working directory; the app runs git elsewhere, so they are
// made absolute against the invoking shell's directory.
var gitEnvPaths = []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_COMMON_DIR"}

// GitEnv picks the GIT_* variables out of environ, with paths made absolute
// against dir. These are the only variables a request carries: the app keeps
// its own environment otherwise.
func GitEnv(environ []string, dir string) []string {
	var env []string
	for _, kv := range environ {
		key, value, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(key, "GIT_") {
			continue
		}
		for _, p := range gitEnvPaths {
			if key == p && value != "" && !filepath.IsAbs(value) {
				value = filepath.Join(dir, value)
			}
		}
		env = append(env, key+"="+value)
	}
	return env
}

// WithoutGitEnv drops GIT_* variables, so the app's own environment does not
// leak the first shell's repository into later requests.
func WithoutGitEnv(environ []string) []string {
	var env []string
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

var buildOnce = sync.OnceValue(func() string {
	exe, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	f, err := os.Open(exe)
	if err != nil {
		return "unknown"
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(h.Sum(nil)[:12])
})

// BuildID identifies this executable's contents, so that a client can tell
// whether the running app is the same build as itself.
func BuildID() string { return buildOnce() }

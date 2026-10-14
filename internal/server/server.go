// Package server is the viewer's loopback HTTP server. It serves the page,
// the page's own endpoints under /ui/, and the agent API under /v1/.
package server

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/statiolake/review-relay/internal/review"
	"github.com/statiolake/review-relay/internal/workspace"
)

//go:embed assets
var assets embed.FS

const maxRequestBytes = 2 << 20

// Server wires the workspace, the review store and the viewer preferences to
// HTTP. Construct it with New, then call Listen.
type Server struct {
	ws      *workspace.Workspace
	store   *review.Store
	prefs   *Prefs
	cliPath string

	addr string // host:port once listening

	mu      sync.Mutex
	clients map[chan event]struct{}
}

type event struct {
	name string
	data any
}

// New creates a server. cliPath is the executable agents should run.
func New(ws *workspace.Workspace, store *review.Store, prefs *Prefs, cliPath string) *Server {
	s := &Server{ws: ws, store: store, prefs: prefs, cliPath: cliPath, clients: map[chan event]struct{}{}}
	store.Subscribe(func() { s.broadcast(event{name: "state", data: struct{}{}}) })
	return s
}

// Listen binds a random loopback port and returns the endpoint URL and a
// function that serves until the listener fails.
func (s *Server) Listen() (endpoint string, serve func() error, err error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	s.addr = ln.Addr().String()
	return "http://" + s.addr, func() error { return http.Serve(ln, s.Handler()) }, nil
}

// Endpoint is the base URL; valid after Listen.
func (s *Server) Endpoint() string { return "http://" + s.addr }

// Handler routes every request. Tests may call it after setting the address
// with SetAddr instead of Listen.
func (s *Server) Handler() http.Handler {
	static, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err) // the embedded directory is fixed at build time
	}
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", http.FileServerFS(static))
	mux.Handle("GET /app.js", http.FileServerFS(static))
	mux.Handle("GET /style.css", http.FileServerFS(static))
	mux.Handle("GET /icon.svg", http.FileServerFS(static))
	mux.Handle("GET /highlight/", http.FileServerFS(static))

	mux.HandleFunc("GET /health", s.handle(func(*http.Request) (int, any, error) {
		return http.StatusOK, map[string]any{"ok": true, "version": 2, "root": s.ws.Root}, nil
	}))
	mux.HandleFunc("GET /v1/comments", s.handle(s.listComments))
	mux.HandleFunc("POST /v1/comments", s.handle(s.addComment))
	mux.HandleFunc("DELETE /v1/comments", s.handle(s.clearComments))
	mux.HandleFunc("POST /v1/comments/{id}/replies", s.handle(s.replyComment))
	mux.HandleFunc("PATCH /v1/comments/{id}", s.handle(s.updateComment))
	mux.HandleFunc("DELETE /v1/comments/{id}", s.handle(s.removeComment))
	mux.HandleFunc("POST /v1/navigate", s.handle(s.navigate))

	mux.HandleFunc("GET /ui/session", s.handle(s.session))
	mux.HandleFunc("GET /ui/files", s.handle(s.files))
	mux.HandleFunc("GET /ui/view", s.handle(s.view))
	mux.HandleFunc("PUT /ui/overall", s.handle(s.setOverall))
	mux.HandleFunc("DELETE /ui/review", s.handle(s.clearReview))
	mux.HandleFunc("PUT /ui/reviewed", s.handle(s.setReviewed))
	mux.HandleFunc("PUT /ui/layout", s.handle(s.setLayout))
	mux.HandleFunc("GET /ui/instructions", s.handle(s.instructions))
	mux.HandleFunc("GET /ui/events", s.events)
	return s.guard(mux)
}

// SetAddr fixes the host:port the guard accepts, for tests using Handler.
func (s *Server) SetAddr(addr string) { s.addr = addr }

// guard only lets through requests addressed to this server by name (which
// defeats DNS rebinding) and either from a non-browser client or from the
// viewer page itself.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.addr {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Unexpected Host header."})
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.addr {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "Browser-origin requests are not allowed."})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// httpError carries a status code for an error the client caused.
type httpError struct {
	status int
	err    error
}

func (e httpError) Error() string { return e.err.Error() }

func badRequest(err error) error { return httpError{http.StatusBadRequest, err} }

// handle adapts a handler returning (status, value, error) and is the single
// place errors become responses: httpError keeps its status, model validation
// errors are 400, ErrNotFound is 404, and anything else is a 500.
func (s *Server) handle(fn func(*http.Request) (int, any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		status, value, err := fn(r)
		if err != nil {
			var he httpError
			var ve *review.ValidationError
			switch {
			case errors.As(err, &he):
				status = he.status
			case errors.As(err, &ve):
				status = http.StatusBadRequest
			case errors.Is(err, review.ErrNotFound):
				status = http.StatusNotFound
			default:
				status = http.StatusInternalServerError
			}
			value = map[string]string{"error": err.Error()}
		}
		writeJSON(w, status, value)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(value) // a failed write only means the client went away
}

func readJSON(r *http.Request, into any) error {
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return httpError{http.StatusUnsupportedMediaType, errors.New("Content-Type must be application/json")}
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxRequestBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxRequestBytes {
		return badRequest(errors.New("request body is too large"))
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		return badRequest(fmt.Errorf("invalid JSON: %w", err))
	}
	return nil
}

func (s *Server) listComments(r *http.Request) (int, any, error) {
	state := s.store.Snapshot()
	if p := r.URL.Query().Get("path"); p != "" {
		filtered := []review.Comment{}
		for _, c := range state.Comments {
			if c.Path == p {
				filtered = append(filtered, c)
			}
		}
		state.Comments = filtered
	}
	return http.StatusOK, state, nil
}

func (s *Server) addComment(r *http.Request) (int, any, error) {
	var input review.CommentInput
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	c, err := s.store.Add(input)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, map[string]any{"comment": c}, nil
}

func (s *Server) replyComment(r *http.Request) (int, any, error) {
	var input review.CommentInput
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	c, err := s.store.Reply(r.PathValue("id"), input)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusCreated, map[string]any{"comment": c}, nil
}

func (s *Server) updateComment(r *http.Request) (int, any, error) {
	var input struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	c, err := s.store.Update(r.PathValue("id"), input.Body)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"comment": c}, nil
}

func (s *Server) removeComment(r *http.Request) (int, any, error) {
	remaining, err := s.store.Remove(r.PathValue("id"))
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"removed": true, "remainingComments": remaining}, nil
}

func (s *Server) clearComments(*http.Request) (int, any, error) {
	removed, err := s.store.Clear()
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"removed": removed, "remainingComments": 0}, nil
}

// navigateTarget is what the page scrolls to.
type navigateTarget struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	EndLine   int    `json:"endLine"`
	CommentID string `json:"commentId,omitempty"`
}

func (s *Server) navigate(r *http.Request) (int, any, error) {
	var input struct {
		CommentID string `json:"commentId,omitempty"`
		review.Location
	}
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	var target navigateTarget
	if input.CommentID != "" {
		if input.Path != "" || input.Line != 0 || input.EndLine != nil {
			return 0, nil, badRequest(errors.New("give either commentId or path and line, not both"))
		}
		c, err := s.store.Find(input.CommentID)
		if err != nil {
			return 0, nil, err
		}
		target = navigateTarget{Path: c.Path, Line: c.Line, EndLine: c.EndLine, CommentID: c.ID}
	} else {
		line, end, err := input.Normalize()
		if err != nil {
			return 0, nil, err
		}
		target = navigateTarget{Path: input.Path, Line: line, EndLine: end}
	}
	lines, notice, err := s.ws.ReadLines(target.Path)
	if err != nil {
		return 0, nil, badRequest(err)
	}
	if notice != "" {
		return 0, nil, badRequest(fmt.Errorf("%s: %s", target.Path, notice))
	}
	if target.EndLine >= len(lines) {
		return 0, nil, badRequest(fmt.Errorf("navigation range is outside the file (%d lines)", len(lines)))
	}
	if !s.broadcast(event{name: "navigate", data: target}) {
		return 0, nil, httpError{http.StatusServiceUnavailable, errors.New("no viewer window is connected")}
	}
	return http.StatusOK, map[string]any{"navigated": target}, nil
}

func (s *Server) session(*http.Request) (int, any, error) {
	return http.StatusOK, map[string]any{
		"root":     s.ws.Root,
		"diff":     s.ws.DiffMode(),
		"diffArgs": s.ws.DiffArgs,
		"layout":   s.prefs.Layout(),
	}, nil
}

func (s *Server) files(*http.Request) (int, any, error) {
	files, err := s.ws.Files()
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{"files": files, "reviewed": s.prefs.Reviewed()}, nil
}

func (s *Server) view(r *http.Request) (int, any, error) {
	p := r.URL.Query().Get("path")
	if err := review.ValidatePath(p); err != nil {
		return 0, nil, err
	}
	v, err := s.ws.View(p)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, v, nil
}

func (s *Server) setOverall(r *http.Request) (int, any, error) {
	var input struct {
		Overall string `json:"overall"`
	}
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{}, s.store.SetOverall(input.Overall)
}

func (s *Server) clearReview(*http.Request) (int, any, error) {
	return http.StatusOK, map[string]any{}, s.store.ClearReview()
}

func (s *Server) setReviewed(r *http.Request) (int, any, error) {
	var input struct {
		Path string `json:"path"`
		Hash string `json:"hash"` // empty clears the mark
	}
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	if err := review.ValidatePath(input.Path); err != nil {
		return 0, nil, err
	}
	return http.StatusOK, map[string]any{}, s.prefs.SetReviewed(input.Path, input.Hash)
}

func (s *Server) setLayout(r *http.Request) (int, any, error) {
	var input struct {
		Layout string `json:"layout"`
	}
	if err := readJSON(r, &input); err != nil {
		return 0, nil, err
	}
	if input.Layout != "inline" && input.Layout != "split" {
		return 0, nil, badRequest(errors.New(`layout must be "inline" or "split"`))
	}
	return http.StatusOK, map[string]any{}, s.prefs.SetLayout(input.Layout)
}

func (s *Server) instructions(*http.Request) (int, any, error) {
	return http.StatusOK, map[string]string{"text": AgentInstructions(s.Endpoint(), s.cliPath, s.ws.Root)}, nil
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher := w.(http.Flusher) // net/http's ResponseWriter always supports it
	ch := make(chan event, 16)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.clients, ch)
		s.mu.Unlock()
	}()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev := <-ch:
			data, err := json.Marshal(ev.data)
			if err != nil {
				panic(err) // events carry plain structs
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.name, data); err != nil {
				return // client went away; its context ends this stream
			}
			flusher.Flush()
		}
	}
}

// broadcast sends ev to every connected page and reports whether any was.
func (s *Server) broadcast(ev event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- ev:
		default:
			// The page is far behind; it refetches state on any event, so
			// dropping one only matters for navigation, which is user-visible.
		}
	}
	return len(s.clients) > 0
}

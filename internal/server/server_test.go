package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/statiolake/review-relay/internal/review"
	"github.com/statiolake/review-relay/internal/workspace"
)

func newTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	ws, err := workspace.Open(root, nil)
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	store, err := review.Open(filepath.Join(data, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefs, err := OpenPrefs(filepath.Join(data, "viewer.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ws, store, prefs, "/path/to/review-relay")
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	s.SetAddr(ts.Listener.Addr().String())
	ts.Config.Handler = s.Handler()
	t.Cleanup(ts.Close)
	return ts, root
}

func do(t *testing.T, ts *httptest.Server, method, path, body string, header map[string]string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestGuardRejectsForeignOriginsAndHosts(t *testing.T) {
	ts, _ := newTestServer(t)
	if code, _ := do(t, ts, "GET", "/v1/comments", "", map[string]string{"Origin": "https://evil.example"}); code != 403 {
		t.Fatalf("foreign origin: %d", code)
	}
	if code, _ := do(t, ts, "GET", "/v1/comments", "", map[string]string{"Origin": ts.URL}); code != 200 {
		t.Fatalf("own origin: %d", code)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
	req.Host = "attacker.example:80"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatalf("foreign host: %d", resp.StatusCode)
	}
}

func TestCommentLifecycleAndErrors(t *testing.T) {
	ts, _ := newTestServer(t)
	code, out := do(t, ts, "POST", "/v1/comments", `{"path":"a.txt","line":1,"body":"hi"}`, nil)
	if code != 201 {
		t.Fatalf("add: %d %v", code, out)
	}
	id := out["comment"].(map[string]any)["id"].(string)

	if code, out := do(t, ts, "POST", "/v1/comments/"+id+"/replies", `{"path":"a.txt","line":2,"body":"re"}`, nil); code != 201 {
		t.Fatalf("reply: %d %v", code, out)
	}
	if code, _ := do(t, ts, "PATCH", "/v1/comments/"+id, `{"body":"edited"}`, nil); code != 200 {
		t.Fatalf("edit: %d", code)
	}
	cases := []struct {
		method, path, body string
		want               int
	}{
		{"POST", "/v1/comments", `{"path":"../x","line":0,"body":"b"}`, 400},
		{"POST", "/v1/comments", `{"path":"a.txt","line":0,"body":""}`, 400},
		{"POST", "/v1/comments", `{"path":"a.txt","line":0,"body":"b","extra":1}`, 400},
		{"POST", "/v1/comments/00000000-0000-4000-8000-000000000000/replies", `{"path":"a.txt","line":0,"body":"b"}`, 404},
		{"DELETE", "/v1/comments/nope", "", 400},
		{"POST", "/v1/navigate", `{"path":"a.txt","line":9}`, 400},
		{"POST", "/v1/navigate", `{"path":"a.txt","line":0}`, 503}, // no window connected
		{"PUT", "/ui/layout", `{"layout":"diagonal"}`, 400},
	}
	for _, c := range cases {
		if code, out := do(t, ts, c.method, c.path, c.body, nil); code != c.want {
			t.Errorf("%s %s %s: %d %v, want %d", c.method, c.path, c.body, code, out, c.want)
		} else if out["error"] == "" {
			t.Errorf("%s %s: no error message", c.method, c.path)
		}
	}
	code, out = do(t, ts, "GET", "/v1/comments?path=a.txt", "", nil)
	comments := out["comments"].([]any)
	if code != 200 || len(comments) != 2 || comments[0].(map[string]any)["line"].(float64) != 2 {
		t.Fatalf("list: %d %v", code, out)
	}
	if code, out := do(t, ts, "DELETE", "/v1/comments/"+id, "", nil); code != 200 || out["remainingComments"].(float64) != 0 {
		t.Fatalf("delete: %d %v", code, out)
	}
}

func TestNavigateReachesConnectedPage(t *testing.T) {
	ts, _ := newTestServer(t)
	resp, err := http.Get(ts.URL + "/ui/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	lines := make(chan string)
	go func() {
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	// The events handler registers the client before flushing headers, so
	// the page counts as connected once the response arrived.
	if code, out := do(t, ts, "POST", "/v1/navigate", `{"path":"a.txt","line":1,"endLine":2}`, nil); code != 200 {
		t.Fatalf("navigate: %d %v", code, out)
	}
	deadline := time.After(2 * time.Second)
	for {
		select {
		case line := <-lines:
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, `"endLine":2`) {
				return
			}
		case <-deadline:
			t.Fatal("navigate event not received")
		}
	}
}

func TestReviewedAndViews(t *testing.T) {
	ts, _ := newTestServer(t)
	if code, _ := do(t, ts, "PUT", "/ui/reviewed", `{"path":"a.txt","hash":"abc"}`, nil); code != 200 {
		t.Fatal(code)
	}
	_, out := do(t, ts, "GET", "/ui/files", "", nil)
	if out["reviewed"].(map[string]any)["a.txt"] != "abc" {
		t.Fatalf("files %v", out)
	}
	code, out := do(t, ts, "GET", "/ui/view?path=a.txt", "", nil)
	if code != 200 || len(out["lines"].([]any)) != 3 {
		t.Fatalf("view %d %v", code, out)
	}
	if code, _ := do(t, ts, "GET", "/ui/view?path=../../etc/passwd", "", nil); code != 400 {
		t.Fatalf("escape: %d", code)
	}
	resp, _ := http.Get(ts.URL + "/")
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(page), "app.js") {
		t.Fatal("index not served")
	}
	for _, asset := range []string{"/app.js", "/style.css", "/icon.svg", "/highlight/highlight.min.js"} {
		resp, err := http.Get(ts.URL + asset)
		if err != nil || resp.StatusCode != 200 {
			t.Fatalf("GET %s: %v %v", asset, resp, err)
		}
		resp.Body.Close()
	}
}

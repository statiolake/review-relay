package server

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
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
	marks, err := OpenMarks(filepath.Join(data, "reviews", "test"))
	if err != nil {
		t.Fatal(err)
	}
	s := New(ws, store, prefs, marks, "/path/to/review-relay")
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
	if code, out := do(t, ts, "PUT", "/v1/comments/"+id+"/resolved", `{"resolved":true}`, nil); code != 200 || out["comment"].(map[string]any)["resolved"] != true {
		t.Fatalf("resolve: %d %v", code, out)
	}
	if _, out := do(t, ts, "GET", "/v1/comments?resolved=false", "", nil); len(out["comments"].([]any)) != 0 {
		t.Fatalf("unresolved filter kept a resolved thread: %v", out)
	}
	if _, out := do(t, ts, "GET", "/v1/comments?resolved=true", "", nil); len(out["comments"].([]any)) != 2 {
		t.Fatalf("resolved filter: %v", out)
	}
	for _, c := range []struct {
		body string
		want int
	}{{`{}`, 400}, {`{"resolved":"yes"}`, 400}} {
		if code, _ := do(t, ts, "PUT", "/v1/comments/"+id+"/resolved", c.body, nil); code != c.want {
			t.Errorf("resolve %s: %d, want %d", c.body, code, c.want)
		}
	}
	if code, _ := do(t, ts, "GET", "/v1/comments?resolved=maybe", "", nil); code != 400 {
		t.Errorf("bad resolved filter: %d", code)
	}
	if code, _ := do(t, ts, "PUT", "/v1/comments/00000000-0000-4000-8000-000000000000/resolved", `{"resolved":true}`, nil); code != 404 {
		t.Errorf("resolve missing: %d", code)
	}
	if code, out := do(t, ts, "PUT", "/v1/comments/"+id+"/resolved", `{"resolved":false}`, nil); code != 200 || out["comment"].(map[string]any)["resolved"] != nil {
		t.Fatalf("unresolve: %d %v", code, out)
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
	if code, _ := do(t, ts, "PUT", "/ui/reviewed", `{"path":"a.txt","hash":"abc"}`, nil); code != 400 {
		t.Fatalf("marking outside a diff: %d", code)
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

func TestChangesSinceReview(t *testing.T) {
	root := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", root, "-c", "user.name=test", "-c", "user.email=test@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "main")
	write("one\n")
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	write("one\ntwo\n")

	ws, err := workspace.Open(root, []string{"HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	data := t.TempDir()
	store, _ := review.Open(filepath.Join(data, "state.json"))
	prefs, _ := OpenPrefs(filepath.Join(data, "viewer.json"))
	marksDir := filepath.Join(data, "reviews", "test")
	marks, err := OpenMarks(marksDir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	ts.Start()
	defer ts.Close()
	s := New(ws, store, prefs, marks, "/path/to/review-relay")
	s.SetAddr(ts.Listener.Addr().String())
	ts.Config.Handler = s.Handler()

	hash := func() string {
		_, out := do(t, ts, "GET", "/ui/files", "", nil)
		return out["files"].([]any)[0].(map[string]any)["hash"].(string)
	}
	kinds := func(query string) (string, bool) {
		t.Helper()
		code, out := do(t, ts, "GET", "/ui/view?path=a.txt"+query, "", nil)
		if code != 200 {
			t.Fatalf("view: %d %v", code, out)
		}
		k := ""
		for _, l := range out["lines"].([]any) {
			k += l.(map[string]any)["k"].(string)
		}
		return k, out["sinceReview"] == true
	}
	mark := func(h string) int {
		code, _ := do(t, ts, "PUT", "/ui/reviewed", `{"path":"a.txt","hash":"`+h+`"}`, nil)
		return code
	}

	if k, since := kinds(""); k != " +" || since {
		t.Fatalf("before review: %q %v", k, since)
	}
	if code := mark("stale"); code != 409 {
		t.Fatalf("stale mark: %d", code)
	}
	reviewed := hash()
	if code := mark(reviewed); code != 200 {
		t.Fatalf("mark: %d", code)
	}
	if k, since := kinds(""); k != " +" || since {
		t.Fatalf("reviewed and unchanged: %q %v", k, since)
	}

	write("one\ntwo\nthree\n")
	if k, since := kinds(""); k != "  +" || !since {
		t.Fatalf("since review: %q %v", k, since)
	}
	if k, since := kinds("&full=1"); k != " ++" || since {
		t.Fatalf("full: %q %v", k, since)
	}

	// The same content coming back restores the mark, and marks survive a reopen.
	write("one\ntwo\n")
	if hash() != reviewed {
		t.Fatal("same content, different id")
	}
	reopened, err := OpenMarks(marksDir)
	if err != nil || reopened.All()["a.txt"] != reviewed {
		t.Fatalf("reopened %v %v", reopened.All(), err)
	}

	if code, _ := do(t, ts, "DELETE", "/ui/reviewed", "", nil); code != 200 {
		t.Fatal(code)
	}
	if _, err := os.Stat(marksDir); !os.IsNotExist(err) {
		t.Fatalf("snapshots left behind: %v", err)
	}
}

func TestCorruptMarksAreReported(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "marks.json"), []byte("{"), 0o600)
	if _, err := OpenMarks(dir); err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Fatalf("OpenMarks = %v", err)
	}
}

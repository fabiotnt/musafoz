package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const secret = "token=SECRET123"

func server(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/files/photo.jpg", func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte("image bytes"))
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 2000))
	})
	mux.HandleFunc("/chunked", func(w http.ResponseWriter, r *http.Request) {
		for range 4 {
			w.Write(make([]byte, 500))
			w.(http.Flusher).Flush()
		}
	})
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &hits
}

func newFetcher(t *testing.T, max int64, timeout time.Duration) *Fetcher {
	t.Helper()
	f, err := New(max, timeout, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestFetchReusesDownloads(t *testing.T) {
	srv, hits := server(t)
	f := newFetcher(t, 1000, time.Minute)
	var logs []string
	f.Logf = func(format string, a ...any) { logs = append(logs, format) }
	u := srv.URL + "/files/photo.jpg?" + secret
	p1, err := f.Fetch(context.Background(), u)
	if err != nil {
		t.Fatal(err)
	}
	p2, err := f.Fetch(context.Background(), u)
	if err != nil || p1 != p2 || hits.Load() != 1 {
		t.Errorf("second fetch: %q %q %v, %d requests", p1, p2, err, hits.Load())
	}
	data, _ := os.ReadFile(p1)
	if string(data) != "image bytes" {
		t.Errorf("content %q", data)
	}
	dir := f.Dir
	f.Close()
	if _, err := os.Stat(dir); err == nil {
		t.Error("Close left the download directory")
	}
}

func TestFetchErrorsHideQuery(t *testing.T) {
	srv, _ := server(t)
	cases := map[string]string{
		"/missing.jpg": "404 Not Found",
		"/big":         "larger than the 1000 byte limit",
		"/chunked":     "larger than the 1000 byte limit",
		"/slow":        "timed out after 200ms",
	}
	for path, want := range cases {
		f := newFetcher(t, 1000, 200*time.Millisecond)
		_, err := f.Fetch(context.Background(), srv.URL+path+"?"+secret+"#frag")
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", path, err, want)
			continue
		}
		if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "frag") {
			t.Errorf("%s: error shows the query: %v", path, err)
		}
		if !strings.Contains(err.Error(), srv.URL+path) {
			t.Errorf("%s: error does not name the URL: %v", path, err)
		}
		entries, _ := os.ReadDir(f.Dir)
		if len(entries) != 0 {
			t.Errorf("%s: partial download left behind", path)
		}
	}
}

func TestFetchCancel(t *testing.T) {
	srv, _ := server(t)
	f := newFetcher(t, 1000, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	start := time.Now()
	_, err := f.Fetch(ctx, srv.URL+"/slow")
	if !errors.Is(err, context.Canceled) || time.Since(start) > 3*time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}

func TestFetchRejectsBadURLs(t *testing.T) {
	f := newFetcher(t, 1000, time.Minute)
	for _, u := range []string{"http://", "https:///x", "http://%zz/"} {
		if _, err := f.Fetch(context.Background(), u); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	// Connection errors name the host but not the query.
	_, err := f.Fetch(context.Background(), "http://127.0.0.1:1/x.jpg?"+secret)
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("err = %v", err)
	}
}

func TestNameAndRedact(t *testing.T) {
	names := map[string]string{
		"https://example.com/a/b/My%20Photo.jpg?x=1#y": "My Photo.jpg",
		"https://example.com/":                         "download",
		"https://example.com":                          "download",
		"https://example.com/dir/":                     "dir",
		"https://example.com/a%3Ab%2Fc.png":            "a_b_c.png",
		"https://example.com/..":                       "download",
	}
	for in, want := range names {
		if got := Name(in); got != want {
			t.Errorf("Name(%q) = %q, want %q", in, got, want)
		}
	}
	red := map[string]string{
		"https://user:pass@example.com/a.jpg?token=1#f": "https://example.com/a.jpg",
		"http://example.com:8080/x":                     "http://example.com:8080/x",
		"not a url?secret":                              "not a url",
	}
	for in, want := range red {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]bool{"http://x": true, "HTTPS://x": true, "ftp://x": false, "C:\\x": false, "file.jpg": false} {
		if IsURL(in) != want {
			t.Errorf("IsURL(%q) != %v", in, want)
		}
	}
}

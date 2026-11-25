// Package fetch downloads remote sources given as direct HTTP or HTTPS
// URLs into a temporary directory. Logs and errors never show the query,
// fragment, or credentials of a URL.
package fetch

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Default limits.
const (
	// MaxImageBytes limits image downloads.
	MaxImageBytes = 100 << 20
	// MaxVideoBytes limits video downloads.
	MaxVideoBytes = 8 << 30
	// ImageTimeout and VideoTimeout limit one whole download.
	ImageTimeout = 2 * time.Minute
	VideoTimeout = 60 * time.Minute
	// HeaderTimeout limits the wait for the response headers.
	HeaderTimeout = 30 * time.Second
)

// IsURL reports whether s is an HTTP or HTTPS URL.
func IsURL(s string) bool {
	l := strings.ToLower(s)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://")
}

// Redact returns the URL without credentials, query, or fragment, for logs
// and errors. A string that is not a URL is returned with anything after
// '?' or '#' removed.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		if i := strings.IndexAny(raw, "?#"); i >= 0 {
			return raw[:i]
		}
		return raw
	}
	r := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}
	return r.String()
}

// Name returns the file name for a URL: the last segment of its path,
// unescaped, without the query, and with characters that are not allowed
// in file names replaced by '_'. It is "download" when the path has no
// segment.
func Name(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "download"
	}
	// Split the escaped path, so an escaped slash stays in the name.
	segs := strings.Split(strings.TrimRight(u.EscapedPath(), "/"), "/")
	seg, err := url.PathUnescape(segs[len(segs)-1])
	if err != nil || seg == "" || seg == "." || seg == ".." {
		return "download"
	}
	return SafeName(seg)
}

// SafeName replaces characters that are invalid in file names on Windows,
// macOS, or Linux, and trims dots and spaces at the ends.
func SafeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7F || strings.ContainsRune(`<>:"/\|?*`, r) {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". ")
	if out == "" {
		return "download"
	}
	return out
}

// Error reports a failed download. Its message names the redacted URL.
type Error struct {
	URL string
	Err error
}

func (e *Error) Error() string { return fmt.Sprintf("download %s: %v", e.URL, e.Err) }
func (e *Error) Unwrap() error { return e.Err }

// Fetcher downloads URLs into Dir. A URL fetched twice in one Fetcher is
// downloaded once; the file is reused. Remove Dir when done.
type Fetcher struct {
	Dir      string
	Client   *http.Client
	MaxBytes int64
	Timeout  time.Duration
	// Logf receives progress messages. It may be nil.
	Logf func(format string, args ...any)

	mu    sync.Mutex
	files map[string]*download
	n     int
}

type download struct {
	once sync.Once
	path string
	err  error
}

// New returns a Fetcher that stores files in a new temporary directory.
func New(maxBytes int64, timeout time.Duration, logf func(string, ...any)) (*Fetcher, error) {
	dir, err := os.MkdirTemp("", "musafoz-download-")
	if err != nil {
		return nil, err
	}
	return &Fetcher{Dir: dir, MaxBytes: maxBytes, Timeout: timeout, Logf: logf}, nil
}

// Close removes the downloaded files.
func (f *Fetcher) Close() error {
	if f == nil || f.Dir == "" {
		return nil
	}
	return os.RemoveAll(f.Dir)
}

func defaultClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = HeaderTimeout
	return &http.Client{Transport: tr}
}

// Fetch downloads the URL, or returns the file of an earlier fetch of the
// same URL. The download stops at MaxBytes, after Timeout, or when ctx is
// canceled.
func (f *Fetcher) Fetch(ctx context.Context, raw string) (string, error) {
	f.mu.Lock()
	if f.files == nil {
		f.files = map[string]*download{}
	}
	d, ok := f.files[raw]
	if !ok {
		f.n++
		d = &download{path: filepath.Join(f.Dir, fmt.Sprintf("source-%d", f.n))}
		f.files[raw] = d
	}
	f.mu.Unlock()
	d.once.Do(func() { d.err = f.get(ctx, raw, d.path) })
	if d.err != nil {
		return "", d.err
	}
	return d.path, nil
}

func (f *Fetcher) logf(format string, args ...any) {
	if f.Logf != nil {
		f.Logf(format, args...)
	}
}

func (f *Fetcher) get(ctx context.Context, raw, dst string) error {
	red := Redact(raw)
	fail := func(err error) error { return &Error{URL: red, Err: err} }
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fail(errors.New("not a valid HTTP or HTTPS URL"))
	}
	if f.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, f.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return fail(errors.New("not a valid HTTP or HTTPS URL"))
	}
	req.Header.Set("User-Agent", "musafoz")
	client := f.Client
	if client == nil {
		client = defaultClient()
	}
	f.logf("downloading %s", red)
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return fail(f.describe(ctx, err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fail(fmt.Errorf("server answered %s", resp.Status))
	}
	if f.MaxBytes > 0 && resp.ContentLength > f.MaxBytes {
		return fail(fmt.Errorf("file is %d bytes, larger than the %d byte limit", resp.ContentLength, f.MaxBytes))
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(err)
	}
	body := io.Reader(resp.Body)
	if f.MaxBytes > 0 {
		body = io.LimitReader(resp.Body, f.MaxBytes+1)
	}
	n, err := io.Copy(out, body)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil && f.MaxBytes > 0 && n > f.MaxBytes {
		err = fmt.Errorf("file is larger than the %d byte limit", f.MaxBytes)
	} else if err == nil && resp.ContentLength >= 0 && n != resp.ContentLength {
		err = fmt.Errorf("received %d of %d bytes", n, resp.ContentLength)
	} else if err != nil {
		err = f.describe(ctx, err)
	}
	if err != nil {
		os.Remove(dst)
		return fail(err)
	}
	f.logf("downloaded %s (%d bytes in %.1f s)", red, n, time.Since(start).Seconds())
	return nil
}

// describe turns a transport error into a message without the URL.
func (f *Fetcher) describe(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Errorf("timed out after %s", f.Timeout)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return errors.New("the server did not answer in time")
	}
	return err
}

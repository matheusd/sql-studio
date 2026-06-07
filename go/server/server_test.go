package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

// stubDB is a minimal Database used by the routing/mount tests. It returns
// trivial, valid responses so handlers exercise the wiring without a real
// SQLite file (and thus without CGO at the data layer).
type stubDB struct{}

func (stubDB) Overview(ctx context.Context) (*Overview, error) {
	return &Overview{FileName: "stub"}, nil
}
func (stubDB) Tables(ctx context.Context) (*Tables, error) { return &Tables{}, nil }
func (stubDB) Table(ctx context.Context, name string) (*Table, error) {
	return &Table{Name: name}, nil
}
func (stubDB) TableData(ctx context.Context, name string, page int) (*TableData, error) {
	return &TableData{}, nil
}
func (stubDB) TablesWithColumns(ctx context.Context) (*TablesWithColumns, error) {
	return &TablesWithColumns{}, nil
}
func (stubDB) Query(ctx context.Context, query string) (*Query, error) { return &Query{}, nil }
func (stubDB) Erd(ctx context.Context) (*Erd, error)                   { return &Erd{}, nil }

// testUI is a minimal stand-in for the embedded frontend: just an index.html
// with the placeholders New() rewrites. This keeps the test independent of the
// real built assets.
func testUI() fstest.MapFS {
	return fstest.MapFS{
		"index.html": {Data: []byte("<!-- __BASE__ -->\n<script src=\"/__ASSETS_PATH__/app.js\"></script>")},
	}
}

func newTestServer(t *testing.T, opts Options) *Server {
	t.Helper()
	srv, err := New(stubDB{}, testUI(), opts)
	if err != nil {
		t.Fatalf("New: unexpected error: %v", err)
	}
	return srv
}

// TestHostMuxMount verifies the documented mount mechanism: a host server routes
// the Prefix()+"/" subtree straight at Handler() on its own *http.ServeMux, and
// both the JSON API and the SPA are reachable through it.
func TestHostMuxMount(t *testing.T) {
	tests := []struct {
		name     string
		basePath string
	}{
		{name: "no base path", basePath: ""},
		{name: "with base path", basePath: "/admin"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(t, Options{BasePath: tc.basePath, Version: "test"})

			// Mount exactly as the documentation prescribes, on a host mux that
			// also owns its own unrelated routes.
			host := http.NewServeMux()
			host.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("ok"))
			})
			host.Handle(srv.Prefix()+"/", srv.Handler())

			ts := httptest.NewServer(host)
			defer ts.Close()

			// The host's own route is unaffected by the mount.
			requireBody(t, ts.URL+"/health", http.StatusOK, "ok")

			// The JSON API is reachable under the prefix.
			res := requireGet(t, ts.URL+srv.Prefix()+"/api/metadata")
			if res.StatusCode != http.StatusOK {
				t.Fatalf("GET /api/metadata: status = %d, want %d", res.StatusCode, http.StatusOK)
			}
			if got := res.Header.Get("Content-Type"); got != "application/json" {
				t.Fatalf("GET /api/metadata: Content-Type = %q, want %q", got, "application/json")
			}

			// The SPA index is served for an app route under the prefix, and the
			// asset path placeholder was rewritten to the full prefix.
			body := requireBody(t, ts.URL+srv.Prefix()+"/", http.StatusOK, "")
			requireContains(t, string(body), `content="`+srv.Prefix()+`"`)
			requireContains(t, string(body), srv.Prefix()+"/app.js")
		})
	}
}

// TestNoShutdownInert verifies that with NoShutdown the shutdown endpoint sends
// no signal and metadata advertises can_shutdown=false (which gates the UI
// button), whether or not the app is mounted in a host mux.
func TestNoShutdownInert(t *testing.T) {
	srv := newTestServer(t, Options{NoShutdown: true})

	host := http.NewServeMux()
	host.Handle(srv.Prefix()+"/", srv.Handler())
	ts := httptest.NewServer(host)
	defer ts.Close()

	// metadata reports the button is disabled.
	body := requireBody(t, ts.URL+srv.Prefix()+"/api/metadata", http.StatusOK, "")
	requireContains(t, string(body), `"can_shutdown":false`)

	// POST /api/shutdown returns 200 but must not enqueue a shutdown signal.
	res, err := http.Post(ts.URL+srv.Prefix()+"/api/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/shutdown: unexpected error: %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatalf("closing response body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/shutdown: status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	select {
	case <-srv.shutdownCh:
		t.Fatal("shutdown signal was sent despite NoShutdown")
	default:
	}
}

// TestShutdownEnabled is the counterpart: with NoShutdown unset, the endpoint
// enqueues exactly one shutdown signal.
func TestShutdownEnabled(t *testing.T) {
	srv := newTestServer(t, Options{})

	host := http.NewServeMux()
	host.Handle(srv.Prefix()+"/", srv.Handler())
	ts := httptest.NewServer(host)
	defer ts.Close()

	body := requireBody(t, ts.URL+srv.Prefix()+"/api/metadata", http.StatusOK, "")
	requireContains(t, string(body), `"can_shutdown":true`)

	res, err := http.Post(ts.URL+srv.Prefix()+"/api/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /api/shutdown: unexpected error: %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatalf("closing response body: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/shutdown: status = %d, want %d", res.StatusCode, http.StatusOK)
	}

	select {
	case <-srv.shutdownCh:
	default:
		t.Fatal("expected a shutdown signal")
	}
}

// TestStandaloneRootRedirect guards against regressing the standalone behavior:
// the bare root redirects into the app subtree. This path is unreachable when
// mounted in a host mux (the host owns "/"), so it is tested against Handler()
// directly rather than through a host mux.
func TestStandaloneRootRedirect(t *testing.T) {
	srv := newTestServer(t, Options{})

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	// Don't follow redirects so we can inspect the hop.
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	res, err := client.Get(ts.URL + "/")
	if err != nil {
		t.Fatalf("GET /: unexpected error: %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatalf("closing response body: %v", err)
	}
	if res.StatusCode != http.StatusFound {
		t.Fatalf("GET /: status = %d, want %d", res.StatusCode, http.StatusFound)
	}
	if got := res.Header.Get("Location"); got != srv.Prefix()+"/" {
		t.Fatalf("GET /: Location = %q, want %q", got, srv.Prefix()+"/")
	}
}

func requireGet(t *testing.T, url string) *http.Response {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: unexpected error: %v", url, err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func requireBody(t *testing.T, url string, wantStatus int, wantBody string) []byte {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s: unexpected error: %v", url, err)
	}
	defer func() {
		if cerr := res.Body.Close(); cerr != nil {
			t.Errorf("closing response body: %v", cerr)
		}
	}()
	if res.StatusCode != wantStatus {
		t.Fatalf("GET %s: status = %d, want %d", url, res.StatusCode, wantStatus)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s: reading body: %v", url, err)
	}
	if wantBody != "" && string(body) != wantBody {
		t.Fatalf("GET %s: body = %q, want %q", url, string(body), wantBody)
	}
	return body
}

// requireContains fails the test if s does not contain substr.
func requireContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Fatalf("expected %q to contain %q", s, substr)
	}
}

package ui

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/koreapyj/bifrost-plugin-payload/payload"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var wasmBytes = []byte("\x00asm\x01\x00\x00\x00fake-engine")

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, err := zw.Write(b)
	require.NoError(t, err)
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func testFS(t *testing.T) fstest.MapFS {
	return fstest.MapFS{
		"index.html":     {Data: []byte(`<!doctype html><base href="` + basePlaceholder + `"><script src="app.js"></script>`)},
		"app.js":         {Data: []byte("console.log(1)")},
		"app.css":        {Data: []byte("body{}")},
		"wasm_exec.js":   {Data: []byte("// go")},
		"engine.wasm.gz": {Data: gz(t, wasmBytes)},
	}
}

// sessionServer mimics GET /api/session/is-auth-enabled.
type sessionServer struct {
	*httptest.Server
	calls atomic.Int32
}

func newSessionServer(t *testing.T, h func(w http.ResponseWriter, r *http.Request)) *sessionServer {
	s := &sessionServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.calls.Add(1)
		if r.URL.Path != sessionEndpoint || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// bifrostLike validates the token cookie / bearer like session.go does.
func bifrostLike(authEnabled bool, valid string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := ""
		if a := r.Header.Get("Authorization"); strings.HasPrefix(a, "Bearer ") {
			tok = strings.TrimPrefix(a, "Bearer ")
		}
		if tok == "" {
			if c, err := r.Cookie("token"); err == nil {
				tok = c.Value
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"is_auth_enabled":%v,"has_valid_token":%v,"auth_type":"password"}`, authEnabled, tok != "" && tok == valid)
	}
}

func newTestServer(t *testing.T, apiBase string) *Server {
	t.Helper()
	s, err := newWithFS(payload.UIConfig{Enabled: true, APIBase: apiBase}, testFS(t))
	require.NoError(t, err)
	require.NotNil(t, s)
	t.Cleanup(s.Close)
	return s
}

func get(path string, headers map[string]string) *schemas.HTTPRequest {
	if headers == nil {
		headers = map[string]string{}
	}
	return &schemas.HTTPRequest{Method: http.MethodGet, Path: path, Headers: headers, Query: map[string]string{}, PathParams: map[string]string{}}
}

func cookie(tok string) map[string]string {
	return map[string]string{"cookie": "theme=dark; token=" + tok}
}

const uiPath = payload.DefaultUIPath

func TestNewDisabledAndNil(t *testing.T) {
	s, err := New(payload.UIConfig{Enabled: false})
	assert.NoError(t, err)
	assert.Nil(t, s)

	var nilS *Server
	resp, err := nilS.PreAuthHook(nil, get(uiPath, nil))
	assert.NoError(t, err)
	assert.Nil(t, resp)
	nilS.Close()
}

func TestNewPathNormalization(t *testing.T) {
	cases := map[string]string{
		"":                      payload.DefaultUIPath,
		"/v1/models/_x/ui/":     "/v1/models/_x/ui",
		"/v1/models/_x/ui///":   "/v1/models/_x/ui",
		" /v1/models/_x/admin ": "/v1/models/_x/admin",
		"/elsewhere":            "/elsewhere",
	}
	for in, want := range cases {
		s, err := newWithFS(payload.UIConfig{Enabled: true, Path: in}, fstest.MapFS{})
		require.NoError(t, err, in)
		assert.Equal(t, want, s.path, in)
	}
	for _, bad := range []string{"relative", "/", "///", "/a/../b", "/a//b", "/a b", "/a?x"} {
		_, err := newWithFS(payload.UIConfig{Enabled: true, Path: bad}, fstest.MapFS{})
		assert.Error(t, err, bad)
	}
}

func TestNewEmbeddedFS(t *testing.T) {
	// Must work both with a populated dist and with only .keep.
	s, err := New(payload.UIConfig{Enabled: true})
	require.NoError(t, err)
	require.NotNil(t, s)
	defer s.Close()
	idx := s.assets["index.html"]
	if idx == nil {
		t.Log("web/dist is not built; embedded asset checks skipped")
		return
	}
	assert.Contains(t, string(idx.body), `<base href="`+payload.DefaultUIPath+`/"`)
	assert.NotContains(t, string(idx.body), basePlaceholder)
	for _, name := range []string{"app.js", "app.css", "wasm_exec.js", "engine.wasm"} {
		assert.NotNil(t, s.assets[name], name)
	}
	assert.NotNil(t, s.assets["engine.wasm"].gz, "engine.wasm is embedded pre-gzipped")
}

func TestGateMatrix(t *testing.T) {
	const valid = "11111111-2222-3333-4444-555555555555"
	slow := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(3 * time.Second):
		case <-r.Context().Done():
		}
		bifrostLike(true, valid)(w, r)
	})
	cases := []struct {
		name    string
		handler func(w http.ResponseWriter, r *http.Request)
		apiBase string
		headers map[string]string
		want    bool
		calls   int32
	}{
		{name: "valid cookie", handler: bifrostLike(true, valid), headers: cookie(valid), want: true, calls: 1},
		{name: "valid bearer", handler: bifrostLike(true, valid), headers: map[string]string{"AUTHORIZATION": "Bearer " + valid}, want: true, calls: 1},
		{name: "invalid token", handler: bifrostLike(true, valid), headers: cookie("nope"), want: false, calls: 1},
		{name: "no credential", handler: bifrostLike(true, valid), headers: map[string]string{"Cookie": "theme=dark"}, want: false, calls: 0},
		{name: "basic auth only", handler: bifrostLike(true, valid), headers: map[string]string{"Authorization": "Basic Zm9vOmJhcg=="}, want: false, calls: 0},
		{name: "auth disabled", handler: bifrostLike(false, valid), headers: cookie(valid), want: false, calls: 1},
		{name: "bad json", handler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>") }, headers: cookie(valid), want: false, calls: 1},
		{name: "missing fields", handler: func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"has_valid_token":true}`) }, headers: cookie(valid), want: false, calls: 1},
		{name: "wrapped envelope", handler: func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"data":{"is_auth_enabled":true,"has_valid_token":true}}`)
		}, headers: cookie(valid), want: false, calls: 1},
		{name: "non-2xx", handler: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(500)
			fmt.Fprint(w, `{"is_auth_enabled":true,"has_valid_token":true}`)
		}, headers: cookie(valid), want: false, calls: 1},
		{name: "redirect not followed", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}, headers: cookie(valid), want: false, calls: 1},
		{name: "empty api_base", apiBase: "-", headers: cookie(valid), want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ss *sessionServer
			base := ""
			if tc.apiBase != "-" {
				ss = newSessionServer(t, tc.handler)
				base = ss.URL
			}
			s := newTestServer(t, base)
			resp, err := s.PreAuthHook(nil, get(uiPath, tc.headers))
			require.NoError(t, err)
			if tc.want {
				require.NotNil(t, resp)
				assert.Equal(t, 200, resp.StatusCode)
			} else {
				assert.Nil(t, resp)
			}
			if ss != nil {
				assert.Equal(t, tc.calls, ss.calls.Load())
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		s, err := newWithFS(payload.UIConfig{Enabled: true, APIBase: slow.URL}, testFS(t))
		require.NoError(t, err)
		s.gate.client.Timeout = 200 * time.Millisecond
		start := time.Now()
		resp, err := s.PreAuthHook(nil, get(uiPath, cookie(valid)))
		assert.NoError(t, err)
		assert.Nil(t, resp)
		assert.Less(t, time.Since(start), 2*time.Second)
	})

	t.Run("network error", func(t *testing.T) {
		dead := httptest.NewServer(http.NotFoundHandler())
		url := dead.URL
		dead.Close()
		s := newTestServer(t, url)
		resp, err := s.PreAuthHook(nil, get(uiPath, cookie(valid)))
		assert.NoError(t, err)
		assert.Nil(t, resp)
	})
}

func TestGateForwardsOnlyCredentials(t *testing.T) {
	var seen http.Header
	ss := newSessionServer(t, func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Clone()
		bifrostLike(true, "tok")(w, r)
	})
	s := newTestServer(t, ss.URL)
	resp, _ := s.PreAuthHook(nil, get(uiPath, map[string]string{
		"Cookie":          "token=tok",
		"Authorization":   "Bearer tok",
		"X-Api-Key":       "secret",
		"X-Forwarded-For": "1.2.3.4",
	}))
	require.NotNil(t, resp)
	assert.Equal(t, "token=tok", seen.Get("Cookie"))
	assert.Equal(t, "Bearer tok", seen.Get("Authorization"))
	assert.Empty(t, seen.Get("X-Api-Key"))
	assert.Empty(t, seen.Get("X-Forwarded-For"))
}

func TestGateCache(t *testing.T) {
	const valid = "good"
	ss := newSessionServer(t, bifrostLike(true, valid))
	s := newTestServer(t, ss.URL)

	now := time.Unix(1_700_000_000, 0)
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = time.Now })

	for i := 0; i < 3; i++ {
		assert.NotNil(t, must(s.PreAuthHook(nil, get(uiPath+"/app.js", cookie(valid)))))
		assert.Nil(t, must(s.PreAuthHook(nil, get(uiPath+"/app.js", cookie("bad")))))
	}
	assert.Equal(t, int32(2), ss.calls.Load(), "pass and failure decisions are cached")

	now = now.Add(31 * time.Second)
	assert.NotNil(t, must(s.PreAuthHook(nil, get(uiPath, cookie(valid)))))
	assert.Equal(t, int32(3), ss.calls.Load(), "expired entries are re-checked")

	// Bounded size.
	s.gate.max = 4
	for i := 0; i < 20; i++ {
		_ = must(s.PreAuthHook(nil, get(uiPath, cookie(fmt.Sprintf("t%d", i)))))
		s.gate.mu.Lock()
		n := len(s.gate.cache)
		s.gate.mu.Unlock()
		assert.LessOrEqual(t, n, 4)
	}
}

func must(r *schemas.HTTPResponse, err error) *schemas.HTTPResponse {
	if err != nil {
		panic(err)
	}
	return r
}

func TestNonUIPathsReturnNilWithoutWork(t *testing.T) {
	ss := newSessionServer(t, bifrostLike(true, "good"))
	s := newTestServer(t, ss.URL)
	reqs := []*schemas.HTTPRequest{
		get("/v1/models", cookie("good")),
		get("/v1/models/openai/gpt-4o", cookie("good")),
		get(uiPath+"x", cookie("good")),
		get(uiPath+"/other.js", cookie("good")),
		get(uiPath+"/engine.wasm.gz", cookie("good")),
		get(uiPath+"/sub/app.js", cookie("good")),
		get(uiPath+"/.keep", cookie("good")),
		get("/v1/chat/completions", cookie("good")),
		{Method: http.MethodPost, Path: uiPath, Headers: cookie("good")},
		{Method: http.MethodHead, Path: uiPath, Headers: cookie("good")},
	}
	for _, r := range reqs {
		resp, err := s.PreAuthHook(nil, r)
		assert.NoError(t, err)
		assert.Nil(t, resp, r.Method+" "+r.Path)
	}
	assert.Equal(t, int32(0), ss.calls.Load())
	resp, err := s.PreAuthHook(nil, nil)
	assert.NoError(t, err)
	assert.Nil(t, resp)
}

func TestAssetsAndHeaders(t *testing.T) {
	ss := newSessionServer(t, bifrostLike(true, "good"))
	s := newTestServer(t, ss.URL)
	cases := []struct {
		path, ct string
	}{
		{uiPath, "text/html; charset=utf-8"},
		{uiPath + "/", "text/html; charset=utf-8"},
		{uiPath + "/index.html", "text/html; charset=utf-8"},
		{uiPath + "/app.js", "application/javascript; charset=utf-8"},
		{uiPath + "/app.css", "text/css; charset=utf-8"},
		{uiPath + "/wasm_exec.js", "application/javascript; charset=utf-8"},
		{uiPath + "/engine.wasm", "application/wasm"},
	}
	for _, tc := range cases {
		resp := must(s.PreAuthHook(nil, get(tc.path, cookie("good"))))
		require.NotNil(t, resp, tc.path)
		assert.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, tc.ct, resp.Headers["Content-Type"], tc.path)
		assert.Equal(t, "no-store", resp.Headers["Cache-Control"])
		assert.Equal(t, "nosniff", resp.Headers["X-Content-Type-Options"])
		assert.Contains(t, resp.Headers["Content-Security-Policy"], "'wasm-unsafe-eval'")
	}
	idx := must(s.PreAuthHook(nil, get(uiPath, cookie("good"))))
	assert.Contains(t, string(idx.Body), `<base href="`+uiPath+`/">`)
	assert.NotContains(t, string(idx.Body), basePlaceholder)
}

func TestWasmGzipNegotiation(t *testing.T) {
	ss := newSessionServer(t, bifrostLike(true, "good"))
	s := newTestServer(t, ss.URL)
	with := func(ae string) map[string]string {
		h := cookie("good")
		if ae != "" {
			h["accept-encoding"] = ae
		}
		return h
	}
	for _, ae := range []string{"gzip", "gzip, deflate, br", "br;q=1.0, gzip;q=0.8", "*"} {
		resp := must(s.PreAuthHook(nil, get(uiPath+"/engine.wasm", with(ae))))
		require.NotNil(t, resp)
		assert.Equal(t, "gzip", resp.Headers["Content-Encoding"], ae)
		assert.Equal(t, "Accept-Encoding", resp.Headers["Vary"])
		raw, err := gunzip(resp.Body)
		require.NoError(t, err)
		assert.Equal(t, wasmBytes, raw)
	}
	for _, ae := range []string{"", "identity", "br", "gzip;q=0", "*;q=0", "deflate, gzip;q=0.0"} {
		resp := must(s.PreAuthHook(nil, get(uiPath+"/engine.wasm", with(ae))))
		require.NotNil(t, resp)
		assert.Empty(t, resp.Headers["Content-Encoding"], ae)
		assert.Equal(t, wasmBytes, resp.Body, ae)
	}
}

func TestMissingIndexIsStealth(t *testing.T) {
	ss := newSessionServer(t, bifrostLike(true, "good"))
	s, err := newWithFS(payload.UIConfig{Enabled: true, APIBase: ss.URL}, fstest.MapFS{".keep": {}})
	require.NoError(t, err)
	for _, p := range []string{uiPath, uiPath + "/app.js", uiPath + "/engine.wasm"} {
		resp, err := s.PreAuthHook(nil, get(p, cookie("good")))
		assert.NoError(t, err)
		assert.Nil(t, resp)
	}
	assert.Equal(t, int32(0), ss.calls.Load())
}

func TestCookieValue(t *testing.T) {
	assert.Equal(t, "abc", cookieValue("a=1; token=abc; b=2", "token"))
	assert.Equal(t, "abc", cookieValue(`token="abc"`, "token"))
	assert.Equal(t, "", cookieValue("xtoken=abc", "token"))
	assert.Equal(t, "", cookieValue("", "token"))
}

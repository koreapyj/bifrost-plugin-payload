// Package ui serves the payload plugin management UI from
// HTTPTransportPreAuthHook.
//
// The UI is a static single page application (embedded from web/dist) that is
// only served after a server-side session check against Bifrost's own
// dashboard session endpoint. When the check fails for any reason the hook
// returns (nil, nil) so the request continues through the normal Bifrost
// pipeline unchanged ("stealth"): the response is exactly what Bifrost would
// return for a non-existent model.
package ui

import (
	"embed"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/koreapyj/bifrost-plugin-payload/internal/logx"
	"github.com/koreapyj/bifrost-plugin-payload/payload"
	"github.com/maximhq/bifrost/core/schemas"
)

// dist holds the built web assets. Only web/dist/.keep is present in a fresh
// checkout; the Makefile/CI populate the directory before building the plugin.
//
//go:embed all:web/dist
var dist embed.FS

// modelsPrefix is the only route prefix that is guaranteed to reach the
// plugin's HTTP transport middleware for GET requests (the always-registered
// GET /v1/models/{model:*} inference route).
const modelsPrefix = "/v1/models/"

// basePlaceholder is replaced in index.html with the HTML-escaped "{path}/"
// so that relative asset URLs resolve under the UI path whether or not the
// page URL has a trailing slash.
const basePlaceholder = "__PAYLOAD_UI_BASE__"

// Server serves the embedded UI behind a server-side session gate.
type Server struct {
	path   string // normalized mount path, no trailing slash
	prefix string // path + "/"
	assets map[string]*asset
	gate   *gate

	missingOnce sync.Once
}

// New returns a Server for cfg. It returns (nil, nil) when the UI is disabled.
func New(cfg payload.UIConfig) (*Server, error) {
	sub, err := fs.Sub(dist, "web/dist")
	if err != nil {
		return nil, fmt.Errorf("ui: embedded assets: %w", err)
	}
	return newWithFS(cfg, sub)
}

// newWithFS is New with an explicit asset file system (used by tests).
func newWithFS(cfg payload.UIConfig, assets fs.FS) (*Server, error) {
	if !cfg.Enabled {
		return nil, nil
	}
	p, err := normalizePath(cfg.Path)
	if err != nil {
		return nil, err
	}
	warnPath(p)
	apiBase := strings.TrimRight(strings.TrimSpace(cfg.APIBase), "/")
	if apiBase == "" {
		logx.Warnf("ui: ui.api_base is not set; the management UI at %s stays hidden (every request falls through to Bifrost)", p)
	}
	s := &Server{
		path:   p,
		prefix: p + "/",
		assets: loadAssets(assets, p+"/"),
		gate:   newGate(apiBase, defaultGateTimeout, defaultCacheTTL, defaultCacheSize),
	}
	return s, nil
}

// normalizePath validates the configured UI path and strips trailing slashes.
func normalizePath(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		p = payload.DefaultUIPath
	}
	if !strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("ui: ui.path %q must start with '/'", p)
	}
	if strings.ContainsAny(p, "?#\\ \t\r\n\"'<>") {
		return "", fmt.Errorf("ui: ui.path %q contains invalid characters", p)
	}
	p = strings.TrimRight(p, "/")
	if p == "" {
		return "", errors.New("ui: ui.path must not be the root path")
	}
	if strings.Contains(p, "//") || strings.Contains(p, "/./") || strings.Contains(p, "/../") ||
		strings.HasSuffix(p, "/.") || strings.HasSuffix(p, "/..") {
		return "", fmt.Errorf("ui: ui.path %q is not a clean path", p)
	}
	return p, nil
}

// warnPath logs configuration hazards of the mount path.
func warnPath(p string) {
	if !strings.HasPrefix(p, modelsPrefix) || len(p) == len(modelsPrefix) {
		logx.Warnf("ui: ui.path %q is not under %s; only GET %s* is guaranteed to pass through plugin middleware, other paths may never reach the UI", p, modelsPrefix, modelsPrefix)
		return
	}
	first := strings.SplitN(strings.TrimPrefix(p, modelsPrefix), "/", 2)[0]
	for _, prov := range schemas.StandardProviders {
		if strings.EqualFold(first, string(prov)) {
			logx.Warnf("ui: ui.path %q starts with the provider name %q; requests that fail the session gate will be forwarded as a model retrieve to that provider. Use a non-provider prefix such as %q", p, first, payload.DefaultUIPath)
			return
		}
	}
}

// PreAuthHook short-circuits requests for the UI path when the session gate
// passes. In every other case it returns (nil, nil) so the request continues
// through the normal Bifrost pipeline unchanged. A nil receiver is valid.
func (s *Server) PreAuthHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if s == nil || req == nil || req.Method != http.MethodGet {
		return nil, nil
	}
	name, ok := s.assetName(req.Path)
	if !ok {
		return nil, nil
	}
	a := s.assets[name]
	if a == nil {
		if name == "index.html" {
			s.missingOnce.Do(func() {
				logx.Warnf("ui: index.html is not embedded (web/dist was not built); the management UI is unavailable")
			})
		}
		return nil, nil
	}
	if !s.gate.allow(req) {
		return nil, nil
	}
	return a.response(req.CaseInsensitiveHeaderLookup("Accept-Encoding")), nil
}

// assetName maps a request path to an asset name. It returns false for any
// path that is not part of the UI.
func (s *Server) assetName(p string) (string, bool) {
	if !strings.HasPrefix(p, s.path) {
		return "", false
	}
	rest := p[len(s.path):]
	if rest == "" || rest == "/" {
		return "index.html", true
	}
	if rest[0] != '/' {
		return "", false
	}
	rest = rest[1:]
	if _, ok := assetTypes[rest]; ok {
		return rest, true
	}
	return "", false
}

// Close releases resources held by the server. A nil receiver is valid.
func (s *Server) Close() {
	if s == nil || s.gate == nil {
		return
	}
	s.gate.close()
}

// timeNow is replaceable in tests.
var timeNow = time.Now

// escapeBase returns the HTML-escaped base href for prefix.
func escapeBase(prefix string) string { return html.EscapeString(prefix) }

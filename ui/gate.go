package ui

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/koreapyj/bifrost-plugin-payload/internal/logx"
	"github.com/maximhq/bifrost/core/schemas"
)

const (
	defaultGateTimeout = 2 * time.Second
	defaultCacheTTL    = 30 * time.Second
	defaultCacheSize   = 1024

	sessionEndpoint = "/api/session/is-auth-enabled"
	maxSessionBody  = 64 << 10
)

// gate checks a request's dashboard session against Bifrost's
// GET /api/session/is-auth-enabled endpoint and caches the decision.
type gate struct {
	apiBase string
	client  *http.Client
	ttl     time.Duration
	max     int

	mu    sync.Mutex
	cache map[[32]byte]cacheEntry
}

type cacheEntry struct {
	ok      bool
	expires time.Time
}

func newGate(apiBase string, timeout, ttl time.Duration, max int) *gate {
	return &gate{
		apiBase: apiBase,
		client: &http.Client{
			Timeout: timeout,
			// Never follow redirects: the credential must only go to api_base.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
			Transport: &http.Transport{
				Proxy:               nil,
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     90 * time.Second,
			},
		},
		ttl:   ttl,
		max:   max,
		cache: make(map[[32]byte]cacheEntry),
	}
}

func (g *gate) close() { g.client.CloseIdleConnections() }

// credential extracts the dashboard session credential the same way Bifrost's
// session handler does: an "Authorization: Bearer" token first, otherwise the
// "token" cookie. The returned key distinguishes the two sources.
func credential(req *schemas.HTTPRequest) (string, bool) {
	if auth := req.CaseInsensitiveHeaderLookup("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		if t := strings.TrimPrefix(auth, "Bearer "); t != "" {
			return "bearer\x00" + t, true
		}
	}
	if t := cookieValue(req.CaseInsensitiveHeaderLookup("Cookie"), "token"); t != "" {
		return "cookie\x00" + t, true
	}
	return "", false
}

// cookieValue returns the value of the named cookie in a Cookie header.
func cookieValue(header, name string) string {
	for _, part := range strings.Split(header, ";") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(k) == name {
			return strings.Trim(strings.TrimSpace(v), "\"")
		}
	}
	return ""
}

// allow reports whether the request carries a valid dashboard session.
func (g *gate) allow(req *schemas.HTTPRequest) bool {
	if g.apiBase == "" {
		logx.Debugf("ui: gate denied: ui.api_base is not set")
		return false
	}
	cred, ok := credential(req)
	if !ok {
		logx.Debugf("ui: gate denied: no session credential")
		return false
	}
	key := sha256.Sum256([]byte(cred))
	now := timeNow()
	g.mu.Lock()
	if e, hit := g.cache[key]; hit && now.Before(e.expires) {
		g.mu.Unlock()
		if !e.ok {
			logx.Debugf("ui: gate denied (cached)")
		}
		return e.ok
	}
	g.mu.Unlock()

	ok = g.check(req)

	g.mu.Lock()
	if len(g.cache) >= g.max {
		for k, e := range g.cache {
			if !now.Before(e.expires) {
				delete(g.cache, k)
			}
		}
		if len(g.cache) >= g.max {
			clear(g.cache)
		}
	}
	g.cache[key] = cacheEntry{ok: ok, expires: now.Add(g.ttl)}
	g.mu.Unlock()
	return ok
}

// sessionStatus is the subset of the is-auth-enabled response we rely on.
type sessionStatus struct {
	IsAuthEnabled *bool `json:"is_auth_enabled"`
	HasValidToken *bool `json:"has_valid_token"`
}

// check performs the loopback session call. Only the Cookie and
// Authorization headers of the original request are forwarded.
func (g *gate) check(req *schemas.HTTPRequest) bool {
	ctx, cancel := context.WithTimeout(context.Background(), g.client.Timeout)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, g.apiBase+sessionEndpoint, nil)
	if err != nil {
		logx.Debugf("ui: gate denied: build request: %v", err)
		return false
	}
	if c := req.CaseInsensitiveHeaderLookup("Cookie"); c != "" {
		hreq.Header.Set("Cookie", c)
	}
	if a := req.CaseInsensitiveHeaderLookup("Authorization"); a != "" {
		hreq.Header.Set("Authorization", a)
	}
	hreq.Header.Set("Accept", "application/json")
	resp, err := g.client.Do(hreq)
	if err != nil {
		logx.Debugf("ui: gate denied: session check failed: %v", err)
		return false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSessionBody))
	if err != nil {
		logx.Debugf("ui: gate denied: read session response: %v", err)
		return false
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		logx.Debugf("ui: gate denied: session check status %d", resp.StatusCode)
		return false
	}
	var st sessionStatus
	if err := json.Unmarshal(body, &st); err != nil {
		logx.Debugf("ui: gate denied: parse session response: %v", err)
		return false
	}
	if st.IsAuthEnabled == nil || st.HasValidToken == nil {
		logx.Debugf("ui: gate denied: session response lacks is_auth_enabled/has_valid_token")
		return false
	}
	if !*st.IsAuthEnabled {
		logx.Debugf("ui: gate denied: dashboard authentication is disabled")
		return false
	}
	if !*st.HasValidToken {
		logx.Debugf("ui: gate denied: invalid or expired session token")
		return false
	}
	return true
}

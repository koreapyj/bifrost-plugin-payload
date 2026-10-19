package ui

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"strconv"
	"strings"

	"github.com/koreapyj/bifrost-plugin-payload/internal/logx"
	"github.com/maximhq/bifrost/core/schemas"
)

// assetTypes lists every servable asset and its Content-Type.
var assetTypes = map[string]string{
	"index.html":   "text/html; charset=utf-8",
	"app.js":       "application/javascript; charset=utf-8",
	"app.css":      "text/css; charset=utf-8",
	"wasm_exec.js": "application/javascript; charset=utf-8",
	"engine.wasm":  "application/wasm",
}

// contentSecurityPolicy allows only same-origin scripts, styles, and fetches,
// plus WebAssembly compilation for the engine.
const contentSecurityPolicy = "default-src 'none'; script-src 'self' 'wasm-unsafe-eval'; " +
	"style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:; " +
	"font-src 'self'; base-uri 'self'; form-action 'none'; frame-ancestors 'none'"

// asset is one embedded file ready to be served.
type asset struct {
	contentType string
	body        []byte // identity body; nil when only gz is available
	gz          []byte // gzip-compressed body, if precompressed
}

// loadAssets reads the known assets from fsys. Missing files are skipped.
func loadAssets(fsys fs.FS, base string) map[string]*asset {
	out := make(map[string]*asset, len(assetTypes))
	for name, ct := range assetTypes {
		if name == "engine.wasm" {
			if gz, err := fs.ReadFile(fsys, "engine.wasm.gz"); err == nil {
				out[name] = &asset{contentType: ct, gz: gz}
				continue
			}
		}
		b, err := fs.ReadFile(fsys, name)
		if err != nil {
			continue
		}
		if name == "index.html" {
			b = bytes.ReplaceAll(b, []byte(basePlaceholder), []byte(escapeBase(base)))
		}
		out[name] = &asset{contentType: ct, body: b}
	}
	return out
}

// response builds the HTTP response for the asset, honouring Accept-Encoding
// for precompressed assets.
func (a *asset) response(acceptEncoding string) *schemas.HTTPResponse {
	h := map[string]string{
		"Content-Type":            a.contentType,
		"Cache-Control":           "no-store",
		"X-Content-Type-Options":  "nosniff",
		"Content-Security-Policy": contentSecurityPolicy,
		"Referrer-Policy":         "same-origin",
		"X-Frame-Options":         "DENY",
	}
	body := a.body
	if a.gz != nil {
		h["Vary"] = "Accept-Encoding"
		if acceptsGzip(acceptEncoding) {
			h["Content-Encoding"] = "gzip"
			body = a.gz
		} else {
			raw, err := gunzip(a.gz)
			if err != nil {
				logx.Errorf("ui: decompress asset: %v", err)
				return &schemas.HTTPResponse{StatusCode: 500, Headers: map[string]string{
					"Content-Type":  "text/plain; charset=utf-8",
					"Cache-Control": "no-store",
				}, Body: []byte("asset decompression failed\n")}
			}
			body = raw
		}
	}
	return &schemas.HTTPResponse{StatusCode: 200, Headers: h, Body: body}
}

func gunzip(b []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// acceptsGzip reports whether an Accept-Encoding header value allows gzip
// (explicitly or via "*"), treating q=0 as a refusal.
func acceptsGzip(v string) bool {
	star := false
	for _, part := range strings.Split(v, ",") {
		fields := strings.Split(part, ";")
		coding := strings.ToLower(strings.TrimSpace(fields[0]))
		if coding != "gzip" && coding != "x-gzip" && coding != "*" {
			continue
		}
		ok := true
		for _, p := range fields[1:] {
			p = strings.TrimSpace(p)
			if len(p) > 2 && (p[0] == 'q' || p[0] == 'Q') && p[1] == '=' {
				if q, err := strconv.ParseFloat(strings.TrimSpace(p[2:]), 64); err == nil && q <= 0 {
					ok = false
				}
			}
		}
		if coding == "*" {
			star = ok
			continue
		}
		return ok
	}
	return star
}

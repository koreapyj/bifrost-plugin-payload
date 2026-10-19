// Command payload is a Bifrost dynamic (.so) plugin that rewrites request
// payloads with rules selected by CEL conditions.
//
// Rules run at three stages:
//
//   - http: HTTPTransportPreHook, on the client's JSON request body;
//   - request: PreRequestHook, once per request before fallback fan-out;
//   - llm: PreLLMHook, once per provider attempt.
//
// At the request and llm stages the document is either the provider-native
// RawRequestBody (when the transport set BifrostContextKeyUseRawRequestBody)
// or the Bifrost view of the request (see package bifrostview).
//
// Errors are logged and never block a request: every hook passes the request
// through unchanged when something goes wrong.
//
// The exported symbol signatures must match the type assertions of Bifrost's
// shared object loader (framework/plugins/soloader.go) exactly.
package main

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/koreapyj/bifrost-plugin-payload/internal/logx"
	"github.com/koreapyj/bifrost-plugin-payload/payload"
	"github.com/koreapyj/bifrost-plugin-payload/ui"
	"github.com/maximhq/bifrost/core/schemas"
)

// pluginName is the name Bifrost registers the plugin under.
const pluginName = "payload"

// state is an immutable snapshot of a successfully initialized configuration.
type state struct {
	cfg    *payload.Config
	engine *payload.Engine
	ui     *ui.Server
}

var (
	// current holds the active state; nil before the first successful Init.
	current atomic.Pointer[state]
	// initMu serializes Init calls so the previous UI server is closed exactly once.
	initMu sync.Mutex
)

func loadState() *state { return current.Load() }

// Init validates and compiles the configuration and atomically replaces the
// active rules. On any error the previous state stays in place.
//
// Bifrost calls Init again on every PUT /api/plugins/payload: ReloadPlugin
// re-runs LoadPlugin, and plugin.Open on the same path returns the already
// loaded object, so the same package globals are reused.
func Init(config any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("payload: Init panicked: %v", r)
			logx.Errorf("%v", err)
		}
	}()
	initMu.Lock()
	defer initMu.Unlock()

	cfg, err := payload.ConfigFromAny(config)
	if err != nil {
		logx.Errorf("Init: invalid configuration, keeping previous rules: %v", err)
		return fmt.Errorf("payload: invalid configuration: %w", err)
	}
	engine, err := payload.Compile(cfg)
	if err != nil {
		logx.Errorf("Init: configuration rejected, keeping previous rules: %v", err)
		return fmt.Errorf("payload: %w", err)
	}
	srv, err := ui.New(cfg.UI)
	if err != nil {
		logx.Errorf("Init: UI configuration rejected, keeping previous rules: %v", err)
		return fmt.Errorf("payload: ui: %w", err)
	}
	old := current.Swap(&state{cfg: cfg, engine: engine, ui: srv})
	if old != nil {
		old.ui.Close()
	}
	logx.Infof("Init: loaded %d rule(s) (http=%t request=%t llm=%t, ui=%t)", len(cfg.Rules),
		engine.HasStage(payload.StageHTTP), engine.HasStage(payload.StageRequest), engine.HasStage(payload.StageLLM), srv != nil)
	return nil
}

// GetName returns the plugin name.
func GetName() string { return pluginName }

// Cleanup is intentionally non-destructive.
//
// On reload Bifrost first calls Init on the same loaded object (installing the
// new state) and only afterwards calls Cleanup on the previous plugin
// instance, once per plugin type it implements (LLM, MCP, A2A). Because all
// instances share this package's globals, tearing down state here would
// disable the configuration that was just installed. A removed plugin's
// hooks are no longer invoked, so retaining the state is harmless.
func Cleanup() error {
	logx.Debugf("Cleanup called (state retained; it is replaced by the next successful Init)")
	return nil
}

// HTTPTransportPreAuthHook serves the management UI (see package ui). It runs
// before Bifrost authenticates the request.
func HTTPTransportPreAuthHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (resp *schemas.HTTPResponse, err error) {
	defer recoverHook("HTTPTransportPreAuthHook", func() { resp, err = nil, nil })
	st := loadState()
	if st == nil {
		return nil, nil
	}
	return st.ui.PreAuthHook(ctx, req)
}

// HTTPTransportPreHook runs the http stage on the client request body.
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (resp *schemas.HTTPResponse, err error) {
	defer recoverHook("HTTPTransportPreHook", func() { resp, err = nil, nil })
	if st := loadState(); st != nil {
		runHTTPStage(ctx, st, req)
	}
	return nil, nil
}

// HTTPTransportPostHook is a no-op.
func HTTPTransportPostHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	return nil
}

// HTTPTransportStreamChunkHook passes stream chunks through unchanged.
func HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	return chunk, nil
}

// HTTPTransportResponseHeadersHook is a no-op.
func HTTPTransportResponseHeadersHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
	return nil
}

// PreRequestHook runs the request stage once per request, before fallbacks
// are fanned out. It has no return value, so a rewritten request is
// published by replacing req's inner request pointer; the previous inner
// request and everything it references stay untouched.
func PreRequestHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (err error) {
	defer recoverHook("PreRequestHook", func() { err = nil })
	st := loadState()
	if st == nil || req == nil {
		return nil
	}
	if out := runCoreStage(ctx, st, req, payload.StageRequest); out != req {
		*req = *out
	}
	return nil
}

// PreLLMHook runs the llm stage once per provider attempt and returns a
// copy-on-write request so other attempts (fallbacks share inner structs with
// the primary request) are not affected.
func PreLLMHook(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (out *schemas.BifrostRequest, sc *schemas.LLMPluginShortCircuit, err error) {
	defer recoverHook("PreLLMHook", func() { out, sc, err = req, nil, nil })
	st := loadState()
	if st == nil || req == nil {
		return req, nil, nil
	}
	return runCoreStage(ctx, st, req, payload.StageLLM), nil, nil
}

// PostLLMHook passes the response through unchanged.
func PostLLMHook(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) {
	return resp, bifrostErr, nil
}

// recoverHook converts a panic inside a hook into a log line and lets the
// hook pass the request through via reset.
func recoverHook(hook string, reset func()) {
	if r := recover(); r != nil {
		logx.Errorf("%s: recovered from panic, passing request through: %v", hook, r)
		reset()
	}
}

func main() {}

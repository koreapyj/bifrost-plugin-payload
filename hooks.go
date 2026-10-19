package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/koreapyj/bifrost-plugin-payload/bifrostview"
	"github.com/koreapyj/bifrost-plugin-payload/internal/logx"
	"github.com/koreapyj/bifrost-plugin-payload/payload"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
)

// ctxKey is the plugin-owned context key type. Being a distinct named type it
// cannot collide with Bifrost's BifrostContextKey values, and the transport
// does not mistake it for a router path parameter (only plain string keys are
// copied into HTTPRequest.PathParams).
type ctxKey string

// httpInfoKey stores the *httpInfo captured by the http stage. Values set in
// HTTPTransportPreHook are copied to the fasthttp user values and from there
// into the request's BifrostContext, so the request and llm stages see them.
const httpInfoKey ctxKey = "payload-plugin-http-info"

// httpInfo is what the http stage remembers about the client request.
type httpInfo struct {
	Path         string
	FromProtocol string
	Headers      map[string]string // lower-cased keys
}

// reqLog writes a request-scoped message to stderr and to the request's plugin
// logs (ctx.Log), which Bifrost shows under "Plugin Logs" in the LLM log view.
// ctx.Log is a no-op outside a plugin-scoped context.
func reqLog(ctx *schemas.BifrostContext, level schemas.LogLevel, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	switch level {
	case schemas.LogLevelDebug:
		logx.Debugf("%s", msg)
	case schemas.LogLevelWarn:
		logx.Warnf("%s", msg)
	case schemas.LogLevelError:
		logx.Errorf("%s", msg)
	default:
		logx.Infof("%s", msg)
	}
	if ctx != nil {
		ctx.Log(level, msg)
	}
}

// reqApplied records the rules that changed a request: info in the plugin
// logs, so every edit is visible per request, but only debug on stderr.
func reqApplied(ctx *schemas.BifrostContext, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	logx.Debugf("%s", msg)
	if ctx != nil {
		ctx.Log(schemas.LogLevelInfo, msg)
	}
}

// runHTTPStage stashes request metadata for later stages and applies the http
// stage rules to the JSON body. It modifies req.Body (and Content-Length) in
// place, which the transport writes back to the request.
func runHTTPStage(ctx *schemas.BifrostContext, st *state, req *schemas.HTTPRequest) {
	if req == nil {
		return
	}
	route := classifyPath(req.Path)
	info := &httpInfo{
		Path:         req.Path,
		FromProtocol: route.protocol,
		Headers:      lowerMap(req.Headers),
	}
	if ctx != nil {
		ctx.SetValue(httpInfoKey, info)
	}
	if !st.engine.HasStage(payload.StageHTTP) {
		return
	}
	if len(req.Body) == 0 {
		if bodyExpected(info.Headers) {
			reqLog(ctx, schemas.LogLevelInfo, "http stage skipped for %s %s: request body is not in memory (large payload mode)", req.Method, req.Path)
		}
		return
	}
	trimmed := bytes.TrimSpace(req.Body)
	if len(trimmed) == 0 || trimmed[0] != '{' || !gjson.ValidBytes(trimmed) {
		reqLog(ctx, schemas.LogLevelDebug, "http stage skipped for %s %s: body is not a JSON object", req.Method, req.Path)
		return
	}

	provider, model := httpProviderModel(req, route)
	mc := payload.MatchContext{
		Stage:        payload.StageHTTP,
		Provider:     provider,
		Model:        model,
		Path:         req.Path,
		FromProtocol: route.protocol,
		BodyFormat:   payload.BodyFormatRaw,
		Headers:      info.Headers,
		Params:       lowerMap(req.Query),
	}
	fillGovernance(ctx, &mc)

	res, err := st.engine.Apply(req.Body, mc)
	if err != nil {
		reqLog(ctx, schemas.LogLevelWarn, "http stage for %s: %v", req.Path, err)
		return
	}
	if !res.Changed {
		return
	}
	req.Body = res.Body
	// The transport re-applies every entry of req.Headers before setting the
	// body, so keep a Content-Length entry consistent with the new body.
	for k := range req.Headers {
		if strings.EqualFold(k, "content-length") {
			req.Headers[k] = strconv.Itoa(len(res.Body))
		}
	}
	reqApplied(ctx, "http stage for %s: applied %v", req.Path, res.Applied)
}

// bodyExpected reports whether the request headers announce a body.
func bodyExpected(lowerHeaders map[string]string) bool {
	if cl, err := strconv.ParseInt(strings.TrimSpace(lowerHeaders["content-length"]), 10, 64); err == nil && cl > 0 {
		return true
	}
	return lowerHeaders["transfer-encoding"] != ""
}

// httpProviderModel resolves the provider and model of an http stage request:
// a known provider prefix on the body model wins, then the integration route.
// The model comes from the body, else from the route's model path parameter.
func httpProviderModel(req *schemas.HTTPRequest, route routeInfo) (string, string) {
	model := gjson.GetBytes(req.Body, "model").String()
	if model == "" && route.modelParam != "" {
		model = req.PathParams[route.modelParam]
		if route.protocol == protocolGenAI {
			model = strings.TrimPrefix(model, "models/")
			if i := strings.IndexByte(model, ':'); i >= 0 {
				model = model[:i]
			}
		}
	}
	provider := route.provider
	if p, m := schemas.ParseModelString(model, ""); p != "" {
		provider, model = string(p), m
	}
	return provider, model
}

// runCoreStage applies the request or llm stage. It returns req itself when
// nothing changed, otherwise a shallow copy of req with a new inner request.
func runCoreStage(ctx *schemas.BifrostContext, st *state, req *schemas.BifrostRequest, stage string) *schemas.BifrostRequest {
	if !st.engine.HasStage(stage) {
		return req
	}
	mc := coreMatchContext(ctx, req, stage)

	if useRaw, _ := ctxValue(ctx, schemas.BifrostContextKeyUseRawRequestBody).(bool); useRaw {
		body := bifrostview.RawBody(req)
		if len(body) == 0 {
			reqLog(ctx, schemas.LogLevelDebug, "%s stage skipped: raw request body mode without a raw body", stage)
			return req
		}
		mc.BodyFormat = payload.BodyFormatRaw
		res, err := st.engine.Apply(body, mc)
		if err != nil {
			reqLog(ctx, schemas.LogLevelWarn, "%s stage (raw): %v", stage, err)
			return req
		}
		if !res.Changed {
			return req
		}
		out := bifrostview.WithRawBody(req, res.Body)
		if out == nil {
			reqLog(ctx, schemas.LogLevelWarn, "%s stage (raw): request type %s has no raw body", stage, req.RequestType)
			return req
		}
		reqApplied(ctx, "%s stage (raw): applied %v", stage, res.Applied)
		return out
	}

	mc.BodyFormat = payload.BodyFormatBifrost
	view, err := bifrostview.Encode(req)
	if err != nil {
		reqLog(ctx, schemas.LogLevelDebug, "%s stage skipped: %v", stage, err)
		return req
	}
	res, err := st.engine.Apply(view, mc)
	if err != nil {
		reqLog(ctx, schemas.LogLevelWarn, "%s stage: %v", stage, err)
		return req
	}
	if !res.Changed {
		return req
	}
	out, extraAdded, warnings, err := bifrostview.Decode(req, view, res.Body, stage)
	for _, w := range warnings {
		reqLog(ctx, schemas.LogLevelWarn, "%s stage, rules %v: %s", stage, res.Applied, w)
	}
	if err != nil {
		reqLog(ctx, schemas.LogLevelError, "%s stage, rules %v: cannot apply edits, request left unchanged: %v", stage, res.Applied, err)
		return req
	}
	if extraAdded && st.cfg.PassthroughExtraParams && ctx != nil {
		ctx.SetValue(schemas.BifrostContextKeyPassthroughExtraParams, true)
	}
	reqApplied(ctx, "%s stage: applied %v", stage, res.Applied)
	return out
}

// coreMatchContext builds the match context of the request and llm stages.
func coreMatchContext(ctx *schemas.BifrostContext, req *schemas.BifrostRequest, stage string) payload.MatchContext {
	provider, model, _ := req.GetRequestFields()
	mc := payload.MatchContext{
		Stage:       stage,
		Provider:    string(provider),
		Model:       model,
		RequestType: string(req.RequestType),
	}
	info, _ := ctxValue(ctx, httpInfoKey).(*httpInfo)
	if info != nil {
		mc.Path = info.Path
		mc.FromProtocol = info.FromProtocol
	}
	if h, ok := ctxValue(ctx, schemas.BifrostContextKeyRequestHeaders).(map[string]string); ok {
		mc.Headers = h
	} else if info != nil {
		mc.Headers = info.Headers
	}
	if q, ok := ctxValue(ctx, schemas.BifrostContextKeyRequestQuery).(map[string]string); ok {
		mc.Params = q
	}
	fillGovernance(ctx, &mc)
	return mc
}

// fillGovernance copies the governance identities from the context.
func fillGovernance(ctx *schemas.BifrostContext, mc *payload.MatchContext) {
	mc.VirtualKeyID = ctxString(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyID)
	mc.VirtualKeyName = ctxString(ctx, schemas.BifrostContextKeyGovernanceVirtualKeyName)
	mc.UserID = ctxString(ctx, schemas.BifrostContextKeyUserID)
	mc.TeamID = ctxString(ctx, schemas.BifrostContextKeyGovernanceTeamID)
	mc.TeamName = ctxString(ctx, schemas.BifrostContextKeyGovernanceTeamName)
	mc.CustomerID = ctxString(ctx, schemas.BifrostContextKeyGovernanceCustomerID)
	mc.CustomerName = ctxString(ctx, schemas.BifrostContextKeyGovernanceCustomerName)
}

func ctxValue(ctx *schemas.BifrostContext, key any) any {
	if ctx == nil {
		return nil
	}
	return ctx.Value(key)
}

func ctxString(ctx *schemas.BifrostContext, key any) string {
	s, _ := ctxValue(ctx, key).(string)
	return s
}

// lowerMap returns a copy of m with lower-cased keys. The copy matters: the
// transport's HTTPRequest maps are pooled and recycled after the hook.
func lowerMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

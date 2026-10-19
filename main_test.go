package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/koreapyj/bifrost-plugin-payload/payload"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// The loader (framework/plugins/soloader.go) type-asserts every symbol; a
// signature mismatch makes Bifrost refuse to load the plugin.
var (
	_ func(config any) error                                                                                                                                      = Init
	_ func() string                                                                                                                                               = GetName
	_ func() error                                                                                                                                                = Cleanup
	_ func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)                                                                  = HTTPTransportPreAuthHook
	_ func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error)                                                                  = HTTPTransportPreHook
	_ func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error                                                               = HTTPTransportPostHook
	_ func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error)                         = HTTPTransportStreamChunkHook
	_ func(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error                                                       = HTTPTransportResponseHeadersHook
	_ func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) error                                                                                        = PreRequestHook
	_ func(ctx *schemas.BifrostContext, req *schemas.BifrostRequest) (*schemas.BifrostRequest, *schemas.LLMPluginShortCircuit, error)                             = PreLLMHook
	_ func(ctx *schemas.BifrostContext, resp *schemas.BifrostResponse, bifrostErr *schemas.BifrostError) (*schemas.BifrostResponse, *schemas.BifrostError, error) = PostLLMHook
)

func newCtx() *schemas.BifrostContext {
	return schemas.NewBifrostContext(context.Background(), schemas.NoDeadline)
}

func ptr[T any](v T) *T { return &v }

// mustInit replaces the plugin state with cfg (a JSON document).
func mustInit(t *testing.T, cfg string) {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal([]byte(cfg), &m))
	require.NoError(t, Init(m))
	t.Cleanup(func() { current.Store(nil) })
}

func chatRequest() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: ptr("hi")},
			}},
			Params: &schemas.ChatParameters{
				Temperature: ptr(0.5),
				ExtraParams: map[string]any{"client_extra": 1.0},
			},
			Fallbacks: []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-x"}},
		},
	}
}

func TestGetName(t *testing.T) {
	assert.Equal(t, "payload", GetName())
}

func TestHooksWithoutState(t *testing.T) {
	current.Store(nil)
	ctx := newCtx()
	hreq := &schemas.HTTPRequest{Path: "/v1/chat/completions", Body: []byte(`{"model":"x"}`)}
	resp, err := HTTPTransportPreAuthHook(ctx, hreq)
	assert.Nil(t, resp)
	assert.NoError(t, err)
	resp, err = HTTPTransportPreHook(ctx, hreq)
	assert.Nil(t, resp)
	assert.NoError(t, err)
	req := chatRequest()
	assert.NoError(t, PreRequestHook(ctx, req))
	out, sc, err := PreLLMHook(ctx, req)
	assert.Same(t, req, out)
	assert.Nil(t, sc)
	assert.NoError(t, err)
	chunk := &schemas.BifrostStreamChunk{}
	gotChunk, err := HTTPTransportStreamChunkHook(ctx, hreq, chunk)
	assert.Same(t, chunk, gotChunk)
	assert.NoError(t, err)
	assert.NoError(t, HTTPTransportPostHook(ctx, hreq, &schemas.HTTPResponse{}))
	assert.NoError(t, HTTPTransportResponseHeadersHook(ctx, hreq, &schemas.HTTPResponseMetadata{}))
	r, e, err := PostLLMHook(ctx, nil, nil)
	assert.Nil(t, r)
	assert.Nil(t, e)
	assert.NoError(t, err)
}

func TestHTTPStageRewritesBody(t *testing.T) {
	mustInit(t, `{"rules":[{
		"name":"http-openai","stage":"http",
		"cel_expression":"provider == \"openai\" && model == \"gpt-4o\" && from_protocol == \"openai\" && body_format == \"raw\" && headers[\"x-tier\"] == \"pro\" && path == \"/v1/chat/completions\"",
		"actions":{"override":{"temperature":0.7},"default":{"metadata.source":"\"plugin\""},"filter":["drop_me"]}
	}]}`)
	ctx := newCtx()
	body := `{"model":"openai/gpt-4o","temperature":1,"drop_me":true}`
	hreq := &schemas.HTTPRequest{
		Method:  "POST",
		Path:    "/v1/chat/completions",
		Headers: map[string]string{"Content-Type": "application/json", "Content-Length": "55", "X-Tier": "pro"},
		Body:    []byte(body),
	}
	resp, err := HTTPTransportPreHook(ctx, hreq)
	require.NoError(t, err)
	assert.Nil(t, resp)
	assert.Equal(t, 0.7, gjson.GetBytes(hreq.Body, "temperature").Float())
	assert.Equal(t, "plugin", gjson.GetBytes(hreq.Body, "metadata.source").String())
	assert.False(t, gjson.GetBytes(hreq.Body, "drop_me").Exists())
	assert.Equal(t, "openai/gpt-4o", gjson.GetBytes(hreq.Body, "model").String())
	assert.Equal(t, len(hreq.Body), atoi(t, hreq.Headers["Content-Length"]))
	assert.Equal(t, "pro", hreq.Headers["X-Tier"], "original header keys are untouched")

	info, ok := ctx.Value(httpInfoKey).(*httpInfo)
	require.True(t, ok)
	assert.Equal(t, "/v1/chat/completions", info.Path)
	assert.Equal(t, "openai", info.FromProtocol)
	assert.Equal(t, "pro", info.Headers["x-tier"])
}

func atoi(t *testing.T, s string) int {
	t.Helper()
	var n int
	require.NoError(t, json.Unmarshal([]byte(s), &n))
	return n
}

func TestHTTPStageProviderFromRoute(t *testing.T) {
	mustInit(t, `{"rules":[
		{"name":"anthropic","stage":"http","cel_expression":"provider == \"anthropic\" && from_protocol == \"anthropic\"","actions":{"override":{"max_tokens":99}}},
		{"name":"gemini","stage":"http","priority":1,"cel_expression":"provider == \"gemini\" && model == \"gemini-2.5-pro\"","actions":{"override":{"generationConfig.temperature":0.1}}}
	]}`)
	hreq := &schemas.HTTPRequest{Path: "/anthropic/v1/messages", Body: []byte(`{"model":"claude-sonnet","max_tokens":1}`)}
	_, err := HTTPTransportPreHook(newCtx(), hreq)
	require.NoError(t, err)
	assert.Equal(t, int64(99), gjson.GetBytes(hreq.Body, "max_tokens").Int())

	hreq = &schemas.HTTPRequest{
		Path:       "/genai/v1beta/models/gemini-2.5-pro:generateContent",
		PathParams: map[string]string{"model": "gemini-2.5-pro:generateContent"},
		Body:       []byte(`{"contents":[]}`),
	}
	_, err = HTTPTransportPreHook(newCtx(), hreq)
	require.NoError(t, err)
	assert.Equal(t, 0.1, gjson.GetBytes(hreq.Body, "generationConfig.temperature").Float())

	// No provider prefix and no integration route: provider is empty.
	hreq = &schemas.HTTPRequest{Path: "/v1/messages", Body: []byte(`{"model":"claude-sonnet","max_tokens":1}`)}
	_, err = HTTPTransportPreHook(newCtx(), hreq)
	require.NoError(t, err)
	assert.Equal(t, int64(1), gjson.GetBytes(hreq.Body, "max_tokens").Int())
}

func TestHTTPStageSkipsLargePayloadAndNonJSON(t *testing.T) {
	mustInit(t, `{"rules":[{"name":"r","stage":"http","actions":{"override":{"x":1}}}]}`)
	hreq := &schemas.HTTPRequest{Path: "/v1/chat/completions", Headers: map[string]string{"Content-Length": "999999999"}}
	_, err := HTTPTransportPreHook(newCtx(), hreq)
	require.NoError(t, err)
	assert.Nil(t, hreq.Body)

	hreq = &schemas.HTTPRequest{Path: "/v1/audio/transcriptions", Body: []byte("--boundary\r\n")}
	_, err = HTTPTransportPreHook(newCtx(), hreq)
	require.NoError(t, err)
	assert.Equal(t, "--boundary\r\n", string(hreq.Body))
}

func TestClassifyPath(t *testing.T) {
	cases := []struct {
		path     string
		protocol string
		provider string
		param    string
	}{
		{"/v1/chat/completions", "openai", "", ""},
		{"/v1/responses", "openai", "", ""},
		{"/openai/v1/chat/completions", "openai", "openai", ""},
		{"/anthropic/v1/messages", "anthropic", "anthropic", ""},
		{"/genai/v1beta/models/gemini:generateContent", "genai", "gemini", "model"},
		{"/bedrock/model/anthropic.claude/converse", "bedrock", "bedrock", "modelId"},
		{"/cohere/v2/chat", "cohere", "cohere", ""},
		{"/litellm/v1/chat/completions", "openai", "", ""},
		{"/litellm/v1/messages", "anthropic", "anthropic", ""},
		{"/langchain/v1beta/models/gemini:generateContent", "genai", "gemini", "model"},
		{"/pydanticai/model/x/converse", "bedrock", "bedrock", "modelId"},
		{"/cursor/v2/chat", "cohere", "cohere", ""},
		{"/cursor/anthropic/v1/messages", "anthropic", "anthropic", ""},
		{"/anthropic_passthrough/v1/messages", "anthropic", "anthropic", ""},
		{"/azure_passthrough/openai/deployments/x", "openai", "azure", ""},
		{"/", "openai", "", ""},
	}
	for _, c := range cases {
		ri := classifyPath(c.path)
		assert.Equal(t, routeInfo{protocol: c.protocol, provider: c.provider, modelParam: c.param}, ri, c.path)
	}
}

func TestRequestStageBifrostView(t *testing.T) {
	mustInit(t, `{"rules":[{"name":"req","stage":"request",
		"cel_expression":"body_format == \"bifrost\" && request_type == \"chat_completion\" && path == \"/v1/chat/completions\" && headers[\"x-team\"] == \"a\"",
		"actions":{"override":{"params.temperature":0.2,"model":"\"gpt-4.1\""}}}]}`)
	ctx := newCtx()
	ctx.SetValue(httpInfoKey, &httpInfo{Path: "/v1/chat/completions", FromProtocol: "openai", Headers: map[string]string{"x-team": "stale"}})
	ctx.SetValue(schemas.BifrostContextKeyRequestHeaders, map[string]string{"x-team": "a"})

	req := chatRequest()
	oldInner, oldParams := req.ChatRequest, req.ChatRequest.Params
	require.NoError(t, PreRequestHook(ctx, req))
	assert.NotSame(t, oldInner, req.ChatRequest)
	assert.Equal(t, "gpt-4.1", req.ChatRequest.Model)
	assert.Equal(t, 0.2, *req.ChatRequest.Params.Temperature)
	// The previous inner request is untouched.
	assert.Equal(t, "gpt-4o", oldInner.Model)
	assert.Same(t, oldParams, oldInner.Params)
	assert.Equal(t, 0.5, *oldParams.Temperature)
}

func TestRequestStageRaw(t *testing.T) {
	mustInit(t, `{"rules":[{"name":"raw","stage":"request","cel_expression":"body_format == \"raw\"","actions":{"override":{"max_tokens":42}}}]}`)
	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	req := chatRequest()
	raw := []byte(`{"model":"claude","max_tokens":1}`)
	req.ChatRequest.RawRequestBody = raw
	oldInner := req.ChatRequest
	require.NoError(t, PreRequestHook(ctx, req))
	assert.Equal(t, int64(42), gjson.GetBytes(req.ChatRequest.RawRequestBody, "max_tokens").Int())
	assert.Equal(t, `{"model":"claude","max_tokens":1}`, string(raw))
	assert.Same(t, oldInner.Params, req.ChatRequest.Params)
	assert.Equal(t, `{"model":"claude","max_tokens":1}`, string(oldInner.RawRequestBody))
}

func TestLLMStageRawAndBifrost(t *testing.T) {
	mustInit(t, `{"rules":[
		{"name":"raw","stage":"llm","cel_expression":"body_format == \"raw\" && provider == \"anthropic\"","actions":{"override":{"thinking.type":"\"enabled\""}}},
		{"name":"view","stage":"llm","priority":1,"cel_expression":"body_format == \"bifrost\" && provider == \"openai\"","actions":{"override":{"params.max_completion_tokens":10}}}
	]}`)
	// Raw.
	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyUseRawRequestBody, true)
	req := chatRequest()
	req.ChatRequest.Provider = schemas.Anthropic
	req.ChatRequest.RawRequestBody = []byte(`{"model":"claude"}`)
	out, sc, err := PreLLMHook(ctx, req)
	require.NoError(t, err)
	assert.Nil(t, sc)
	assert.Equal(t, "enabled", gjson.GetBytes(out.ChatRequest.RawRequestBody, "thinking.type").String())
	assert.Equal(t, `{"model":"claude"}`, string(req.ChatRequest.RawRequestBody))

	// Bifrost view.
	ctx = newCtx()
	req = chatRequest()
	out, _, err = PreLLMHook(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, 10, *out.ChatRequest.Params.MaxCompletionTokens)
	assert.Nil(t, req.ChatRequest.Params.MaxCompletionTokens)
	assert.Nil(t, ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams), "no new extra params")

	// No match: the very same request is returned.
	req.ChatRequest.Provider = schemas.Gemini
	out, _, err = PreLLMHook(newCtx(), req)
	require.NoError(t, err)
	assert.Same(t, req, out)
}

func TestPassthroughExtraParamsFlag(t *testing.T) {
	const rules = `"rules":[{"name":"extra","stage":"llm","actions":{"override":{"params.max_tokens":64}}}]`
	for _, passthrough := range []bool{true, false} {
		mustInit(t, `{`+rules+`,"passthrough_extra_params":`+map[bool]string{true: "true", false: "false"}[passthrough]+`}`)
		ctx := newCtx()
		out, _, err := PreLLMHook(ctx, chatRequest())
		require.NoError(t, err)
		assert.Equal(t, float64(64), out.ChatRequest.Params.ExtraParams["max_tokens"])
		if passthrough {
			assert.Equal(t, true, ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams))
		} else {
			assert.Nil(t, ctx.Value(schemas.BifrostContextKeyPassthroughExtraParams))
		}
	}
}

func TestPreLLMHookCopyOnWriteWithFallback(t *testing.T) {
	mustInit(t, `{"rules":[{"name":"openai-only","stage":"llm","cel_expression":"provider == \"openai\"",
		"actions":{"override":{"params.temperature":1.9,"params.client_extra":2,"params.added":"\"x\""}}}]}`)
	primary := chatRequest()
	// What core's prepareFallbackRequest builds: a shallow inner copy sharing Params.
	inner := *primary.ChatRequest
	inner.Provider, inner.Model = schemas.Anthropic, "claude-x"
	fallback := *primary
	fallback.ChatRequest = &inner
	require.Same(t, primary.ChatRequest.Params, fallback.ChatRequest.Params)
	paramsBefore := *primary.ChatRequest.Params
	extraBefore := map[string]any{"client_extra": 1.0}

	ctx := newCtx()
	out, _, err := PreLLMHook(ctx, primary)
	require.NoError(t, err)
	assert.Equal(t, 1.9, *out.ChatRequest.Params.Temperature)
	assert.Equal(t, map[string]any{"client_extra": 2.0, "added": "x"}, out.ChatRequest.Params.ExtraParams)

	// Neither the primary request nor the fallback attempt sharing its Params changed.
	assert.Equal(t, 0.5, *primary.ChatRequest.Params.Temperature)
	assert.Equal(t, extraBefore, primary.ChatRequest.Params.ExtraParams)
	assert.True(t, reflect.DeepEqual(paramsBefore, *primary.ChatRequest.Params))

	fbOut, _, err := PreLLMHook(ctx, &fallback)
	require.NoError(t, err)
	assert.Same(t, &fallback, fbOut, "rule does not match the anthropic fallback")
	assert.Equal(t, 0.5, *fbOut.ChatRequest.Params.Temperature)
	assert.Equal(t, extraBefore, fbOut.ChatRequest.Params.ExtraParams)
}

func TestInitFailureKeepsRulesAndReinitSwaps(t *testing.T) {
	mustInit(t, `{"rules":[{"name":"v1","stage":"llm","actions":{"override":{"params.temperature":0.1}}}]}`)
	apply := func() float64 {
		out, _, err := PreLLMHook(newCtx(), chatRequest())
		require.NoError(t, err)
		return *out.ChatRequest.Params.Temperature
	}
	assert.Equal(t, 0.1, apply())

	// Invalid CEL is rejected and the previous rules stay active.
	err := Init(map[string]any{"rules": []any{map[string]any{
		"name": "bad", "stage": "llm", "cel_expression": "provider ==", "actions": map[string]any{"override": map[string]any{"params.temperature": 0.9}},
	}}})
	require.Error(t, err)
	assert.Equal(t, 0.1, apply())
	// Invalid JSON values are rejected as well.
	require.Error(t, Init(`{"rules":[{"name":"bad","stage":"llm","actions":{"override":{"a":"{not json"}}}]}`))
	assert.Equal(t, 0.1, apply())

	// Re-Init swaps the rules; the Cleanup Bifrost runs on the replaced
	// instance afterwards must not disable them.
	require.NoError(t, Init(`{"rules":[{"name":"v2","stage":"llm","actions":{"override":{"params.temperature":0.3}}}]}`))
	require.NoError(t, Cleanup())
	require.NoError(t, Cleanup())
	assert.Equal(t, 0.3, apply())

	// An empty configuration disables everything.
	require.NoError(t, Init(nil))
	assert.Equal(t, 0.5, apply())
	assert.False(t, loadState().engine.HasStage(payload.StageLLM))
}

func TestGovernanceAndQueryInMatchContext(t *testing.T) {
	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyName, "vk-name")
	ctx.SetValue(schemas.BifrostContextKeyUserID, "u1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamID, "t1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceTeamName, "team")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerID, "c1")
	ctx.SetValue(schemas.BifrostContextKeyGovernanceCustomerName, "cust")
	ctx.SetValue(schemas.BifrostContextKeyRequestQuery, map[string]string{"q": "1"})
	ctx.SetValue(httpInfoKey, &httpInfo{Path: "/anthropic/v1/messages", FromProtocol: "anthropic", Headers: map[string]string{"x-a": "b"}})
	mc := coreMatchContext(ctx, chatRequest(), payload.StageLLM)
	assert.Equal(t, payload.MatchContext{
		Stage: "llm", Provider: "openai", Model: "gpt-4o", RequestType: "chat_completion",
		Path: "/anthropic/v1/messages", FromProtocol: "anthropic",
		Headers: map[string]string{"x-a": "b"}, Params: map[string]string{"q": "1"},
		VirtualKeyID: "vk1", VirtualKeyName: "vk-name", UserID: "u1",
		TeamID: "t1", TeamName: "team", CustomerID: "c1", CustomerName: "cust",
	}, mc)
}

func TestVirtualKeyScope(t *testing.T) {
	mustInit(t, `{"rules":[
		{"name":"vk","stage":"llm","scope":"virtual_key","scope_id":"vk1","actions":{"override":{"params.temperature":0.11}}},
		{"name":"global","stage":"llm","actions":{"override":{"params.temperature":0.22}}}
	]}`)
	ctx := newCtx()
	ctx.SetValue(schemas.BifrostContextKeyGovernanceVirtualKeyID, "vk1")
	out, _, err := PreLLMHook(ctx, chatRequest())
	require.NoError(t, err)
	assert.Equal(t, 0.11, *out.ChatRequest.Params.Temperature)
	out, _, err = PreLLMHook(newCtx(), chatRequest())
	require.NoError(t, err)
	assert.Equal(t, 0.22, *out.ChatRequest.Params.Temperature)
}

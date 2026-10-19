package bifrostview

import (
	"reflect"
	"testing"
	"unsafe"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// deepCopy returns a deep copy of v, including unexported fields, so tests can
// prove that Decode never writes through the original request.
func deepCopy[T any](v T) T {
	src := reflect.ValueOf(&v).Elem()
	dst := reflect.New(src.Type()).Elem()
	copyValue(dst, src)
	return dst.Interface().(T)
}

func settable(v reflect.Value) reflect.Value {
	if v.CanSet() {
		return v
	}
	return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem()
}

func copyValue(dst, src reflect.Value) {
	dst = settable(dst)
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			return
		}
		n := reflect.New(src.Type().Elem())
		copyValue(n.Elem(), src.Elem())
		dst.Set(n)
	case reflect.Struct:
		for i := 0; i < src.NumField(); i++ {
			copyValue(dst.Field(i), src.Field(i))
		}
	case reflect.Slice:
		if src.IsNil() {
			return
		}
		n := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := 0; i < src.Len(); i++ {
			copyValue(n.Index(i), src.Index(i))
		}
		dst.Set(n)
	case reflect.Map:
		if src.IsNil() {
			return
		}
		n := reflect.MakeMapWithSize(src.Type(), src.Len())
		iter := src.MapRange()
		for iter.Next() {
			val := reflect.New(iter.Value().Type()).Elem()
			copyValue(val, iter.Value())
			n.SetMapIndex(iter.Key(), val)
		}
		dst.Set(n)
	case reflect.Interface:
		if src.IsNil() {
			return
		}
		val := reflect.New(src.Elem().Type()).Elem()
		copyValue(val, src.Elem())
		dst.Set(val)
	default:
		dst.Set(src)
	}
}

func ptr[T any](v T) *T { return &v }

func chatReq() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ChatCompletionRequest,
		ChatRequest: &schemas.BifrostChatRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-4o",
			Input: []schemas.ChatMessage{{
				Role:    schemas.ChatMessageRoleUser,
				Content: &schemas.ChatMessageContent{ContentStr: ptr("hello")},
			}},
			Params: &schemas.ChatParameters{
				Temperature: ptr(0.5),
				Tools: []schemas.ChatTool{{
					Type: schemas.ChatToolTypeFunction,
					Function: &schemas.ChatToolFunction{
						Name: "lookup",
					},
					Annotations: &schemas.MCPToolAnnotations{Title: "Lookup tool"},
				}},
				ExtraParams: map[string]any{"existing_extra": "keep"},
			},
			Fallbacks:      []schemas.Fallback{{Provider: schemas.Anthropic, Model: "claude-x"}},
			RawRequestBody: []byte(`{"raw":true}`),
		},
	}
}

func responsesReq() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.ResponsesRequest,
		ResponsesRequest: &schemas.BifrostResponsesRequest{
			Provider: schemas.OpenAI,
			Model:    "gpt-5",
			Input: []schemas.ResponsesMessage{{
				Role:    ptr(schemas.ResponsesInputMessageRoleUser),
				Content: &schemas.ResponsesMessageContent{ContentStr: ptr("hi")},
			}},
			Params: &schemas.ResponsesParameters{
				MaxOutputTokens: ptr(100),
				Reasoning:       &schemas.ResponsesParametersReasoning{Effort: ptr("low")},
			},
		},
	}
}

func embeddingReq() *schemas.BifrostRequest {
	return &schemas.BifrostRequest{
		RequestType: schemas.EmbeddingRequest,
		EmbeddingRequest: &schemas.BifrostEmbeddingRequest{
			Provider: schemas.OpenAI,
			Model:    "text-embedding-3-small",
			Params:   &schemas.EmbeddingParameters{Dimensions: ptr(256)},
		},
	}
}

func set(t *testing.T, doc []byte, path string, value any) []byte {
	t.Helper()
	out, err := sjson.SetBytes(doc, path, value)
	require.NoError(t, err)
	return out
}

func setRaw(t *testing.T, doc []byte, path, raw string) []byte {
	t.Helper()
	out, err := sjson.SetRawBytes(doc, path, []byte(raw))
	require.NoError(t, err)
	return out
}

func TestEncodeFlattensExtraParams(t *testing.T) {
	req := chatReq()
	req.ChatRequest.Params.ExtraParams["temperature"] = 9.0 // shadowed by the real field
	view, err := Encode(req)
	require.NoError(t, err)
	assert.Equal(t, "keep", gjson.GetBytes(view, "params.existing_extra").String())
	assert.Equal(t, 0.5, gjson.GetBytes(view, "params.temperature").Float())
	assert.Equal(t, "gpt-4o", gjson.GetBytes(view, "model").String())
	assert.False(t, gjson.GetBytes(view, "RawRequestBody").Exists())
}

func TestEncodeNoInner(t *testing.T) {
	_, err := Encode(&schemas.BifrostRequest{})
	assert.ErrorIs(t, err, ErrNoInnerRequest)
}

func TestRoundtripNoop(t *testing.T) {
	for name, mk := range map[string]func() *schemas.BifrostRequest{
		"chat": chatReq, "responses": responsesReq, "embedding": embeddingReq,
	} {
		t.Run(name, func(t *testing.T) {
			req := mk()
			view, err := Encode(req)
			require.NoError(t, err)
			out, added, warns, err := Decode(req, view, append([]byte(nil), view...), StageLLM)
			require.NoError(t, err)
			assert.Same(t, req, out)
			assert.False(t, added)
			assert.Empty(t, warns)

			// Whitespace-only differences are not edits.
			pretty := []byte(gjson.ParseBytes(view).Get("@pretty").Raw)
			out, _, warns, err = Decode(req, view, pretty, StageLLM)
			require.NoError(t, err)
			assert.Same(t, req, out)
			assert.Empty(t, warns)
		})
	}
}

func TestChatParamEditCopyOnWrite(t *testing.T) {
	req := chatReq()
	snapshot := deepCopy(req)
	oldInner, oldParams := req.ChatRequest, req.ChatRequest.Params

	view, err := Encode(req)
	require.NoError(t, err)
	after := set(t, view, "params.temperature", 0.9)
	out, added, warns, err := Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.Empty(t, warns)
	assert.False(t, added, "existing extras unchanged")

	require.NotSame(t, req, out)
	assert.NotSame(t, oldInner, out.ChatRequest)
	assert.NotSame(t, oldParams, out.ChatRequest.Params)
	assert.Equal(t, 0.9, *out.ChatRequest.Params.Temperature)
	assert.Equal(t, map[string]any{"existing_extra": "keep"}, out.ChatRequest.Params.ExtraParams)
	// Untouched parts are carried over.
	assert.Equal(t, req.ChatRequest.Input, out.ChatRequest.Input)
	assert.Equal(t, []byte(`{"raw":true}`), out.ChatRequest.RawRequestBody)
	assert.Equal(t, schemas.ChatCompletionRequest, out.RequestType)

	// The original request is untouched.
	assert.Same(t, oldInner, req.ChatRequest)
	assert.True(t, reflect.DeepEqual(snapshot, req), "original request was modified")
}

func TestUnknownParamGoesToExtraParams(t *testing.T) {
	req := chatReq()
	snapshot := deepCopy(req)
	view, err := Encode(req)
	require.NoError(t, err)
	after := setRaw(t, view, "params.custom_knob", `{"a":[1,2]}`)
	out, added, _, err := Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.True(t, added)
	assert.Equal(t, map[string]any{"a": []any{float64(1), float64(2)}}, out.ChatRequest.Params.ExtraParams["custom_knob"])
	assert.Equal(t, "keep", out.ChatRequest.Params.ExtraParams["existing_extra"])
	assert.True(t, reflect.DeepEqual(snapshot, req))
}

func TestChatMaxTokensIsUnknown(t *testing.T) {
	// ChatParameters has no max_tokens field: rules must target
	// params.max_completion_tokens instead.
	req := chatReq()
	view, err := Encode(req)
	require.NoError(t, err)

	out, added, _, err := Decode(req, view, set(t, view, "params.max_tokens", 64), StageLLM)
	require.NoError(t, err)
	assert.True(t, added)
	assert.Nil(t, out.ChatRequest.Params.MaxCompletionTokens)
	assert.Equal(t, float64(64), out.ChatRequest.Params.ExtraParams["max_tokens"])

	out, added, _, err = Decode(req, view, set(t, view, "params.max_completion_tokens", 64), StageLLM)
	require.NoError(t, err)
	assert.False(t, added)
	assert.Equal(t, 64, *out.ChatRequest.Params.MaxCompletionTokens)
	assert.NotContains(t, out.ChatRequest.Params.ExtraParams, "max_completion_tokens")
}

func TestAliasIsNotUnknown(t *testing.T) {
	req := chatReq()
	view, err := Encode(req)
	require.NoError(t, err)
	out, added, _, err := Decode(req, view, set(t, view, "params.reasoning_effort", "high"), StageLLM)
	require.NoError(t, err)
	assert.False(t, added)
	require.NotNil(t, out.ChatRequest.Params.Reasoning)
	assert.Equal(t, "high", *out.ChatRequest.Params.Reasoning.Effort)
	assert.NotContains(t, out.ChatRequest.Params.ExtraParams, "reasoning_effort")
	assert.Nil(t, req.ChatRequest.Params.Reasoning)
}

func TestNestedIgnoredFieldsPreserved(t *testing.T) {
	req := chatReq()
	view, err := Encode(req)
	require.NoError(t, err)
	out, _, _, err := Decode(req, view, set(t, view, "params.temperature", 0.1), StageLLM)
	require.NoError(t, err)
	require.Len(t, out.ChatRequest.Params.Tools, 1)
	require.NotNil(t, out.ChatRequest.Params.Tools[0].Annotations, "json:\"-\" data of untouched fields must survive")
	assert.Equal(t, "Lookup tool", out.ChatRequest.Params.Tools[0].Annotations.Title)
}

func TestShadowedExtraCarriedOver(t *testing.T) {
	req := chatReq()
	req.ChatRequest.Params.ExtraParams["temperature"] = 9.0
	view, err := Encode(req)
	require.NoError(t, err)
	out, _, _, err := Decode(req, view, set(t, view, "params.top_p", 0.2), StageLLM)
	require.NoError(t, err)
	assert.Equal(t, 9.0, out.ChatRequest.Params.ExtraParams["temperature"])
	assert.Equal(t, 0.5, *out.ChatRequest.Params.Temperature)
}

func TestRemoveExtraAndNullParams(t *testing.T) {
	req := chatReq()
	view, err := Encode(req)
	require.NoError(t, err)
	after, err := sjson.DeleteBytes(view, "params.existing_extra")
	require.NoError(t, err)
	out, added, _, err := Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.False(t, added)
	assert.Nil(t, out.ChatRequest.Params.ExtraParams)
	assert.Equal(t, "keep", req.ChatRequest.Params.ExtraParams["existing_extra"])

	out, _, _, err = Decode(req, view, setRaw(t, view, "params", "null"), StageLLM)
	require.NoError(t, err)
	assert.Nil(t, out.ChatRequest.Params)
	assert.NotNil(t, req.ChatRequest.Params)
}

func TestDisallowedKeys(t *testing.T) {
	req := chatReq()
	snapshot := deepCopy(req)
	view, err := Encode(req)
	require.NoError(t, err)

	// input is not editable.
	after := set(t, view, "input.0.content", "changed")
	out, _, warns, err := Decode(req, view, after, StageRequest)
	require.NoError(t, err)
	assert.Same(t, req, out)
	require.Len(t, warns, 1)
	assert.Contains(t, warns[0], `"input"`)

	// provider is ignored at the llm stage, fallbacks dropped.
	after = set(t, view, "provider", "anthropic")
	after = setRaw(t, after, "fallbacks", `[]`)
	out, _, warns, err = Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.Same(t, req, out)
	assert.Len(t, warns, 2)

	// Mixed: the model edit applies, the input edit is dropped.
	after = set(t, view, "model", "gpt-4.1")
	after = set(t, after, "input.0.role", "system")
	out, _, warns, err = Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.Len(t, warns, 1)
	assert.Equal(t, "gpt-4.1", out.ChatRequest.Model)
	assert.Equal(t, schemas.ChatMessageRoleUser, out.ChatRequest.Input[0].Role)
	assert.True(t, reflect.DeepEqual(snapshot, req))
}

func TestRequestStageProviderAndFallbacks(t *testing.T) {
	req := chatReq()
	snapshot := deepCopy(req)
	view, err := Encode(req)
	require.NoError(t, err)
	after := set(t, view, "provider", "anthropic")
	after = setRaw(t, after, "fallbacks", `[{"provider":"gemini","model":"gemini-2.5-pro"}]`)
	out, _, warns, err := Decode(req, view, after, StageRequest)
	require.NoError(t, err)
	assert.Empty(t, warns)
	assert.Equal(t, schemas.Anthropic, out.ChatRequest.Provider)
	assert.Equal(t, []schemas.Fallback{{Provider: schemas.Gemini, Model: "gemini-2.5-pro"}}, out.ChatRequest.Fallbacks)
	assert.Same(t, req.ChatRequest.Params, out.ChatRequest.Params, "params untouched, so shared")
	assert.True(t, reflect.DeepEqual(snapshot, req))
}

func TestFallbackSharingIsolation(t *testing.T) {
	primary := chatReq()
	// Simulate core's prepareFallbackRequest: shallow copy of the inner request
	// that shares Params (and ExtraParams) with the primary attempt.
	inner := *primary.ChatRequest
	inner.Provider, inner.Model = schemas.Anthropic, "claude-x"
	fallback := *primary
	fallback.ChatRequest = &inner
	require.Same(t, primary.ChatRequest.Params, fallback.ChatRequest.Params)
	fallbackSnapshot := deepCopy(&fallback)

	view, err := Encode(primary)
	require.NoError(t, err)
	after := set(t, view, "params.temperature", 1.5)
	after = set(t, after, "params.new_extra", true)
	after = set(t, after, "params.existing_extra", "changed")
	out, added, _, err := Decode(primary, view, after, StageLLM)
	require.NoError(t, err)
	assert.True(t, added)
	assert.Equal(t, 1.5, *out.ChatRequest.Params.Temperature)
	assert.Equal(t, "changed", out.ChatRequest.Params.ExtraParams["existing_extra"])

	assert.True(t, reflect.DeepEqual(fallbackSnapshot, &fallback), "fallback attempt was affected")
	assert.Equal(t, 0.5, *fallback.ChatRequest.Params.Temperature)
	assert.Equal(t, map[string]any{"existing_extra": "keep"}, fallback.ChatRequest.Params.ExtraParams)
}

func TestResponsesEdit(t *testing.T) {
	req := responsesReq()
	snapshot := deepCopy(req)
	view, err := Encode(req)
	require.NoError(t, err)
	after := set(t, view, "params.max_output_tokens", 4096)
	after = set(t, after, "params.reasoning.effort", "high")
	after = set(t, after, "params.unknown_flag", "x")
	out, added, warns, err := Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.Empty(t, warns)
	assert.True(t, added)
	assert.Equal(t, 4096, *out.ResponsesRequest.Params.MaxOutputTokens)
	assert.Equal(t, "high", *out.ResponsesRequest.Params.Reasoning.Effort)
	assert.Equal(t, map[string]any{"unknown_flag": "x"}, out.ResponsesRequest.Params.ExtraParams)
	assert.Equal(t, req.ResponsesRequest.Input, out.ResponsesRequest.Input)
	assert.True(t, reflect.DeepEqual(snapshot, req))
}

func TestCountTokensSelectsActiveField(t *testing.T) {
	r := responsesReq()
	req := &schemas.BifrostRequest{RequestType: schemas.CountTokensRequest, CountTokensRequest: r.ResponsesRequest}
	view, err := Encode(req)
	require.NoError(t, err)
	out, _, _, err := Decode(req, view, set(t, view, "model", "gpt-5-mini"), StageLLM)
	require.NoError(t, err)
	assert.Nil(t, out.ResponsesRequest)
	assert.Equal(t, "gpt-5-mini", out.CountTokensRequest.Model)
	assert.Equal(t, "gpt-5", req.CountTokensRequest.Model)
}

func TestEmbeddingEdit(t *testing.T) {
	req := embeddingReq()
	snapshot := deepCopy(req)
	view, err := Encode(req)
	require.NoError(t, err)
	after := set(t, view, "params.dimensions", 1024)
	after = set(t, after, "params.encoding_format", "base64")
	out, added, _, err := Decode(req, view, after, StageLLM)
	require.NoError(t, err)
	assert.False(t, added)
	assert.Equal(t, 1024, *out.EmbeddingRequest.Params.Dimensions)
	assert.Equal(t, "base64", *out.EmbeddingRequest.Params.EncodingFormat)
	assert.True(t, reflect.DeepEqual(snapshot, req))
}

func TestParamsAddedWhenNil(t *testing.T) {
	req := embeddingReq()
	req.EmbeddingRequest.Params = nil
	view, err := Encode(req)
	require.NoError(t, err)
	out, added, _, err := Decode(req, view, set(t, view, "params.dimensions", 8), StageLLM)
	require.NoError(t, err)
	assert.False(t, added)
	assert.Equal(t, 8, *out.EmbeddingRequest.Params.Dimensions)
	assert.Nil(t, req.EmbeddingRequest.Params)
}

func TestInvalidEdits(t *testing.T) {
	req := chatReq()
	view, err := Encode(req)
	require.NoError(t, err)
	_, _, _, err = Decode(req, view, setRaw(t, view, "params", `"oops"`), StageLLM)
	assert.Error(t, err)
	_, _, _, err = Decode(req, view, []byte(`[1]`), StageLLM)
	assert.Error(t, err)
	_, _, _, err = Decode(req, view, []byte(`{`), StageLLM)
	assert.Error(t, err)
	out, _, warns, err := Decode(req, view, set(t, view, "model", 5), StageLLM)
	require.NoError(t, err)
	assert.Same(t, req, out)
	assert.Len(t, warns, 1)
}

func TestRawBodyHelpers(t *testing.T) {
	req := chatReq()
	snapshot := deepCopy(req)
	assert.Equal(t, []byte(`{"raw":true}`), RawBody(req))
	body := []byte(`{"raw":false}`)
	out := WithRawBody(req, body)
	require.NotNil(t, out)
	body[2] = 'X' // the new request owns its copy
	assert.Equal(t, `{"raw":false}`, string(out.ChatRequest.RawRequestBody))
	assert.NotSame(t, req.ChatRequest, out.ChatRequest)
	assert.Same(t, req.ChatRequest.Params, out.ChatRequest.Params)
	assert.True(t, reflect.DeepEqual(snapshot, req))

	assert.Nil(t, RawBody(&schemas.BifrostRequest{}))
	assert.Nil(t, WithRawBody(&schemas.BifrostRequest{}, body))
}

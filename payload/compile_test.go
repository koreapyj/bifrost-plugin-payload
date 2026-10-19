package payload

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func compileJSON(t *testing.T, raw string) (*Engine, error) {
	t.Helper()
	cfg, err := ParseConfig([]byte(raw))
	require.NoError(t, err)
	return Compile(cfg)
}

func mustEngine(t *testing.T, raw string) *Engine {
	t.Helper()
	e, err := compileJSON(t, raw)
	require.NoError(t, err)
	return e
}

func TestCompileValidation(t *testing.T) {
	tests := []struct {
		name  string
		raw   string
		paths []string // expected FieldError paths, in order
	}{
		{name: "valid minimal", raw: `{"rules":[{"name":"a","stage":"http"}]}`},
		{name: "valid scoped", raw: `{"rules":[{"name":"a","stage":"llm","scope":"virtual_key","scope_id":"vk1"}]}`},
		{name: "global with empty scope_id", raw: `{"rules":[{"name":"a","stage":"http","scope":"global","scope_id":""}]}`},
		{name: "missing name", raw: `{"rules":[{"stage":"http"}]}`, paths: []string{"rules[0].name"}},
		{name: "bad stage", raw: `{"rules":[{"name":"a","stage":"pre"}]}`, paths: []string{"rules[0].stage"}},
		{name: "bad scope", raw: `{"rules":[{"name":"a","stage":"http","scope":"org","scope_id":"x"}]}`, paths: []string{"rules[0].scope"}},
		{name: "scope_id required", raw: `{"rules":[{"name":"a","stage":"http","scope":"team"}]}`, paths: []string{"rules[0].scope_id"}},
		{name: "scope_id blank", raw: `{"rules":[{"name":"a","stage":"http","scope":"user","scope_id":"  "}]}`, paths: []string{"rules[0].scope_id"}},
		{name: "scope_id padded", raw: `{"rules":[{"name":"a","stage":"http","scope":"team","scope_id":" t1 "}]}`, paths: []string{"rules[0].scope_id"}},
		{name: "scope_id on global", raw: `{"rules":[{"name":"a","stage":"http","scope_id":"x"}]}`, paths: []string{"rules[0].scope_id"}},
		{name: "duplicate priority same scope and stage", raw: `{"rules":[
			{"name":"a","stage":"http","priority":1},
			{"name":"b","stage":"http","priority":2},
			{"name":"c","stage":"http","priority":1,"enabled":false}]}`, paths: []string{"rules[2].priority"}},
		{name: "same priority different stage/scope ok", raw: `{"rules":[
			{"name":"a","stage":"http","priority":1},
			{"name":"b","stage":"llm","priority":1},
			{"name":"c","stage":"http","priority":1,"scope":"team","scope_id":"t1"},
			{"name":"d","stage":"http","priority":1,"scope":"team","scope_id":"t2"}]}`},
		{name: "cel syntax error", raw: `{"rules":[{"name":"a","stage":"http","cel_expression":"model ==="}]}`, paths: []string{"rules[0].cel_expression"}},
		{name: "cel unknown variable", raw: `{"rules":[{"name":"a","stage":"http","cel_expression":"nope == 1"}]}`, paths: []string{"rules[0].cel_expression"}},
		{name: "cel non-bool", raw: `{"rules":[{"name":"a","stage":"http","cel_expression":"model"}]}`, paths: []string{"rules[0].cel_expression"}},
		{name: "cel dyn accepted", raw: `{"rules":[{"name":"a","stage":"http","cel_expression":"body.stream"}]}`},
		{name: "invalid raw json string", raw: `{"rules":[{"name":"a","stage":"http","actions":{"default":{"a.b":"high"}}}]}`, paths: []string{`rules[0].actions.default["a.b"]`}},
		{name: "valid raw values", raw: `{"rules":[{"name":"a","stage":"http","actions":{"default":{"a":"\"high\"","b":1,"c":{"x":[1]},"d":null,"e":true},"override":{"f":"{\"type\":\"json_object\"}"}}}]}`},
		{name: "empty action path", raw: `{"rules":[{"name":"a","stage":"http","actions":{"override":{" ":"1"},"filter":["ok",""]}}]}`, paths: []string{`rules[0].actions.override[" "]`, "rules[0].actions.filter[1]"}},
		{name: "malformed query path", raw: `{"rules":[{"name":"a","stage":"http","actions":{"filter":["tools.#(type==\"x\"","a..b","tools.#()#"]}}]}`, paths: []string{"rules[0].actions.filter[0]", "rules[0].actions.filter[1]", "rules[0].actions.filter[2]"}},
		{name: "disabled rules are validated", raw: `{"rules":[{"name":"a","stage":"http","enabled":false,"cel_expression":"(("}]}`, paths: []string{"rules[0].cel_expression"}},
		{name: "errors collected across rules", raw: `{"rules":[
			{"name":"a","stage":"http"},
			{"name":"b","stage":"bad","cel_expression":"1 +"},
			{"name":"c","stage":"http","priority":1,"actions":{"override":{"x":"nope"}}}]}`,
			paths: []string{"rules[1].stage", "rules[1].cel_expression", `rules[2].actions.override["x"]`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, err := compileJSON(t, tt.raw)
			if len(tt.paths) == 0 {
				require.NoError(t, err)
				require.NotNil(t, e)
				return
			}
			require.Error(t, err)
			assert.Nil(t, e, "compile must never partially succeed")
			var verr *ValidationError
			require.True(t, errors.As(err, &verr))
			got := make([]string, 0, len(verr.Errors))
			for _, fe := range verr.Errors {
				got = append(got, fe.Path)
				assert.NotEmpty(t, fe.Message)
			}
			assert.Equal(t, tt.paths, got)
		})
	}
}

func TestCompileDoesNotMutateInput(t *testing.T) {
	cfg := &Config{Rules: []Rule{{Name: "a", Stage: StageHTTP}}}
	e, err := Compile(cfg)
	require.NoError(t, err)
	assert.Equal(t, "", cfg.Rules[0].Scope)
	assert.Equal(t, ScopeGlobal, e.Config().Rules[0].Scope)
	assert.Equal(t, DefaultUIPath, e.Config().UI.Path)
}

func TestCompileNilConfig(t *testing.T) {
	e, err := Compile(nil)
	require.NoError(t, err)
	assert.False(t, e.HasStage(StageHTTP))
	res, err := e.Apply([]byte(`{}`), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.False(t, res.Changed)
}

func TestHasStage(t *testing.T) {
	e := mustEngine(t, `{"rules":[
		{"name":"a","stage":"http"},
		{"name":"b","stage":"llm","enabled":false}]}`)
	assert.True(t, e.HasStage(StageHTTP))
	assert.False(t, e.HasStage(StageLLM), "disabled rules do not count")
	assert.False(t, e.HasStage(StageRequest))
	var nilEngine *Engine
	assert.False(t, nilEngine.HasStage(StageHTTP))
	assert.Nil(t, nilEngine.Config())
}

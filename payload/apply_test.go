package payload

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// applyOne compiles a single global http rule with the given actions and
// applies it to doc.
func applyOne(t *testing.T, actions Actions, doc string) Result {
	t.Helper()
	e, err := Compile(&Config{Rules: []Rule{{Name: "r", Stage: StageHTTP, Actions: actions}}})
	require.NoError(t, err)
	res, err := e.Apply([]byte(doc), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	return res
}

func TestActions(t *testing.T) {
	tests := []struct {
		name    string
		actions Actions
		doc     string
		want    string
	}{
		{
			name:    "default writes missing raw string",
			actions: Actions{Default: map[string]any{"reasoning.effort": `"high"`}},
			doc:     `{"model":"m"}`,
			want:    `{"model":"m","reasoning":{"effort":"high"}}`,
		},
		{
			name:    "default keeps existing",
			actions: Actions{Default: map[string]any{"max_output_tokens": 32768}},
			doc:     `{"max_output_tokens":10}`,
			want:    `{"max_output_tokens":10}`,
		},
		{
			name:    "default keeps existing null",
			actions: Actions{Default: map[string]any{"a": 1}},
			doc:     `{"a":null}`,
			want:    `{"a":null}`,
		},
		{
			name:    "raw object value",
			actions: Actions{Override: map[string]any{"text.format": `{"type":"json_object"}`}},
			doc:     `{"text":{"format":{"type":"text"}}}`,
			want:    `{"text":{"format":{"type":"json_object"}}}`,
		},
		{
			name:    "non-string values are marshalled",
			actions: Actions{Override: map[string]any{"n": 3, "b": false, "arr": []any{1, "x"}, "obj": map[string]any{"k": "v"}, "nil": nil}},
			doc:     `{}`,
			want:    `{"arr":[1,"x"],"b":false,"n":3,"nil":null,"obj":{"k":"v"}}`,
		},
		{
			name:    "json.Number from config is written verbatim",
			actions: Actions{Override: map[string]any{"n": json.Number("9007199254740993")}},
			doc:     `{}`,
			want:    `{"n":9007199254740993}`,
		},
		{
			name:    "order default then override then filter",
			actions: Actions{Default: map[string]any{"a": 1}, Override: map[string]any{"a": 2, "b": 3}, Filter: []string{"b"}},
			doc:     `{}`,
			want:    `{"a":2}`,
		},
		{
			name:    "filter missing path is a no-op",
			actions: Actions{Filter: []string{"metadata.debug"}},
			doc:     `{"x":1}`,
			want:    `{"x":1}`,
		},
		{
			name:    "filter nested",
			actions: Actions{Filter: []string{"metadata.debug"}},
			doc:     `{"metadata":{"debug":true,"keep":1}}`,
			want:    `{"metadata":{"keep":1}}`,
		},
		{
			name:    "override first query match only",
			actions: Actions{Override: map[string]any{`tools.#(type=="web_search").enabled`: true}},
			doc:     `{"tools":[{"type":"web_search"},{"type":"web_search"}]}`,
			want:    `{"tools":[{"type":"web_search","enabled":true},{"type":"web_search"}]}`,
		},
		{
			name:    "override all query matches",
			actions: Actions{Override: map[string]any{`tools.#(type=="web_search")#.enabled`: true}},
			doc:     `{"tools":[{"type":"web_search"},{"type":"fn"},{"type":"web_search"}]}`,
			want:    `{"tools":[{"type":"web_search","enabled":true},{"type":"fn"},{"type":"web_search","enabled":true}]}`,
		},
		{
			name:    "query with && and ||",
			actions: Actions{Filter: []string{`tools.#(type=="a" && name=="x" || type=="c")#`}},
			doc:     `{"tools":[{"type":"a","name":"x"},{"type":"a","name":"y"},{"type":"b"},{"type":"c"}]}`,
			want:    `{"tools":[{"type":"a","name":"y"},{"type":"b"}]}`,
		},
		{
			name:    "query no match is a no-op",
			actions: Actions{Override: map[string]any{`tools.#(type=="zzz").x`: 1}, Filter: []string{`tools.#(type=="zzz")#`}},
			doc:     `{"tools":[{"type":"a"}]}`,
			want:    `{"tools":[{"type":"a"}]}`,
		},
		{
			name:    "query on non-array is a no-op",
			actions: Actions{Filter: []string{`tools.#(type=="a")#`}},
			doc:     `{"tools":{"type":"a"}}`,
			want:    `{"tools":{"type":"a"}}`,
		},
		{
			name:    "filter deletes matches in reverse order",
			actions: Actions{Filter: []string{`items.#(drop==true)#`}},
			doc:     `{"items":[{"id":0,"drop":true},{"id":1},{"id":2,"drop":true},{"id":3,"drop":true}]}`,
			want:    `{"items":[{"id":1}]}`,
		},
		{
			name:    "query with quoted dot and parenthesis",
			actions: Actions{Filter: []string{`tools.#(name=="a.b(c)")`}},
			doc:     `{"tools":[{"name":"a.b(c)"},{"name":"x"}]}`,
			want:    `{"tools":[{"name":"x"}]}`,
		},
		{
			name:    "escaped dot in key",
			actions: Actions{Override: map[string]any{`a\.b`: 1}},
			doc:     `{}`,
			want:    `{"a.b":1}`,
		},
		{
			name:    "array append path",
			actions: Actions{Override: map[string]any{`stop.-1`: `"END"`}},
			doc:     `{"stop":["a"]}`,
			want:    `{"stop":["a","END"]}`,
		},
		{
			// Ported from CLIProxyAPI TestApplyPayloadConfigProjectionOverrideRawWritesEveryMatch.
			name:    "projection override writes every element",
			actions: Actions{Override: map[string]any{"items.#.value": `[1,2]`}},
			doc:     `{"items":[{"value":1},{"value":2}]}`,
			want:    `{"items":[{"value":[1,2]},{"value":[1,2]}]}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := applyOne(t, tt.actions, tt.doc)
			assert.JSONEq(t, tt.want, string(res.Body))
			assert.Equal(t, tt.doc != tt.want, res.Changed)
		})
	}
}

// Ported from CLIProxyAPI TestApplyPayloadConfigReusesCanonicalOverrides: an
// override that does not change anything returns the input slice itself.
func TestOverrideIdenticalReturnsInputSlice(t *testing.T) {
	input := []byte(`{"model":"gpt-test","stream":true,"metadata":{"source":"executor"},"messages":[]}`)
	e, err := Compile(&Config{Rules: []Rule{{Name: "r", Stage: StageHTTP, Actions: Actions{
		Override: map[string]any{"stream": true, "model": `"gpt-test"`, "metadata": `{"source":"executor"}`},
		Default:  map[string]any{"model": `"other"`},
		Filter:   []string{"missing"},
	}}}})
	require.NoError(t, err)
	res, err := e.Apply(input, MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Same(t, &input[0], &res.Body[0])
	assert.Equal(t, []string{"r"}, res.Applied, "a matched rule is reported even without changes")
}

func TestApplyNeverMutatesInput(t *testing.T) {
	input := []byte(`{"a":1,"b":{"c":2},"items":[1,2,3]}`)
	snapshot := string(input)
	res := applyOne(t, Actions{
		Default:  map[string]any{"z": 1},
		Override: map[string]any{"a": 5, "b.c": 7},
		Filter:   []string{"items.1"},
	}, string(input))
	require.True(t, res.Changed)
	// applyOne converts to a new slice; check the direct path as well.
	e, err := Compile(&Config{Rules: []Rule{{Name: "r", Stage: StageHTTP, Actions: Actions{Override: map[string]any{"a": 5}, Filter: []string{"b"}}}}})
	require.NoError(t, err)
	res, err = e.Apply(input, MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	require.True(t, res.Changed)
	assert.Equal(t, snapshot, string(input))
}

// Ported from CLIProxyAPI TestApplyPayloadConfig_CodexAdditionalToolsFilter.
func TestCodexAdditionalToolsFilter(t *testing.T) {
	const path = `input.#(type=="additional_tools")#.tools.#(name=="functions")#.tools.#(name=="apply_patch")#`
	actions := Actions{Filter: []string{path}}

	t.Run("removes target tool and keeps other namespaces", func(t *testing.T) {
		doc := `{"model":"gpt-5-codex","input":[{"type":"additional_tools","role":"developer","tools":[
			{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"},{"type":"custom","name":"exec_command"}]},
			{"type":"namespace","name":"collaboration","tools":[{"type":"custom","name":"share"}]}]}]}`
		res := applyOne(t, actions, doc)
		out := gjson.ParseBytes(res.Body)
		assert.Equal(t, `["exec_command"]`, out.Get("input.0.tools.0.tools.#.name").Raw)
		assert.Equal(t, `["share"]`, out.Get("input.0.tools.1.tools.#.name").Raw)
	})

	t.Run("non-matching target is a no-op", func(t *testing.T) {
		doc := `{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"exec_command"}]}]}]}`
		res := applyOne(t, actions, doc)
		assert.False(t, res.Changed)
		assert.Equal(t, doc, string(res.Body))
	})

	t.Run("removes matches across multiple elements", func(t *testing.T) {
		doc := `{"input":[
			{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"}]}]},
			{"type":"message","role":"user","content":"hello"},
			{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[{"type":"custom","name":"apply_patch"},{"type":"custom","name":"view_image"}]}]}]}`
		out := string(applyOne(t, actions, doc).Body)
		assert.NotContains(t, out, "apply_patch")
		assert.Contains(t, out, "view_image")
		assert.Contains(t, out, "hello")
	})

	t.Run("removes multiple matches in the same array", func(t *testing.T) {
		doc := `{"input":[{"type":"additional_tools","tools":[{"type":"namespace","name":"functions","tools":[
			{"type":"custom","name":"apply_patch","id":"p1"},{"type":"custom","name":"exec_command"},{"type":"custom","name":"apply_patch","id":"p2"}]}]}]}`
		out := string(applyOne(t, actions, doc).Body)
		assert.NotContains(t, out, "apply_patch")
		assert.Contains(t, out, "exec_command")
	})
}

func TestResolvePaths(t *testing.T) {
	doc := []byte(`{"a":[{"t":"x","b":[{"n":1},{"n":2}]},{"t":"y"},{"t":"x","b":[{"n":2}]}]}`)
	tests := []struct {
		path string
		want []string
	}{
		{"", nil},
		{"plain.path", []string{"plain.path"}},
		{`a.#(t=="x")`, []string{"a.0"}},
		{`a.#(t=="x")#`, []string{"a.0", "a.2"}},
		{`a.#(t=="x")#.b.#(n==2)#.n`, []string{"a.0.b.1.n", "a.2.b.0.n"}},
		{`a.#(t=="z")#.b`, nil},
		{`#(t=="x")`, nil},                     // root is not an array
		{`a.#(t=="x"`, []string{`a.#(t=="x"`}}, // malformed queries are kept literally
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assert.Equal(t, tt.want, resolvePaths(doc, tt.path))
		})
	}
	assert.Equal(t, []string{"0", "2"}, resolvePaths([]byte(`[{"t":1},{"t":2},{"t":1}]`), `#(t==1)#`))
}

func TestValidateActionPath(t *testing.T) {
	valid := []string{"a", "a.b", `a\.b`, `tools.#(type=="x")`, `tools.#(type=="x")#.name`, `a.#(b=="(")#`, "items.#.value", "stop.-1"}
	invalid := []string{"", "  ", "a..b", "a.", `a.#(x`, `a.#(x)y`, `a.#()`, `a.#( )#`}
	for _, p := range valid {
		assert.NoError(t, validateActionPath(p), p)
	}
	for _, p := range invalid {
		assert.Error(t, validateActionPath(p), p)
	}
}

func TestSplitPathSegments(t *testing.T) {
	assert.Equal(t, []string{"a", `#(x=="a.b")#`, "c"}, splitPathSegments(`a.#(x=="a.b")#.c`))
	assert.Equal(t, []string{`a\.b`, "c"}, splitPathSegments(`a\.b.c`))
	assert.Equal(t, []string{"a", `#(x=='a.b' && (y==1))`}, splitPathSegments(`a.#(x=='a.b' && (y==1))`))
	assert.True(t, strings.HasPrefix(splitPathSegments("x")[0], "x"))
}

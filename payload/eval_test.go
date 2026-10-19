package payload

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestScopeChainAndPriority(t *testing.T) {
	// Every rule overrides "winner" with its own name; only one fires per
	// stage (first match wins, no chain rules).
	cfg := `{"rules":[
		{"name":"global-1","stage":"http","priority":1,"actions":{"override":{"winner":"\"global-1\""}}},
		{"name":"global-0","stage":"http","priority":0,"cel_expression":"model == \"m0\"","actions":{"override":{"winner":"\"global-0\""}}},
		{"name":"customer","stage":"http","scope":"customer","scope_id":"c1","actions":{"override":{"winner":"\"customer\""}}},
		{"name":"team","stage":"http","scope":"team","scope_id":"t1","actions":{"override":{"winner":"\"team\""}}},
		{"name":"user","stage":"http","scope":"user","scope_id":"u1","actions":{"override":{"winner":"\"user\""}}},
		{"name":"vk-late","stage":"http","scope":"virtual_key","scope_id":"vk1","priority":5,"actions":{"override":{"winner":"\"vk-late\""}}},
		{"name":"vk-early","stage":"http","scope":"virtual_key","scope_id":"vk1","priority":2,"cel_expression":"headers[\"X-Early\"] == \"1\"","actions":{"override":{"winner":"\"vk-early\""}}},
		{"name":"other-team","stage":"http","scope":"team","scope_id":"t2","actions":{"override":{"winner":"\"other-team\""}}},
		{"name":"llm-only","stage":"llm","actions":{"override":{"winner":"\"llm-only\""}}},
		{"name":"disabled","stage":"http","scope":"virtual_key","scope_id":"vk2","enabled":false,"actions":{"override":{"winner":"\"disabled\""}}}
	]}`
	e := mustEngine(t, cfg)

	tests := []struct {
		name string
		mc   MatchContext
		want string
	}{
		{"global by priority", MatchContext{Stage: StageHTTP}, "global-1"},
		{"global lower priority value first", MatchContext{Stage: StageHTTP, Model: "m0"}, "global-0"},
		{"customer beats global", MatchContext{Stage: StageHTTP, CustomerID: "c1"}, "customer"},
		{"team beats customer", MatchContext{Stage: StageHTTP, TeamID: "t1", CustomerID: "c1"}, "team"},
		{"user beats team", MatchContext{Stage: StageHTTP, UserID: "u1", TeamID: "t1"}, "user"},
		{"virtual key beats all", MatchContext{Stage: StageHTTP, VirtualKeyID: "vk1", UserID: "u1", TeamID: "t1", CustomerID: "c1"}, "vk-late"},
		{"priority within scope", MatchContext{Stage: StageHTTP, VirtualKeyID: "vk1", Headers: map[string]string{"x-early": "1"}}, "vk-early"},
		{"scope id must match", MatchContext{Stage: StageHTTP, TeamID: "t3"}, "global-1"},
		{"other stage", MatchContext{Stage: StageLLM, VirtualKeyID: "vk1"}, "llm-only"},
		{"disabled excluded", MatchContext{Stage: StageHTTP, VirtualKeyID: "vk2"}, "global-1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := e.Apply([]byte(`{}`), tt.mc)
			require.NoError(t, err)
			assert.Equal(t, tt.want, gjson.GetBytes(res.Body, "winner").String())
			assert.Equal(t, []string{tt.want}, res.Applied)
		})
	}

	res, err := e.Apply([]byte(`{}`), MatchContext{Stage: StageRequest})
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Empty(t, res.Applied)
}

func TestStablePriorityOrder(t *testing.T) {
	// Equal priorities cannot exist in one (scope, scope_id, stage), but the
	// sort must still be stable; check config order is used as a tie breaker
	// through the internal helper.
	rules := []*compiledRule{{name: "a", priority: 1}, {name: "b", priority: 0}, {name: "c", priority: 1}}
	sortRules(rules)
	assert.Equal(t, "b", rules[0].name)
	assert.Equal(t, "a", rules[1].name)
	assert.Equal(t, "c", rules[2].name)
}

func TestChainRules(t *testing.T) {
	tests := []struct {
		name    string
		rules   string
		doc     string
		mc      MatchContext
		want    string
		applied []string
	}{
		{
			name: "non-chain rule stops evaluation",
			rules: `{"name":"a","stage":"http","priority":0,"actions":{"override":{"a":1}}},
				{"name":"b","stage":"http","priority":1,"actions":{"override":{"b":1}}}`,
			doc: `{}`, want: `{"a":1}`, applied: []string{"a"},
		},
		{
			name: "chain rule continues with the next match",
			rules: `{"name":"a","stage":"http","priority":0,"chain_rule":true,"actions":{"override":{"a":1}}},
				{"name":"b","stage":"http","priority":1,"actions":{"override":{"b":1}}},
				{"name":"c","stage":"http","priority":2,"actions":{"override":{"c":1}}}`,
			doc: `{}`, want: `{"a":1,"b":1}`, applied: []string{"a", "b"},
		},
		{
			name: "chain re-evaluates against the modified body",
			rules: `{"name":"needs-flag","stage":"http","priority":0,"cel_expression":"has(body.flag)","actions":{"override":{"seen":true}}},
				{"name":"set-flag","stage":"http","priority":1,"chain_rule":true,"actions":{"override":{"flag":1}}}`,
			doc: `{}`, want: `{"flag":1,"seen":true}`, applied: []string{"set-flag", "needs-flag"},
		},
		{
			name: "chain restarts from the most specific scope",
			rules: `{"name":"global","stage":"http","chain_rule":true,"actions":{"override":{"g":1}}},
				{"name":"team","stage":"http","scope":"team","scope_id":"t1","cel_expression":"has(body.g)","actions":{"override":{"t":1}}}`,
			doc: `{}`, mc: MatchContext{TeamID: "t1"}, want: `{"g":1,"t":1}`, applied: []string{"global", "team"},
		},
		{
			name: "all chain rules fire once then stop",
			rules: `{"name":"a","stage":"http","priority":0,"chain_rule":true,"actions":{"override":{"a":1}}},
				{"name":"b","stage":"http","priority":1,"chain_rule":true,"actions":{"override":{"b":1}}}`,
			doc: `{}`, want: `{"a":1,"b":1}`, applied: []string{"a", "b"},
		},
		{
			name: "default first writer wins across a chain",
			rules: `{"name":"a","stage":"http","priority":0,"chain_rule":true,"actions":{"default":{"x":"\"a\""}}},
				{"name":"b","stage":"http","priority":1,"actions":{"default":{"x":"\"b\"","y":"\"b\""}}}`,
			doc: `{}`, want: `{"x":"a","y":"b"}`, applied: []string{"a", "b"},
		},
		{
			name: "default checks the original document",
			rules: `{"name":"a","stage":"http","priority":0,"chain_rule":true,"actions":{"filter":["x"]}},
				{"name":"b","stage":"http","priority":1,"actions":{"default":{"x":2}}}`,
			doc: `{"x":1}`, want: `{}`, applied: []string{"a", "b"},
		},
		{
			name: "override last writer wins across a chain",
			rules: `{"name":"a","stage":"http","priority":0,"chain_rule":true,"actions":{"override":{"x":"\"a\""}}},
				{"name":"b","stage":"http","priority":1,"actions":{"override":{"x":"\"b\""}}}`,
			doc: `{}`, want: `{"x":"b"}`, applied: []string{"a", "b"},
		},
		{
			name: "default overwrites a value written by an earlier override",
			rules: `{"name":"a","stage":"http","priority":0,"chain_rule":true,"actions":{"override":{"x":"\"a\""}}},
				{"name":"b","stage":"http","priority":1,"actions":{"default":{"x":"\"b\""}}}`,
			doc: `{}`, want: `{"x":"b"}`, applied: []string{"a", "b"},
		},
		{
			name:  "no match yields no change",
			rules: `{"name":"a","stage":"http","cel_expression":"false","chain_rule":true,"actions":{"override":{"a":1}}}`,
			doc:   `{"z":0}`, want: `{"z":0}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := mustEngine(t, `{"rules":[`+tt.rules+`]}`)
			mc := tt.mc
			mc.Stage = StageHTTP
			res, err := e.Apply([]byte(tt.doc), mc)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(res.Body))
			assert.Equal(t, tt.applied, res.Applied)
		})
	}
}

func TestChainMaxDepth(t *testing.T) {
	var rules []string
	for i := 0; i < MaxChainDepth+5; i++ {
		rules = append(rules, fmt.Sprintf(`{"name":"r%d","stage":"http","priority":%d,"chain_rule":true,"actions":{"override":{"r%d":true}}}`, i, i, i))
	}
	e := mustEngine(t, `{"rules":[`+strings.Join(rules, ",")+`]}`)
	res, err := e.Apply([]byte(`{}`), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	require.Len(t, res.Applied, MaxChainDepth)
	assert.Equal(t, "r9", res.Applied[MaxChainDepth-1])
	assert.True(t, gjson.GetBytes(res.Body, "r9").Bool())
	assert.False(t, gjson.GetBytes(res.Body, "r10").Exists())
}

func TestApplyDocumentEdgeCases(t *testing.T) {
	e := mustEngine(t, `{"rules":[{"name":"r","stage":"http","actions":{"default":{"a":1}}}]}`)

	res, err := e.Apply(nil, MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.False(t, res.Changed)
	assert.Nil(t, res.Body)

	res, err = e.Apply([]byte("  "), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.False(t, res.Changed)

	bad := []byte(`{"a":`)
	res, err = e.Apply(bad, MatchContext{Stage: StageHTTP})
	require.True(t, errors.Is(err, ErrInvalidDocument))
	assert.Equal(t, bad, res.Body)
	assert.False(t, res.Changed)

	var nilEngine *Engine
	res, err = nilEngine.Apply([]byte(`{}`), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.False(t, res.Changed)
}

func TestApplyConcurrent(t *testing.T) {
	e := mustEngine(t, `{"rules":[{"name":"r","stage":"http","cel_expression":"body.n > 1","actions":{"override":{"x":1}}}]}`)
	done := make(chan bool)
	for i := 0; i < 8; i++ {
		go func(i int) {
			res, err := e.Apply([]byte(fmt.Sprintf(`{"n":%d}`, i)), MatchContext{Stage: StageHTTP})
			done <- err == nil && res.Changed == (i > 1)
		}(i)
	}
	for i := 0; i < 8; i++ {
		assert.True(t, <-done)
	}
}

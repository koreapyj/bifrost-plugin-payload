package payload

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNormalizeMapKeysInCEL(t *testing.T) {
	tests := []struct{ in, want string }{
		{`headers["X-Tier"] == "Pro"`, `headers["x-tier"] == "Pro"`},
		{`headers['X-Tier'] == 'Pro'`, `headers['x-tier'] == 'Pro'`},
		{`params["Region"] == "EU"`, `params["region"] == "EU"`},
		{`"X-Api-Key" in headers`, `"x-api-key" in headers`},
		{`'Region'   in params && model == "GPT"`, `'region'   in params && model == "GPT"`},
		{`body.Model == "X"`, `body.Model == "X"`},
		{`headers.foo == "A"`, `headers.foo == "A"`},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.want, NormalizeMapKeysInCEL(tt.in))
		})
	}
}

func TestBodyVariable(t *testing.T) {
	assert.Equal(t, map[string]any{}, bodyVariable([]byte(`[1,2]`)))
	assert.Equal(t, map[string]any{}, bodyVariable([]byte(`not json`)))
	assert.Equal(t, map[string]any{}, bodyVariable([]byte(`{} x`)))
	assert.Equal(t, map[string]any{}, bodyVariable(nil))
	got := bodyVariable([]byte(`{"n":1,"f":1.5,"big":1e400,"a":[{"x":2}]}`))
	assert.Equal(t, int64(1), got["n"])
	assert.Equal(t, 1.5, got["f"])
	assert.Equal(t, "1e400", got["big"])
	assert.Equal(t, int64(2), got["a"].([]any)[0].(map[string]any)["x"])
}

// TestCELVariables checks every variable exposed to conditions through the
// engine, using a single always-overriding action as the match signal.
func TestCELVariables(t *testing.T) {
	mc := MatchContext{
		Stage:          StageHTTP,
		Provider:       "openai",
		Model:          "gpt-5-mini",
		RequestType:    "chat_completion",
		Path:           "/v1/chat/completions",
		FromProtocol:   "openai",
		BodyFormat:     BodyFormatRaw,
		Headers:        map[string]string{"X-Tier": "pro", "Content-Type": "application/json"},
		Params:         map[string]string{"Region": "eu"},
		VirtualKeyID:   "vk-1",
		VirtualKeyName: "vk-name",
		UserID:         "u-1",
		TeamID:         "t-1",
		TeamName:       "team",
		CustomerID:     "c-1",
		CustomerName:   "cust",
	}
	doc := `{"model":"gpt-5-mini","max_tokens":200,"n":1,"temperature":0.5,"stream":true,"metadata":{"client":"codex"},"tools":[{"type":"function"},{"type":"web_search"}]}`

	tests := []struct {
		expr  string
		match bool
	}{
		{``, true},
		{`true`, true},
		{`false`, false},
		{`provider == "openai" && model.startsWith("gpt-5")`, true},
		{`request_type == "chat_completion"`, true},
		{`headers["X-Tier"] == "pro"`, true},
		{`headers["x-tier"] == "pro"`, true},
		{`"X-TIER" in headers`, true},
		{`headers["x-missing"] == "pro"`, false}, // no such key -> no match
		{`params["REGION"] == "eu"`, true},
		{`virtual_key_id == "vk-1" && virtual_key_name == "vk-name"`, true},
		{`user_id == "u-1" && team_id == "t-1" && team_name == "team"`, true},
		{`customer_id == "c-1" && customer_name == "cust"`, true},
		{`stage == "http" && path == "/v1/chat/completions"`, true},
		{`from_protocol == "openai" && body_format == "raw"`, true},
		{`body.max_tokens > 100`, true},
		{`body.max_tokens > 1000`, false},
		{`body.n == 1`, true},
		{`body.temperature < 1.0`, true},
		{`body.temperature < 1`, true},
		{`body.metadata.client == "codex"`, true},
		{`has(body.tools)`, true},
		{`has(body.missing)`, false},
		{`body.tools.exists(t, t.type == "web_search")`, true},
		{`body.tools.all(t, t.type == "function")`, false},
		{`body.missing.deep == 1`, false}, // eval error -> no match
		{`body.stream`, true},             // dyn bool
		{`body.model`, false},             // dyn non-bool at runtime -> no match
		{`model.matches("^gpt-[0-9]")`, true},
	}
	for _, tt := range tests {
		t.Run(tt.expr, func(t *testing.T) {
			cfg := &Config{Rules: []Rule{{
				Name: "r", Stage: StageHTTP, CelExpression: tt.expr,
				Actions: Actions{Override: map[string]any{"matched": true}},
			}}}
			e, err := Compile(cfg)
			require.NoError(t, err)
			res, err := e.Apply([]byte(doc), mc)
			require.NoError(t, err)
			assert.Equal(t, tt.match, res.Changed)
			if tt.match {
				assert.Equal(t, []string{"r"}, res.Applied)
			} else {
				assert.Empty(t, res.Applied)
			}
		})
	}
}

func TestCELNilMaps(t *testing.T) {
	e := mustEngine(t, `{"rules":[{"name":"r","stage":"http","cel_expression":"size(headers) == 0 && size(params) == 0 && !(\"a\" in headers)","actions":{"override":{"ok":true}}}]}`)
	res, err := e.Apply([]byte(`{}`), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.True(t, res.Changed)
}

func TestCELNonObjectBody(t *testing.T) {
	e := mustEngine(t, `{"rules":[{"name":"r","stage":"http","cel_expression":"size(body) == 0","actions":{"override":{"0":"1"}}}]}`)
	res, err := e.Apply([]byte(`[0]`), MatchContext{Stage: StageHTTP})
	require.NoError(t, err)
	assert.Equal(t, `[1]`, string(res.Body))
}

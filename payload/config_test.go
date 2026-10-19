package payload

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		wantErr string
		check   func(t *testing.T, cfg *Config)
	}{
		{name: "empty", raw: "", check: func(t *testing.T, cfg *Config) {
			assert.Empty(t, cfg.Rules)
			assert.Equal(t, DefaultUIPath, cfg.UI.Path)
		}},
		{name: "whitespace", raw: " \n\t", check: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Rules) }},
		{name: "null", raw: "null", check: func(t *testing.T, cfg *Config) { assert.Empty(t, cfg.Rules) }},
		{name: "unknown top-level field", raw: `{"rulez":[]}`, wantErr: "unknown field"},
		{name: "unknown rule field", raw: `{"rules":[{"name":"a","stage":"http","bogus":1}]}`, wantErr: "unknown field"},
		{name: "trailing data", raw: `{} {}`, wantErr: "unexpected data"},
		{name: "syntax error", raw: `{"rules":`, wantErr: "invalid payload config"},
		{name: "defaults", raw: `{"rules":[{"name":"a","stage":"http"}],"ui":{"enabled":true}}`, check: func(t *testing.T, cfg *Config) {
			require.Len(t, cfg.Rules, 1)
			assert.Equal(t, ScopeGlobal, cfg.Rules[0].Scope)
			assert.True(t, cfg.Rules[0].IsEnabled())
			assert.Equal(t, DefaultUIPath, cfg.UI.Path)
		}},
		{name: "explicit values kept", raw: `{"rules":[{"name":"a","stage":"llm","scope":"team","scope_id":"t1","enabled":false}],"ui":{"path":"/x"}}`, check: func(t *testing.T, cfg *Config) {
			assert.Equal(t, ScopeTeam, cfg.Rules[0].Scope)
			assert.Equal(t, "t1", *cfg.Rules[0].ScopeID)
			assert.False(t, cfg.Rules[0].IsEnabled())
			assert.Equal(t, "/x", cfg.UI.Path)
		}},
		{name: "query tree accepted as free-form", raw: `{"rules":[{"name":"a","stage":"http","query":{"combinator":"and","rules":[]}}]}`, check: func(t *testing.T, cfg *Config) {
			assert.Equal(t, "and", cfg.Rules[0].Query["combinator"])
		}},
		{name: "big integers preserved", raw: `{"rules":[{"name":"a","stage":"http","actions":{"override":{"n":9007199254740993}}}]}`, check: func(t *testing.T, cfg *Config) {
			raw, err := json.Marshal(cfg.Rules[0].Actions.Override["n"])
			require.NoError(t, err)
			assert.Equal(t, "9007199254740993", string(raw))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ParseConfig([]byte(tt.raw))
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			tt.check(t, cfg)
		})
	}
}

func TestConfigFromAny(t *testing.T) {
	const raw = `{"rules":[{"name":"a","stage":"http"}]}`
	var asMap map[string]any
	require.NoError(t, json.Unmarshal([]byte(raw), &asMap))
	orig := &Config{Rules: []Rule{{Name: "a", Stage: StageHTTP}}}

	tests := []struct {
		name    string
		in      any
		rules   int
		wantErr bool
	}{
		{name: "nil", in: nil, rules: 0},
		{name: "nil *Config", in: (*Config)(nil), rules: 0},
		{name: "map", in: asMap, rules: 1},
		{name: "bytes", in: []byte(raw), rules: 1},
		{name: "string", in: raw, rules: 1},
		{name: "raw message", in: json.RawMessage(raw), rules: 1},
		{name: "*Config", in: orig, rules: 1},
		{name: "Config", in: *orig, rules: 1},
		{name: "map with unknown field", in: map[string]any{"nope": true}, wantErr: true},
		{name: "unmarshalable", in: map[string]any{"rules": make(chan int)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := ConfigFromAny(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, cfg.Rules, tt.rules)
			if tt.rules > 0 {
				assert.Equal(t, ScopeGlobal, cfg.Rules[0].Scope)
			}
			assert.Equal(t, DefaultUIPath, cfg.UI.Path)
		})
	}
	// The caller's *Config must not be modified by applying defaults.
	assert.Equal(t, "", orig.Rules[0].Scope)
	assert.Equal(t, "", orig.UI.Path)
}

func TestValidateJSON(t *testing.T) {
	assert.Nil(t, ValidateJSON([]byte(`{"rules":[{"name":"a","stage":"http","cel_expression":"model == \"x\""}]}`)))
	assert.Nil(t, ValidateJSON(nil))

	errs := ValidateJSON([]byte(`{"rules":[`))
	require.Len(t, errs, 1)
	assert.Equal(t, "", errs[0].Path)

	errs = ValidateJSON([]byte(`{"rules":[{"name":"","stage":"x"}]}`))
	require.Len(t, errs, 2)
	assert.Equal(t, "rules[0].name", errs[0].Path)
	assert.Equal(t, "rules[0].stage", errs[1].Path)
}

func TestValidationErrorMessage(t *testing.T) {
	err := &ValidationError{Errors: []FieldError{{Path: "rules[0].name", Message: "bad"}, {Message: "global"}}}
	assert.Equal(t, "invalid payload config: rules[0].name: bad; global", err.Error())
}

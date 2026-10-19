package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// IsEnabled reports whether the rule takes part in evaluation. A nil Enabled
// field means enabled.
func (r Rule) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

// ParseConfig decodes a JSON configuration strictly (unknown fields are
// rejected) and applies defaults. Empty input, whitespace and the JSON literal
// null all yield an empty configuration.
//
// Numbers inside free-form maps (actions.default, actions.override, query) are
// kept as json.Number so integer values survive a round trip unchanged.
func ParseConfig(raw []byte) (*Config, error) {
	trimmed := bytes.TrimSpace(raw)
	cfg := &Config{}
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		applyDefaults(cfg)
		return cfg, nil
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("invalid payload config: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("invalid payload config: unexpected data after the top-level JSON value")
	}
	applyDefaults(cfg)
	return cfg, nil
}

// ConfigFromAny converts what Bifrost passes to a plugin's Init into a Config.
// Supported inputs: nil, map[string]any, []byte, string, json.RawMessage,
// *Config and Config. Any other value is marshalled to JSON and parsed
// strictly. The returned Config never aliases a caller-provided *Config.
func ConfigFromAny(v any) (*Config, error) {
	switch typed := v.(type) {
	case nil:
		return ParseConfig(nil)
	case *Config:
		if typed == nil {
			return ParseConfig(nil)
		}
		cfg := cloneConfig(typed)
		applyDefaults(cfg)
		return cfg, nil
	case Config:
		cfg := cloneConfig(&typed)
		applyDefaults(cfg)
		return cfg, nil
	case []byte:
		return ParseConfig(typed)
	case json.RawMessage:
		return ParseConfig(typed)
	case string:
		return ParseConfig([]byte(typed))
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return nil, fmt.Errorf("invalid payload config: %w", err)
		}
		return ParseConfig(raw)
	}
}

// ValidateJSON parses and compiles a raw JSON configuration and returns every
// problem found, or nil when the configuration is valid. A parse error is
// reported as a single FieldError with an empty path. It is intended for the
// management UI (WebAssembly binding).
func ValidateJSON(raw []byte) []FieldError {
	cfg, err := ParseConfig(raw)
	if err != nil {
		return []FieldError{{Path: "", Message: err.Error()}}
	}
	if _, err := Compile(cfg); err != nil {
		var verr *ValidationError
		if errors.As(err, &verr) {
			return append([]FieldError(nil), verr.Errors...)
		}
		return []FieldError{{Path: "", Message: err.Error()}}
	}
	return nil
}

// Error joins all field errors into a single message.
func (e *ValidationError) Error() string {
	if e == nil || len(e.Errors) == 0 {
		return "invalid payload config"
	}
	parts := make([]string, 0, len(e.Errors))
	for _, fe := range e.Errors {
		if fe.Path == "" {
			parts = append(parts, fe.Message)
			continue
		}
		parts = append(parts, fe.Path+": "+fe.Message)
	}
	return "invalid payload config: " + strings.Join(parts, "; ")
}

// applyDefaults fills in documented defaults in place.
func applyDefaults(cfg *Config) {
	for i := range cfg.Rules {
		if cfg.Rules[i].Scope == "" {
			cfg.Rules[i].Scope = ScopeGlobal
		}
	}
	if cfg.UI.Path == "" {
		cfg.UI.Path = DefaultUIPath
	}
}

// cloneConfig copies a Config deeply enough that applying defaults or storing
// it inside an Engine never affects the caller's value. Free-form maps are
// copied one level deep; their values are treated as immutable.
func cloneConfig(src *Config) *Config {
	dst := *src
	if src.Rules != nil {
		dst.Rules = make([]Rule, len(src.Rules))
		for i, r := range src.Rules {
			if r.Enabled != nil {
				v := *r.Enabled
				r.Enabled = &v
			}
			if r.ScopeID != nil {
				v := *r.ScopeID
				r.ScopeID = &v
			}
			r.Query = cloneMap(r.Query)
			r.Actions.Default = cloneMap(r.Actions.Default)
			r.Actions.Override = cloneMap(r.Actions.Override)
			if r.Actions.Filter != nil {
				r.Actions.Filter = append([]string(nil), r.Actions.Filter...)
			}
			dst.Rules[i] = r
		}
	}
	return &dst
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

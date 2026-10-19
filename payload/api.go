// Package payload implements a Bifrost-independent request payload rewriting
// engine. Rules are selected with CEL conditions (Routing Rules semantics) and
// mutate a JSON document with gjson/sjson paths (CLIProxyAPI payload semantics).
//
// The package must stay free of Bifrost dependencies so it can be compiled to
// WebAssembly (GOOS=js GOARCH=wasm) for the management UI.
//
// This file defines the exported API shared with bifrostview/, ui/, ui/wasm and
// the plugin entry point. Implementation lives in the other files of this
// package.
package payload

// Stage names.
const (
	StageHTTP    = "http"
	StageRequest = "request"
	StageLLM     = "llm"
)

// Scope names, ordered from most specific to least specific.
const (
	ScopeVirtualKey = "virtual_key"
	ScopeUser       = "user"
	ScopeTeam       = "team"
	ScopeCustomer   = "customer"
	ScopeGlobal     = "global"
)

// Body formats exposed to CEL as body_format.
const (
	BodyFormatRaw     = "raw"
	BodyFormatBifrost = "bifrost"
)

// DefaultUIPath is the default mount path of the management UI.
const DefaultUIPath = "/v1/models/_payload/ui"

// Config is the plugin configuration as passed to Init (JSON, snake_case).
type Config struct {
	Rules                  []Rule   `json:"rules"`
	PassthroughExtraParams bool     `json:"passthrough_extra_params"`
	UI                     UIConfig `json:"ui"`
}

// UIConfig configures the management UI served by the plugin.
type UIConfig struct {
	Enabled bool   `json:"enabled"`
	Path    string `json:"path,omitempty"`     // defaults to DefaultUIPath
	APIBase string `json:"api_base,omitempty"` // no default; empty disables the UI
}

// Rule is a single payload rewriting rule.
type Rule struct {
	Name          string         `json:"name"`
	Description   string         `json:"description,omitempty"`
	Enabled       *bool          `json:"enabled,omitempty"` // nil means true
	Stage         string         `json:"stage"`             // http | request | llm
	Scope         string         `json:"scope,omitempty"`   // defaults to global
	ScopeID       *string        `json:"scope_id"`
	Priority      int            `json:"priority"`
	ChainRule     bool           `json:"chain_rule"`
	CelExpression string         `json:"cel_expression"`
	Query         map[string]any `json:"query"` // UI builder tree; ignored by the engine
	Actions       Actions        `json:"actions"`
}

// Actions are applied in the order default -> override -> filter.
//
// Values of Default and Override are raw JSON: a JSON string value is used
// verbatim as a JSON fragment (e.g. "\"high\"" is the string high), any other
// JSON value is marshalled as-is.
type Actions struct {
	Default  map[string]any `json:"default,omitempty"`
	Override map[string]any `json:"override,omitempty"`
	Filter   []string       `json:"filter,omitempty"`
}

// MatchContext carries the variables exposed to CEL conditions. The body
// variable is derived from the current document by the engine.
type MatchContext struct {
	Stage        string            `json:"stage"`
	Provider     string            `json:"provider"`
	Model        string            `json:"model"`
	RequestType  string            `json:"request_type"`
	Path         string            `json:"path"`
	FromProtocol string            `json:"from_protocol"`
	BodyFormat   string            `json:"body_format"`
	Headers      map[string]string `json:"headers"` // keys are lower-cased by the engine
	Params       map[string]string `json:"params"`  // keys are lower-cased by the engine

	VirtualKeyID   string `json:"virtual_key_id"`
	VirtualKeyName string `json:"virtual_key_name"`
	UserID         string `json:"user_id"`
	TeamID         string `json:"team_id"`
	TeamName       string `json:"team_name"`
	CustomerID     string `json:"customer_id"`
	CustomerName   string `json:"customer_name"`
}

// FieldError is a validation error located at a JSON path inside the config,
// e.g. "rules[2].cel_expression" or "rules[0].actions.default[\"a.b\"]".
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError aggregates all FieldErrors found while compiling a config.
// Its Error method joins all messages.
type ValidationError struct {
	Errors []FieldError `json:"errors"`
}

// Result is the outcome of Engine.Apply.
type Result struct {
	Body    []byte   // resulting document (the input slice when nothing changed)
	Changed bool     // true when Body differs from the input
	Applied []string // names of the rules whose actions were applied, in order
}

// Exported functions implemented elsewhere in this package (contract):
//
//	func ParseConfig(raw []byte) (*Config, error)
//	    Strict JSON decode (unknown fields rejected) + defaults.
//	func ConfigFromAny(v any) (*Config, error)
//	    Accepts what Bifrost passes to Init: map[string]any, []byte, string,
//	    json.RawMessage, *Config, Config, or nil (empty config).
//	func Compile(cfg *Config) (*Engine, error)
//	    Validates and pre-compiles every rule. Returns *ValidationError on
//	    failure. Never partially succeeds.
//	func (e *Engine) Config() *Config
//	func (e *Engine) HasStage(stage string) bool
//	    True when at least one enabled rule targets the stage (fast path).
//	func (e *Engine) Apply(doc []byte, mc MatchContext) (Result, error)
//	    Runs the scope chain for mc.Stage over doc. Never mutates doc.
//	    An empty document is a no-op. An invalid JSON document is returned
//	    unchanged together with ErrInvalidDocument.
//	func (r Rule) IsEnabled() bool
//	    Enabled == nil means true.
//	func ValidateJSON(raw []byte) []FieldError
//	    ParseConfig + Compile; nil when valid. A parse error is reported as a
//	    single FieldError with an empty Path.
//	const MaxChainDepth = 10
//	var ErrInvalidDocument error
//	func NormalizeMapKeysInCEL(expr string) string

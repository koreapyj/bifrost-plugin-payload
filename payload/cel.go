package payload

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"

	"github.com/google/cel-go/cel"
	"github.com/google/cel-go/common/types"
)

// CEL support for rule conditions.
//
// The variable schema mirrors Bifrost Routing Rules
// (plugins/routing/rules/engine.go, createCELEnvironment; Apache-2.0) so that
// expressions written for routing rules work unchanged. Routing enables no CEL
// extensions, so none are enabled here either. The plugin adds stage, path,
// from_protocol, body_format and body.

// Patterns used by NormalizeMapKeysInCEL. They reproduce the behavior of
// Bifrost's framework/routing.NormalizeMapKeysInCEL (Apache-2.0).
var (
	celHeaderKeyPattern = regexp.MustCompile(`headers\[["']([^"']+)["']\]`)
	celHeaderInPattern  = regexp.MustCompile(`["']([^"']+)["']\s+in\s+headers`)
	celParamKeyPattern  = regexp.MustCompile(`params\[["']([^"']+)["']\]`)
	celParamInPattern   = regexp.MustCompile(`["']([^"']+)["']\s+in\s+params`)
)

// NormalizeMapKeysInCEL lower-cases literal header and param keys in a CEL
// expression so they match the lower-cased map keys exposed at runtime:
// headers["X-Tier"] becomes headers["x-tier"] and "X-Tier" in headers becomes
// "x-tier" in headers (likewise for params). The whole matched fragment is
// lower-cased, exactly like Bifrost's implementation.
func NormalizeMapKeysInCEL(expr string) string {
	lower := strings.ToLower
	expr = celHeaderKeyPattern.ReplaceAllStringFunc(expr, lower)
	expr = celParamKeyPattern.ReplaceAllStringFunc(expr, lower)
	expr = celHeaderInPattern.ReplaceAllStringFunc(expr, lower)
	expr = celParamInPattern.ReplaceAllStringFunc(expr, lower)
	return expr
}

var (
	celEnvOnce sync.Once
	celEnv     *cel.Env
	celEnvErr  error
)

// celEnvironment returns the shared CEL environment (built once).
func celEnvironment() (*cel.Env, error) {
	celEnvOnce.Do(func() {
		celEnv, celEnvErr = cel.NewEnv(
			// Routing Rules variables.
			cel.Variable("model", cel.StringType),
			cel.Variable("provider", cel.StringType),
			cel.Variable("request_type", cel.StringType),
			cel.Variable("headers", cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable("params", cel.MapType(cel.StringType, cel.StringType)),
			cel.Variable("virtual_key_id", cel.StringType),
			cel.Variable("virtual_key_name", cel.StringType),
			cel.Variable("user_id", cel.StringType),
			cel.Variable("team_id", cel.StringType),
			cel.Variable("team_name", cel.StringType),
			cel.Variable("customer_id", cel.StringType),
			cel.Variable("customer_name", cel.StringType),
			// Plugin variables.
			cel.Variable("stage", cel.StringType),
			cel.Variable("path", cel.StringType),
			cel.Variable("from_protocol", cel.StringType),
			cel.Variable("body_format", cel.StringType),
			cel.Variable("body", cel.DynType),
		)
	})
	return celEnv, celEnvErr
}

// compileCondition compiles a rule condition. An empty (or whitespace-only)
// expression yields a nil program, which always matches. The expression must
// evaluate to bool; dyn results (e.g. a bare body.stream) are accepted and a
// non-bool value at runtime is treated as no match.
func compileCondition(expr string) (cel.Program, error) {
	if strings.TrimSpace(expr) == "" {
		return nil, nil
	}
	env, err := celEnvironment()
	if err != nil {
		return nil, fmt.Errorf("failed to initialize CEL environment: %w", err)
	}
	ast, iss := env.Compile(NormalizeMapKeysInCEL(expr))
	if iss != nil && iss.Err() != nil {
		return nil, fmt.Errorf("CEL compile error: %s", iss.Err().Error())
	}
	out := ast.OutputType()
	if !out.IsExactType(types.BoolType) && !out.IsExactType(types.DynType) {
		return nil, fmt.Errorf("CEL expression must return bool, got %s", out.String())
	}
	prg, err := env.Program(ast)
	if err != nil {
		return nil, fmt.Errorf("CEL program error: %w", err)
	}
	return prg, nil
}

// evalCondition runs a compiled condition. Any evaluation error (including
// "no such key"), an unknown result or a non-bool result is a non-match, as in
// Routing Rules.
func evalCondition(prg cel.Program, vars map[string]any) bool {
	if prg == nil {
		return true
	}
	out, _, err := prg.Eval(vars)
	if err != nil || out == nil || types.IsUnknownOrError(out) {
		return false
	}
	b, ok := out.Value().(bool)
	return ok && b
}

// baseVariables builds the CEL activation from a match context, without body.
func baseVariables(mc MatchContext) map[string]any {
	return map[string]any{
		"model":            mc.Model,
		"provider":         mc.Provider,
		"request_type":     mc.RequestType,
		"headers":          lowerKeys(mc.Headers),
		"params":           lowerKeys(mc.Params),
		"virtual_key_id":   mc.VirtualKeyID,
		"virtual_key_name": mc.VirtualKeyName,
		"user_id":          mc.UserID,
		"team_id":          mc.TeamID,
		"team_name":        mc.TeamName,
		"customer_id":      mc.CustomerID,
		"customer_name":    mc.CustomerName,
		"stage":            mc.Stage,
		"path":             mc.Path,
		"from_protocol":    mc.FromProtocol,
		"body_format":      mc.BodyFormat,
	}
}

func lowerKeys(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[strings.ToLower(k)] = v
	}
	return out
}

// bodyVariable decodes doc for the CEL body variable. Objects become
// map[string]any; integral numbers become int64 and other numbers float64 so
// that comparisons such as body.n == 1 and body.max_tokens > 100 behave
// naturally. Anything that is not a JSON object yields an empty map.
func bodyVariable(doc []byte) map[string]any {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return map[string]any{}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return map[string]any{} // trailing data: not a single JSON value
	}
	obj, ok := normalizeNumbers(v).(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return obj
}

func normalizeNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = normalizeNumbers(e)
		}
		return t
	case []any:
		for i, e := range t {
			t[i] = normalizeNumbers(e)
		}
		return t
	case json.Number:
		if i, err := t.Int64(); err == nil {
			return i
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return t.String()
	default:
		return v
	}
}

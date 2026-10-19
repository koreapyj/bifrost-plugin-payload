// Package engineapi is the string-in/string-out facade of the payload engine
// used by the WebAssembly binding (ui/wasm). It has no syscall/js dependency so
// the exact same code is exercised by native Go tests.
package engineapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/koreapyj/bifrost-plugin-payload/payload"
)

// ValidateResult is the JSON shape returned by Validate.
type ValidateResult struct {
	OK     bool                 `json:"ok"`
	Errors []payload.FieldError `json:"errors"`
}

// ApplyResult is the JSON shape returned by Apply. Body is the resulting
// document as text.
type ApplyResult struct {
	OK      bool                 `json:"ok"`
	Body    string               `json:"body"`
	Changed bool                 `json:"changed"`
	Applied []string             `json:"applied"`
	Errors  []payload.FieldError `json:"errors"`
}

// The last compiled engine is cached so dry runs on every keystroke do not
// recompile every CEL program.
var (
	cacheMu   sync.Mutex
	cacheSet  bool
	cachedCfg string
	cachedEng *payload.Engine
	cachedErr []payload.FieldError
)

func compile(cfgJSON string) (*payload.Engine, []payload.FieldError) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if cacheSet && cfgJSON == cachedCfg {
		return cachedEng, cachedErr
	}
	eng, errs := compileUncached(cfgJSON)
	cacheSet, cachedCfg, cachedEng, cachedErr = true, cfgJSON, eng, errs
	return eng, errs
}

func compileUncached(cfgJSON string) (*payload.Engine, []payload.FieldError) {
	cfg, err := payload.ParseConfig([]byte(cfgJSON))
	if err != nil {
		return nil, []payload.FieldError{{Path: "", Message: err.Error()}}
	}
	eng, err := payload.Compile(cfg)
	if err != nil {
		var verr *payload.ValidationError
		if errors.As(err, &verr) && len(verr.Errors) > 0 {
			return nil, append([]payload.FieldError(nil), verr.Errors...)
		}
		return nil, []payload.FieldError{{Path: "", Message: err.Error()}}
	}
	return eng, nil
}

// ValidateResultFor validates a JSON config.
func ValidateResultFor(cfgJSON string) ValidateResult {
	_, errs := compile(cfgJSON)
	if errs == nil {
		errs = []payload.FieldError{}
	}
	return ValidateResult{OK: len(errs) == 0, Errors: errs}
}

// ApplyResultFor runs the config over docJSON with the payload.MatchContext
// in ctxJSON.
func ApplyResultFor(cfgJSON, docJSON, ctxJSON string) ApplyResult {
	res := ApplyResult{Applied: []string{}, Errors: []payload.FieldError{}}
	eng, errs := compile(cfgJSON)
	if len(errs) > 0 {
		res.Errors = errs
		return res
	}
	var mc payload.MatchContext
	if ctxJSON != "" {
		if err := json.Unmarshal([]byte(ctxJSON), &mc); err != nil {
			res.Errors = []payload.FieldError{{Path: "context", Message: "invalid match context: " + err.Error()}}
			return res
		}
	}
	if !json.Valid([]byte(docJSON)) {
		res.Errors = []payload.FieldError{{Path: "body", Message: "sample body is not valid JSON"}}
		return res
	}
	r, err := eng.Apply([]byte(docJSON), mc)
	if err != nil {
		res.Errors = []payload.FieldError{{Path: "", Message: err.Error()}}
		return res
	}
	res.OK = true
	res.Body = string(r.Body)
	res.Changed = r.Changed
	if r.Applied != nil {
		res.Applied = r.Applied
	}
	return res
}

// Validate returns ValidateResultFor as JSON.
func Validate(cfgJSON string) string {
	return marshal(guard(func() any { return ValidateResultFor(cfgJSON) }))
}

// Apply returns ApplyResultFor as JSON.
func Apply(cfgJSON, docJSON, ctxJSON string) string {
	return marshal(guard(func() any { return ApplyResultFor(cfgJSON, docJSON, ctxJSON) }))
}

// guard turns a panic into an error result instead of killing the runtime
// (which would break every later call from the page).
func guard(fn func() any) (out any) {
	defer func() {
		if r := recover(); r != nil {
			out = ApplyResult{Applied: []string{}, Errors: []payload.FieldError{{Message: fmt.Sprintf("engine panic: %v", r)}}}
		}
	}()
	return fn()
}

func marshal(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(ValidateResult{Errors: []payload.FieldError{{Message: err.Error()}}})
	}
	return string(b)
}

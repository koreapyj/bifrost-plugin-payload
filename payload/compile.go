package payload

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/cel-go/cel"
)

// Engine is an immutable, pre-compiled rule set. It is safe for concurrent
// use.
type Engine struct {
	cfg *Config
	// stages maps a stage name to its scope index. Only enabled rules are
	// indexed.
	stages map[string]*stageIndex
}

// stageIndex holds the enabled rules of one stage grouped by scope level.
type stageIndex struct {
	// scoped maps scope name -> scope id -> rules sorted by priority.
	scoped map[string]map[string][]*compiledRule
	global []*compiledRule
}

// compiledRule is a validated rule with its condition program and actions
// pre-encoded as raw JSON.
type compiledRule struct {
	index    int // position in Config.Rules; identity for the visited set
	name     string
	priority int
	chain    bool
	program  cel.Program // nil means always match
	defaults []rawAction
	override []rawAction
	filter   []string
}

// rawAction is a path with its raw JSON value.
type rawAction struct {
	path  string
	value []byte
}

// Config returns the configuration the engine was compiled from (with
// defaults applied). Callers must treat it as read-only.
func (e *Engine) Config() *Config {
	if e == nil {
		return nil
	}
	return e.cfg
}

// HasStage reports whether at least one enabled rule targets stage.
func (e *Engine) HasStage(stage string) bool {
	if e == nil {
		return false
	}
	_, ok := e.stages[stage]
	return ok
}

var validStages = map[string]bool{StageHTTP: true, StageRequest: true, StageLLM: true}

var validScopes = map[string]bool{
	ScopeVirtualKey: true, ScopeUser: true, ScopeTeam: true, ScopeCustomer: true, ScopeGlobal: true,
}

// Compile validates every rule (enabled or not), pre-compiles CEL conditions
// and action values, and returns an Engine. On any problem it returns a
// *ValidationError listing all of them and no Engine.
func Compile(cfg *Config) (*Engine, error) {
	if cfg == nil {
		cfg = &Config{}
	}
	cfg = cloneConfig(cfg)
	applyDefaults(cfg)

	var errs []FieldError
	add := func(path, format string, args ...any) {
		errs = append(errs, FieldError{Path: path, Message: fmt.Sprintf(format, args...)})
	}

	type priorityKey struct {
		scope, scopeID, stage string
		priority              int
	}
	seenPriority := make(map[priorityKey]int)
	compiled := make([]*compiledRule, len(cfg.Rules))

	for i := range cfg.Rules {
		r := &cfg.Rules[i]
		base := fmt.Sprintf("rules[%d]", i)
		ok := true
		fail := func(field, format string, args ...any) {
			ok = false
			add(base+field, format, args...)
		}

		if strings.TrimSpace(r.Name) == "" {
			fail(".name", "name must not be empty")
		}
		if !validStages[r.Stage] {
			fail(".stage", "stage must be one of http, request, llm; got %q", r.Stage)
		}
		scopeOK := validScopes[r.Scope]
		if !scopeOK {
			fail(".scope", "scope must be one of virtual_key, user, team, customer, global; got %q", r.Scope)
		}
		scopeID := ""
		if r.ScopeID != nil {
			scopeID = *r.ScopeID
		}
		if scopeOK {
			if r.Scope == ScopeGlobal && scopeID != "" {
				fail(".scope_id", "scope_id must be null or empty for global scope")
			}
			if r.Scope != ScopeGlobal {
				switch {
				case strings.TrimSpace(scopeID) == "":
					fail(".scope_id", "scope_id is required for %s scope", r.Scope)
				case strings.TrimSpace(scopeID) != scopeID:
					fail(".scope_id", "scope_id must not have leading or trailing whitespace")
				}
			}
		}
		if scopeOK && validStages[r.Stage] {
			// Like Routing Rules (HTTP 409), a priority must be unique within its
			// scope; here the stage is part of the key since stages are evaluated
			// independently. Disabled rules count too.
			key := priorityKey{r.Scope, scopeID, r.Stage, r.Priority}
			if prev, dup := seenPriority[key]; dup {
				fail(".priority", "priority %d already used by rules[%d] in the same stage and scope", r.Priority, prev)
			} else {
				seenPriority[key] = i
			}
		}

		prg, err := compileCondition(r.CelExpression)
		if err != nil {
			fail(".cel_expression", "%s", err.Error())
		}

		cr := &compiledRule{
			index:    i,
			name:     r.Name,
			priority: r.Priority,
			chain:    r.ChainRule,
			program:  prg,
		}
		cr.defaults = compileValues(r.Actions.Default, ".actions.default", fail)
		cr.override = compileValues(r.Actions.Override, ".actions.override", fail)
		for j, p := range r.Actions.Filter {
			if err := validateActionPath(p); err != nil {
				fail(fmt.Sprintf(".actions.filter[%d]", j), "%s", err.Error())
				continue
			}
			cr.filter = append(cr.filter, strings.TrimSpace(p))
		}
		if ok {
			compiled[i] = cr
		}
	}

	if len(errs) > 0 {
		return nil, &ValidationError{Errors: errs}
	}

	e := &Engine{cfg: cfg, stages: make(map[string]*stageIndex)}
	for i, cr := range compiled {
		r := &cfg.Rules[i]
		if !r.IsEnabled() {
			continue
		}
		idx := e.stages[r.Stage]
		if idx == nil {
			idx = &stageIndex{scoped: make(map[string]map[string][]*compiledRule)}
			e.stages[r.Stage] = idx
		}
		if r.Scope == ScopeGlobal {
			idx.global = append(idx.global, cr)
			continue
		}
		byID := idx.scoped[r.Scope]
		if byID == nil {
			byID = make(map[string][]*compiledRule)
			idx.scoped[r.Scope] = byID
		}
		byID[*r.ScopeID] = append(byID[*r.ScopeID], cr)
	}
	for _, idx := range e.stages {
		sortRules(idx.global)
		for _, byID := range idx.scoped {
			for _, rules := range byID {
				sortRules(rules)
			}
		}
	}
	return e, nil
}

func sortRules(rules []*compiledRule) {
	sort.SliceStable(rules, func(a, b int) bool { return rules[a].priority < rules[b].priority })
}

// compileValues validates action paths and encodes their values as raw JSON.
// Keys are processed in sorted order so evaluation is deterministic.
func compileValues(values map[string]any, field string, fail func(field, format string, args ...any)) []rawAction {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]rawAction, 0, len(keys))
	for _, k := range keys {
		at := field + "[" + strconv.Quote(k) + "]"
		if err := validateActionPath(k); err != nil {
			fail(at, "%s", err.Error())
			continue
		}
		raw, err := rawJSONValue(values[k])
		if err != nil {
			fail(at, "%s", err.Error())
			continue
		}
		out = append(out, rawAction{path: strings.TrimSpace(k), value: raw})
	}
	return out
}

// rawJSONValue encodes an action value. A Go string is a raw JSON fragment
// and must be valid JSON; any other value (including nil, which becomes JSON
// null) is marshalled.
func rawJSONValue(v any) ([]byte, error) {
	if s, ok := v.(string); ok {
		s = strings.TrimSpace(s)
		if !json.Valid([]byte(s)) {
			return nil, fmt.Errorf("value is not valid JSON: %s (wrap strings in quotes, e.g. \"\\\"high\\\"\")", strconv.Quote(s))
		}
		return []byte(s), nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("value cannot be encoded as JSON: %v", err)
	}
	return raw, nil
}

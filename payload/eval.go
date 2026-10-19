package payload

import (
	"bytes"
	"encoding/json"
	"errors"
)

// MaxChainDepth bounds how many rules can fire in one Apply call through
// chain_rule. It matches Bifrost Routing's default chain depth.
const MaxChainDepth = 10

// ErrInvalidDocument is returned by Apply when the document is not valid
// JSON. The document is returned unchanged.
var ErrInvalidDocument = errors.New("payload: document is not valid JSON")

// Apply runs the rules of mc.Stage over doc and returns the result. doc is
// never mutated.
//
// Evaluation follows Bifrost Routing Rules (plugins/routing/rules/engine.go):
// scopes are visited from most to least specific (virtual_key, user, team,
// customer, global; a scope only takes part when the context carries its ID)
// and, inside a scope, rules are visited by ascending priority. The first
// matching rule fires and ends the stage, unless it is a chain rule: then the
// whole scope chain is evaluated again against the modified document,
// skipping chain rules that already fired, for at most MaxChainDepth steps.
//
// An empty document is a no-op; an invalid JSON document is returned
// unchanged together with ErrInvalidDocument.
func (e *Engine) Apply(doc []byte, mc MatchContext) (Result, error) {
	res := Result{Body: doc}
	if e == nil {
		return res, nil
	}
	idx := e.stages[mc.Stage]
	if idx == nil {
		return res, nil
	}
	if len(bytes.TrimSpace(doc)) == 0 {
		return res, nil
	}
	if !json.Valid(doc) {
		return res, ErrInvalidDocument
	}

	chain := idx.chainFor(mc)
	if len(chain) == 0 {
		return res, nil
	}

	vars := baseVariables(mc)
	st := &applyState{original: doc, out: doc, appliedDefaults: make(map[string]struct{})}
	visited := make(map[int]struct{})
	bodyStale := true

	for step := 0; step < MaxChainDepth; step++ {
		if bodyStale {
			vars["body"] = bodyVariable(st.out)
			bodyStale = false
		}
		matched := findMatch(chain, visited, vars)
		if matched == nil {
			break
		}
		before := st.out
		st.apply(matched)
		res.Applied = append(res.Applied, matched.name)
		if !matched.chain {
			break
		}
		visited[matched.index] = struct{}{}
		if !bytes.Equal(before, st.out) {
			bodyStale = true
		}
	}

	if !bytes.Equal(st.out, doc) {
		res.Body = st.out
		res.Changed = true
	}
	return res, nil
}

// chainFor returns the rule groups to visit for a request, most specific
// scope first.
func (idx *stageIndex) chainFor(mc MatchContext) [][]*compiledRule {
	var chain [][]*compiledRule
	add := func(scope, id string) {
		if id == "" {
			return
		}
		if rules := idx.scoped[scope][id]; len(rules) > 0 {
			chain = append(chain, rules)
		}
	}
	add(ScopeVirtualKey, mc.VirtualKeyID)
	add(ScopeUser, mc.UserID)
	add(ScopeTeam, mc.TeamID)
	add(ScopeCustomer, mc.CustomerID)
	if len(idx.global) > 0 {
		chain = append(chain, idx.global)
	}
	return chain
}

func findMatch(chain [][]*compiledRule, visited map[int]struct{}, vars map[string]any) *compiledRule {
	for _, rules := range chain {
		for _, r := range rules {
			if _, fired := visited[r.index]; fired {
				continue
			}
			if evalCondition(r.program, vars) {
				return r
			}
		}
	}
	return nil
}

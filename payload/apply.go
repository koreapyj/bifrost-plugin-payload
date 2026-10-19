package payload

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Action semantics are a reimplementation of CLIProxyAPI's default-raw,
// override-raw and filter payload rules
// (internal/runtime/executor/helps/payload_helpers.go, MIT license).

// applyState carries the document being rewritten across the rules fired in
// one Apply call.
type applyState struct {
	original []byte // input document of the Apply call; never written
	out      []byte // current document
	owned    bool   // out is a private copy that may be handed to sjson
	// appliedDefaults records resolved paths already written by a default so
	// that the first rule to default a field wins.
	appliedDefaults map[string]struct{}
}

// apply runs one rule's actions in the order default, override, filter.
// Paths sjson rejects are skipped, like in CLIProxyAPI.
func (s *applyState) apply(r *compiledRule) {
	for _, a := range r.defaults {
		for _, p := range resolvePaths(s.out, a.path) {
			if gjson.GetBytes(s.original, p).Exists() {
				continue
			}
			if _, done := s.appliedDefaults[p]; done {
				continue
			}
			if s.setRaw(p, a.value) {
				s.appliedDefaults[p] = struct{}{}
			}
		}
	}
	for _, a := range r.override {
		for _, p := range resolvePaths(s.out, a.path) {
			cur := gjson.GetBytes(s.out, p)
			// Same check as CLIProxyAPI: skip when the stored raw text is already
			// identical. Results with Indexes come from projections (#) and are
			// always rewritten.
			if cur.Exists() && len(cur.Indexes) == 0 && cur.Raw == string(a.value) {
				continue
			}
			s.setRaw(p, a.value)
		}
	}
	for _, f := range r.filter {
		paths := resolvePaths(s.out, f)
		// Delete from the highest index down so earlier deletions do not shift
		// the indices of later ones.
		for i := len(paths) - 1; i >= 0; i-- {
			if !gjson.GetBytes(s.out, paths[i]).Exists() {
				continue
			}
			s.mutate(func(b []byte) ([]byte, error) { return sjson.DeleteBytes(b, paths[i]) })
		}
	}
}

func (s *applyState) setRaw(path string, value []byte) bool {
	return s.mutate(func(b []byte) ([]byte, error) { return sjson.SetRawBytes(b, path, value) })
}

// mutate applies fn to a private copy of the document so the caller's input
// slice is never modified.
func (s *applyState) mutate(fn func([]byte) ([]byte, error)) bool {
	if !s.owned {
		s.out = append([]byte(nil), s.out...)
		s.owned = true
	}
	updated, err := fn(s.out)
	if err != nil {
		return false
	}
	s.out = updated
	return true
}

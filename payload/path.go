package payload

import (
	"errors"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
)

// Path handling for actions.
//
// Paths are gjson/sjson paths. In addition, a path segment of the form
// #(query) selects the first array element matching the gjson query and
// #(query)# selects every matching element. Such segments are expanded against
// the current document into concrete index paths before sjson is used, since
// sjson itself does not understand queries. Queries support && and || between
// gjson query terms, evaluated per element.
//
// The semantics are a reimplementation of the payload path resolution in
// CLIProxyAPI (internal/runtime/executor/helps/payload_helpers.go, MIT
// license).

// validateActionPath checks a path at compile time. It returns a non-nil
// error describing the first problem.
func validateActionPath(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return errors.New("path must not be empty")
	}
	for _, seg := range splitPathSegments(path) {
		if seg == "" {
			return errors.New("path contains an empty segment")
		}
		if !strings.HasPrefix(seg, "#(") {
			continue
		}
		query, _, ok := parseQuerySegment(seg)
		if !ok {
			return errors.New("malformed query segment " + strconv.Quote(seg) + ": expected #(...) or #(...)#")
		}
		if strings.TrimSpace(query) == "" {
			return errors.New("empty query segment " + strconv.Quote(seg))
		}
	}
	return nil
}

// resolvePaths expands query segments of path against doc. Paths without
// queries are returned as-is. When a query matches nothing, no path is
// returned.
func resolvePaths(doc []byte, path string) []string {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if !strings.Contains(path, "#(") {
		return []string{path}
	}
	bases := []string{""}
	for _, seg := range splitPathSegments(path) {
		query, all, ok := parseQuerySegment(seg)
		if !ok {
			for i := range bases {
				bases[i] = joinPath(bases[i], seg)
			}
			continue
		}
		var next []string
		for _, base := range bases {
			arr := valueAt(doc, base)
			if !arr.IsArray() {
				continue
			}
			for idx, item := range arr.Array() {
				if !queryMatches(item, query) {
					continue
				}
				next = append(next, joinPath(base, strconv.Itoa(idx)))
				if !all {
					break
				}
			}
		}
		if len(next) == 0 {
			return nil
		}
		bases = next
	}
	return bases
}

// scanner state shared by the splitting helpers: it tracks quoting and
// backslash escapes so separators inside string literals are ignored.
type quoteScanner struct {
	quote   byte
	escaped bool
}

// skip reports whether ch is consumed by quoting/escaping and must not be
// interpreted as a structural character.
func (s *quoteScanner) skip(ch byte) bool {
	if s.escaped {
		s.escaped = false
		return true
	}
	if ch == '\\' {
		s.escaped = true
		return true
	}
	if s.quote != 0 {
		if ch == s.quote {
			s.quote = 0
		}
		return true
	}
	if ch == '"' || ch == '\'' {
		s.quote = ch
		return true
	}
	return false
}

// splitPathSegments splits path on dots that are outside parentheses and
// quotes. Escaped dots (\.) stay inside their segment.
func splitPathSegments(path string) []string {
	var (
		segs  []string
		sc    quoteScanner
		depth int
		start int
	)
	for i := 0; i < len(path); i++ {
		ch := path[i]
		if sc.skip(ch) {
			continue
		}
		switch ch {
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case '.':
			if depth == 0 {
				segs = append(segs, path[start:i])
				start = i + 1
			}
		}
	}
	return append(segs, path[start:])
}

// parseQuerySegment recognizes #(query) and #(query)#. It returns the inner
// query, whether all matches are requested, and whether seg is a query.
func parseQuerySegment(seg string) (query string, all bool, ok bool) {
	if !strings.HasPrefix(seg, "#(") {
		return "", false, false
	}
	var sc quoteScanner
	depth := 1
	end := -1
	for i := 2; i < len(seg) && end < 0; i++ {
		ch := seg[i]
		if sc.skip(ch) {
			continue
		}
		switch ch {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				end = i
			}
		}
	}
	if end < 0 {
		return "", false, false
	}
	switch rest := seg[end+1:]; rest {
	case "":
		return strings.TrimSpace(seg[2:end]), false, true
	case "#":
		return strings.TrimSpace(seg[2:end]), true, true
	default:
		return "", false, false
	}
}

func joinPath(base, seg string) string {
	switch {
	case base == "":
		return seg
	case seg == "":
		return base
	default:
		return base + "." + seg
	}
}

func valueAt(doc []byte, path string) gjson.Result {
	if path == "" {
		return gjson.ParseBytes(doc)
	}
	return gjson.GetBytes(doc, path)
}

// queryMatches evaluates a query of gjson terms joined by || and && against a
// single array element.
func queryMatches(item gjson.Result, query string) bool {
	for _, alt := range splitLogical(query, "||") {
		terms := splitLogical(alt, "&&")
		matched := len(terms) > 0
		for _, term := range terms {
			if !termMatches(item, term) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func splitLogical(query, op string) []string {
	var (
		parts []string
		sc    quoteScanner
		start int
	)
	for i := 0; i < len(query); i++ {
		if sc.skip(query[i]) {
			continue
		}
		if strings.HasPrefix(query[i:], op) {
			parts = append(parts, strings.TrimSpace(query[start:i]))
			i += len(op) - 1
			start = i + 1
		}
	}
	return append(parts, strings.TrimSpace(query[start:]))
}

// termMatches evaluates one gjson query term by wrapping the element in a
// one-element array and asking gjson for #(term).
func termMatches(item gjson.Result, term string) bool {
	term = strings.TrimSpace(term)
	if term == "" || item.Raw == "" {
		return false
	}
	wrapped := make([]byte, 0, len(item.Raw)+2)
	wrapped = append(wrapped, '[')
	wrapped = append(wrapped, item.Raw...)
	wrapped = append(wrapped, ']')
	return gjson.GetBytes(wrapped, "#("+term+")").Exists()
}

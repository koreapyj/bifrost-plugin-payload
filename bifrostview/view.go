// Package bifrostview converts a *schemas.BifrostRequest into an editable JSON
// "Bifrost view" and folds edits made to that view back into a new request
// without mutating the original (copy-on-write).
//
// The view is the JSON encoding of the active inner request (ChatRequest,
// ResponsesRequest, EmbeddingRequest, ...) with params.ExtraParams flattened
// into the "params" object. Only the top-level keys model and params (plus
// provider and fallbacks at the request stage) may be edited; edits to any
// other key are dropped with a warning.
//
// Copy-on-write matters because Bifrost fallback attempts are shallow copies
// of the original inner request and therefore share its Params pointer and
// ExtraParams map. Decode never writes through any pointer reachable from the
// request it is given.
package bifrostview

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Stage names understood by Decode. They match the payload engine stages.
const (
	StageRequest = "request"
	StageLLM     = "llm"
)

const (
	fieldParams      = "Params"
	fieldExtraParams = "ExtraParams"
	fieldRawBody     = "RawRequestBody"
)

// ErrNoInnerRequest is returned when the BifrostRequest carries no inner request.
var ErrNoInnerRequest = errors.New("bifrostview: request has no active inner request")

// active returns the index of the active (non-nil) inner request field of req
// and the pointer value stored there. The active request is selected by the
// non-nil field, not by type: CountTokensRequest and ResponsesRequest share a
// type.
func active(req *schemas.BifrostRequest) (int, reflect.Value, bool) {
	if req == nil {
		return -1, reflect.Value{}, false
	}
	v := reflect.ValueOf(req).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := v.Field(i)
		if !t.Field(i).IsExported() || f.Kind() != reflect.Pointer || f.IsNil() {
			continue
		}
		if f.Elem().Kind() != reflect.Struct {
			continue
		}
		return i, f, true
	}
	return -1, reflect.Value{}, false
}

// withInner returns a shallow copy of req whose active inner request pointer
// (field index idx) is replaced by inner.
func withInner(req *schemas.BifrostRequest, idx int, inner reflect.Value) *schemas.BifrostRequest {
	cp := *req
	reflect.ValueOf(&cp).Elem().Field(idx).Set(inner)
	return &cp
}

// cloneInner allocates a new inner request holding a shallow copy of *ptr.
func cloneInner(ptr reflect.Value) reflect.Value {
	n := reflect.New(ptr.Elem().Type())
	n.Elem().Set(ptr.Elem())
	return n
}

// RawBody returns the RawRequestBody of the active inner request, or nil when
// there is none.
func RawBody(req *schemas.BifrostRequest) []byte {
	_, inner, ok := active(req)
	if !ok {
		return nil
	}
	f := inner.Elem().FieldByName(fieldRawBody)
	if !f.IsValid() || f.Type() != reflect.TypeOf([]byte(nil)) {
		return nil
	}
	return f.Bytes()
}

// WithRawBody returns a shallow copy of req whose active inner request is a
// new value carrying body as RawRequestBody. req is not modified. It returns
// nil when the active inner request has no RawRequestBody field.
func WithRawBody(req *schemas.BifrostRequest, body []byte) *schemas.BifrostRequest {
	idx, inner, ok := active(req)
	if !ok {
		return nil
	}
	if f := inner.Elem().FieldByName(fieldRawBody); !f.IsValid() || f.Type() != reflect.TypeOf([]byte(nil)) {
		return nil
	}
	n := cloneInner(inner)
	n.Elem().FieldByName(fieldRawBody).SetBytes(append([]byte(nil), body...))
	return withInner(req, idx, n)
}

// paramsOf returns the Params field of an inner request struct value, if any.
func paramsOf(innerElem reflect.Value) (reflect.Value, bool) {
	f := innerElem.FieldByName(fieldParams)
	if !f.IsValid() || f.Kind() != reflect.Pointer || f.Type().Elem().Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	return f, true
}

// extraParamsOf returns the ExtraParams map of a non-nil params pointer.
func extraParamsOf(params reflect.Value) map[string]any {
	if !params.IsValid() || params.IsNil() {
		return nil
	}
	f := params.Elem().FieldByName(fieldExtraParams)
	if !f.IsValid() || f.Type() != reflect.TypeOf(map[string]any(nil)) || f.IsNil() {
		return nil
	}
	return f.Interface().(map[string]any)
}

// Encode returns the Bifrost view of req: the JSON encoding of the active
// inner request with params.ExtraParams flattened into "params". An
// ExtraParams key never overrides a key the real Params fields already emit.
func Encode(req *schemas.BifrostRequest) ([]byte, error) {
	_, inner, ok := active(req)
	if !ok {
		return nil, ErrNoInnerRequest
	}
	data, err := schemas.Marshal(inner.Interface())
	if err != nil {
		return nil, fmt.Errorf("bifrostview: encode: %w", err)
	}
	params, ok := paramsOf(inner.Elem())
	if !ok {
		return data, nil
	}
	extra := extraParamsOf(params)
	if len(extra) == 0 {
		return data, nil
	}
	pr := gjson.GetBytes(data, "params")
	if !pr.IsObject() {
		return data, nil
	}
	present := objectKeys(pr)
	keys := make([]string, 0, len(extra))
	for k := range extra {
		if !present[k] {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		return data, nil
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	raw := strings.TrimSpace(pr.Raw)
	buf.WriteString(raw[:len(raw)-1]) // drop the closing brace
	first := len(present) == 0
	for _, k := range keys {
		v, err := schemas.Marshal(extra[k])
		if err != nil {
			return nil, fmt.Errorf("bifrostview: encode extra param %q: %w", k, err)
		}
		kb, _ := json.Marshal(k)
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.Write(kb)
		buf.WriteByte(':')
		buf.Write(v)
	}
	buf.WriteByte('}')
	return sjson.SetRawBytes(data, "params", buf.Bytes())
}

// objectKeys returns the set of keys of a JSON object.
func objectKeys(obj gjson.Result) map[string]bool {
	keys := map[string]bool{}
	obj.ForEach(func(k, _ gjson.Result) bool {
		keys[k.String()] = true
		return true
	})
	return keys
}

// objectEntries returns the entries of a JSON object in document order.
type entry struct {
	key    string
	rawKey string
	value  gjson.Result
}

func objectEntries(obj gjson.Result) []entry {
	var out []entry
	obj.ForEach(func(k, v gjson.Result) bool {
		out = append(out, entry{key: k.String(), rawKey: k.Raw, value: v})
		return true
	})
	return out
}

// sameJSON reports whether two raw JSON values are equal ignoring
// insignificant whitespace.
func sameJSON(a, b string) bool {
	if a == b {
		return true
	}
	var ca, cb bytes.Buffer
	if json.Compact(&ca, []byte(a)) != nil || json.Compact(&cb, []byte(b)) != nil {
		return false
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}

// jsonName returns the JSON key of a struct field as encoding/json and sonic
// see it, and whether the field is serialized at all.
func jsonName(f reflect.StructField) (string, bool) {
	if !f.IsExported() {
		return "", false
	}
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false
	}
	name, _, _ := strings.Cut(tag, ",")
	if name == "" {
		name = f.Name
	}
	return name, true
}

// fieldByJSONName returns the index of the field serialized under name.
func fieldByJSONName(t reflect.Type, name string) (int, bool) {
	for i := 0; i < t.NumField(); i++ {
		if n, ok := jsonName(t.Field(i)); ok && n == name {
			return i, true
		}
	}
	return -1, false
}

// Decode folds the edits between before (the output of Encode for req) and
// after (the edited view) back into a request.
//
// Only top-level keys whose JSON changed are rebuilt, each with a new value.
// Editable keys are model and params, plus provider and fallbacks when stage
// is "request". Other edits are dropped and reported in warnings.
//
// newReq is req itself when nothing applicable changed; otherwise it is a
// shallow copy of req holding a new inner request pointer. req and every
// value reachable from it are never modified. extraAdded reports whether the
// rebuilt params gained ExtraParams entries that were not present before
// (new keys or changed values).
func Decode(req *schemas.BifrostRequest, before, after []byte, stage string) (newReq *schemas.BifrostRequest, extraAdded bool, warnings []string, err error) {
	if bytes.Equal(before, after) {
		return req, false, nil, nil
	}
	idx, inner, ok := active(req)
	if !ok {
		return req, false, nil, ErrNoInnerRequest
	}
	if !gjson.ValidBytes(after) {
		return req, false, nil, errors.New("bifrostview: edited view is not valid JSON")
	}
	b, a := gjson.ParseBytes(before), gjson.ParseBytes(after)
	if !a.IsObject() {
		return req, false, nil, errors.New("bifrostview: edited view is not a JSON object")
	}

	// Collect changed top-level keys in a stable order.
	var changed []string
	seen := map[string]bool{}
	for _, e := range objectEntries(b) {
		seen[e.key] = true
		if av := a.Get(gjson.Escape(e.key)); !av.Exists() || !sameJSON(e.value.Raw, av.Raw) {
			changed = append(changed, e.key)
		}
	}
	for _, e := range objectEntries(a) {
		if !seen[e.key] {
			seen[e.key] = true
			changed = append(changed, e.key)
		}
	}
	if len(changed) == 0 {
		return req, false, nil, nil
	}

	innerType := inner.Elem().Type()
	var n reflect.Value // lazily allocated new inner request
	newInner := func() reflect.Value {
		if !n.IsValid() {
			n = cloneInner(inner)
		}
		return n
	}
	warnf := func(format string, args ...any) {
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}

	for _, key := range changed {
		av := a.Get(gjson.Escape(key))
		switch key {
		case "model":
			fi, ok := fieldByJSONName(innerType, key)
			if !ok || innerType.Field(fi).Type.Kind() != reflect.String {
				warnf("edit of %q dropped: %s has no editable model", key, innerType.Name())
				continue
			}
			if av.Type != gjson.String || av.Str == "" {
				warnf("edit of %q dropped: model must be a non-empty string", key)
				continue
			}
			f := newInner().Elem().Field(fi)
			f.Set(reflect.ValueOf(av.Str).Convert(f.Type()))
		case "provider":
			if stage != StageRequest {
				warnf("edit of %q ignored at stage %q: the provider is already selected", key, stage)
				continue
			}
			fi, ok := fieldByJSONName(innerType, key)
			if !ok || innerType.Field(fi).Type.Kind() != reflect.String {
				warnf("edit of %q dropped: %s has no editable provider", key, innerType.Name())
				continue
			}
			if av.Type != gjson.String || av.Str == "" {
				warnf("edit of %q dropped: provider must be a non-empty string", key)
				continue
			}
			f := newInner().Elem().Field(fi)
			f.Set(reflect.ValueOf(av.Str).Convert(f.Type()))
		case "fallbacks":
			if stage != StageRequest {
				warnf("edit of %q dropped at stage %q: only editable at stage %q", key, stage, StageRequest)
				continue
			}
			fi, ok := fieldByJSONName(innerType, key)
			if !ok {
				warnf("edit of %q dropped: %s has no fallbacks", key, innerType.Name())
				continue
			}
			ft := innerType.Field(fi).Type
			nv := reflect.New(ft)
			if av.Exists() && av.Type != gjson.Null {
				if err := schemas.Unmarshal([]byte(av.Raw), nv.Interface()); err != nil {
					warnf("edit of %q dropped: %v", key, err)
					continue
				}
			}
			newInner().Elem().Field(fi).Set(nv.Elem())
		case "params":
			pf, ok := paramsOf(inner.Elem())
			if !ok {
				warnf("edit of %q dropped: %s has no params", key, innerType.Name())
				continue
			}
			np, added, err := decodeParams(pf, av)
			if err != nil {
				return req, false, warnings, fmt.Errorf("bifrostview: params: %w", err)
			}
			extraAdded = extraAdded || added
			newInner().Elem().FieldByName(fieldParams).Set(np)
		default:
			warnf("edit of top-level key %q dropped: only model, params (and provider, fallbacks at stage %q) are editable", key, StageRequest)
		}
	}
	if !n.IsValid() {
		return req, false, warnings, nil
	}
	return withInner(req, idx, n), extraAdded, warnings, nil
}

// decodeParams builds a new Params pointer from the edited params JSON. old is
// the current Params pointer (possibly nil); it is never modified.
func decodeParams(old reflect.Value, av gjson.Result) (reflect.Value, bool, error) {
	pt := old.Type() // pointer type
	et := pt.Elem()
	if !av.Exists() || av.Type == gjson.Null {
		return reflect.Zero(pt), false, nil
	}
	if !av.IsObject() {
		return reflect.Value{}, false, errors.New("params must be a JSON object")
	}
	raw := []byte(av.Raw)
	fresh := reflect.New(et)
	if err := schemas.Unmarshal(raw, fresh.Interface()); err != nil {
		return reflect.Value{}, false, err
	}

	// Known keys are the JSON names of serialized Params fields.
	known := map[string]bool{}
	for i := 0; i < et.NumField(); i++ {
		if n, ok := jsonName(et.Field(i)); ok {
			known[n] = true
		}
	}

	// A key is unknown when it is not a field name and removing it does not
	// change the decoded value. Aliases consumed by a custom UnmarshalJSON
	// (e.g. reasoning_effort) change the result and are therefore kept out of
	// ExtraParams.
	entries := objectEntries(av)
	extras := map[string]any{}
	for i, e := range entries {
		if known[e.key] {
			continue
		}
		probe := reflect.New(et)
		if err := schemas.Unmarshal(objectWithout(entries, i), probe.Interface()); err != nil {
			continue // removing the key broke decoding: it is consumed
		}
		if !reflect.DeepEqual(probe.Elem().Interface(), fresh.Elem().Interface()) {
			continue
		}
		var v any
		if err := schemas.Unmarshal([]byte(e.value.Raw), &v); err != nil {
			return reflect.Value{}, false, fmt.Errorf("extra param %q: %w", e.key, err)
		}
		extras[e.key] = v
	}

	var oldExtra map[string]any
	if !old.IsNil() {
		oldExtra = extraParamsOf(old)
		// Reuse old field values whose serialized form is unchanged. This keeps
		// nested json:"-" data (e.g. tool annotations) of untouched fields.
		for i := 0; i < et.NumField(); i++ {
			sf := et.Field(i)
			if _, ok := jsonName(sf); !ok {
				continue
			}
			ov, nv := old.Elem().Field(i), fresh.Elem().Field(i)
			ob, err1 := schemas.Marshal(ov.Interface())
			nb, err2 := schemas.Marshal(nv.Interface())
			if err1 == nil && err2 == nil && bytes.Equal(ob, nb) {
				nv.Set(ov)
			}
		}
		// Preserve exported json:"-" fields other than ExtraParams.
		for i := 0; i < et.NumField(); i++ {
			sf := et.Field(i)
			if !sf.IsExported() || sf.Tag.Get("json") != "-" || sf.Name == fieldExtraParams {
				continue
			}
			fresh.Elem().Field(i).Set(old.Elem().Field(i))
		}
		// Carry over old extras that Encode could not flatten because a real
		// field already emitted the same key (they were invisible in the view).
		if len(oldExtra) > 0 {
			if ob, err := schemas.Marshal(old.Interface()); err == nil {
				shadowed := objectKeys(gjson.ParseBytes(ob))
				for k, v := range oldExtra {
					if _, dup := extras[k]; !dup && shadowed[k] {
						extras[k] = v
					}
				}
			}
		}
	}

	added := false
	for k, v := range extras {
		ov, ok := oldExtra[k]
		if !ok {
			added = true
			break
		}
		ob, err1 := schemas.Marshal(ov)
		nb, err2 := schemas.Marshal(v)
		if err1 != nil || err2 != nil || !sameJSON(string(ob), string(nb)) {
			added = true
			break
		}
	}

	if ef := fresh.Elem().FieldByName(fieldExtraParams); ef.IsValid() && ef.Type() == reflect.TypeOf(map[string]any(nil)) {
		if len(extras) > 0 {
			ef.Set(reflect.ValueOf(extras))
		} else {
			ef.Set(reflect.Zero(ef.Type()))
		}
	} else if len(extras) > 0 {
		return reflect.Value{}, false, fmt.Errorf("%s has no ExtraParams for unknown keys", et.Name())
	}
	return fresh, added, nil
}

// objectWithout re-serializes an object from its entries, skipping index skip.
func objectWithout(entries []entry, skip int) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	first := true
	for i, e := range entries {
		if i == skip {
			continue
		}
		if !first {
			buf.WriteByte(',')
		}
		first = false
		buf.WriteString(e.rawKey)
		buf.WriteByte(':')
		buf.WriteString(e.value.Raw)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

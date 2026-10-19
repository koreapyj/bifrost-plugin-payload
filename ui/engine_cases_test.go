package ui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/koreapyj/bifrost-plugin-payload/ui/internal/engineapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// engineCases mirrors testdata/engine_cases.json, which the Node test
// (ui/web/test/engine.test.mjs) runs through engine.wasm with the same
// expectations.
type engineCases struct {
	Validate []struct {
		Name       string          `json:"name"`
		Config     json.RawMessage `json:"config"`
		OK         bool            `json:"ok"`
		ErrorPaths []string        `json:"error_paths"`
	} `json:"validate"`
	Apply []struct {
		Name       string          `json:"name"`
		Config     json.RawMessage `json:"config"`
		Doc        json.RawMessage `json:"doc"`
		DocText    *string         `json:"doc_text"`
		Ctx        json.RawMessage `json:"ctx"`
		OK         bool            `json:"ok"`
		Changed    bool            `json:"changed"`
		Applied    []string        `json:"applied"`
		Body       json.RawMessage `json:"body"`
		ErrorPaths []string        `json:"error_paths"`
	} `json:"apply"`
}

func loadEngineCases(t *testing.T) engineCases {
	t.Helper()
	raw, err := os.ReadFile("testdata/engine_cases.json")
	require.NoError(t, err)
	var c engineCases
	require.NoError(t, json.Unmarshal(raw, &c))
	require.NotEmpty(t, c.Validate)
	require.NotEmpty(t, c.Apply)
	return c
}

func errorPaths(t *testing.T, errs []map[string]string) []string {
	t.Helper()
	out := make([]string, 0, len(errs))
	for _, e := range errs {
		assert.NotEmpty(t, e["message"])
		out = append(out, e["path"])
	}
	return out
}

func TestEngineCasesNative(t *testing.T) {
	cases := loadEngineCases(t)
	for _, tc := range cases.Validate {
		t.Run("validate/"+tc.Name, func(t *testing.T) {
			var got struct {
				OK     bool                `json:"ok"`
				Errors []map[string]string `json:"errors"`
			}
			require.NoError(t, json.Unmarshal([]byte(engineapi.Validate(string(tc.Config))), &got))
			assert.Equal(t, tc.OK, got.OK)
			paths := errorPaths(t, got.Errors)
			if tc.OK {
				assert.Empty(t, paths)
			} else {
				assert.Equal(t, tc.ErrorPaths, paths)
			}
		})
	}
	for _, tc := range cases.Apply {
		t.Run("apply/"+tc.Name, func(t *testing.T) {
			doc := string(tc.Doc)
			if tc.DocText != nil {
				doc = *tc.DocText
			}
			var got struct {
				OK      bool                `json:"ok"`
				Body    string              `json:"body"`
				Changed bool                `json:"changed"`
				Applied []string            `json:"applied"`
				Errors  []map[string]string `json:"errors"`
			}
			require.NoError(t, json.Unmarshal([]byte(engineapi.Apply(string(tc.Config), doc, string(tc.Ctx))), &got))
			require.Equal(t, tc.OK, got.OK, "errors: %v", got.Errors)
			if !tc.OK {
				assert.Equal(t, tc.ErrorPaths, errorPaths(t, got.Errors))
				return
			}
			assert.Empty(t, got.Errors)
			assert.Equal(t, tc.Changed, got.Changed)
			assert.Equal(t, tc.Applied, got.Applied)
			assert.JSONEq(t, string(tc.Body), got.Body)
			if !tc.Changed {
				assert.Equal(t, doc, got.Body, "an unchanged document is returned as-is")
			}
		})
	}
}

func TestEngineAPIRejectsBadContext(t *testing.T) {
	out := engineapi.Apply(`{}`, `{}`, `{"stage":1}`)
	assert.True(t, strings.Contains(out, `"path":"context"`), out)
}

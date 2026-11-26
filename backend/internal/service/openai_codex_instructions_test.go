package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func requireCodexDeveloperInstructions(t *testing.T, body []byte, index int, want string) {
	t.Helper()
	require.False(t, gjson.GetBytes(body, "instructions").Exists())
	items := gjson.GetBytes(body, "input").Array()
	require.Greater(t, len(items), index)
	item := items[index]
	require.Equal(t, "message", item.Get("type").String())
	require.Equal(t, "developer", item.Get("role").String())
	require.True(t, item.Get("content").IsArray())
	require.Equal(t, int64(1), item.Get("content.#").Int())
	require.Equal(t, "input_text", item.Get("content.0.type").String())
	require.Equal(t, want, item.Get("content.0.text").String())
}

// requireNoCodexImageBridgeMarker checks decoded JSON values rather than raw
// json.Marshal output so HTML-escaped < and > cannot mask the bridge marker.
func requireNoCodexImageBridgeMarker(t *testing.T, body []byte) {
	t.Helper()
	require.NotContains(t, gjson.GetBytes(body, "instructions").String(), codexImageGenerationBridgeMarker)
	input := gjson.GetBytes(body, "input")
	if input.Type == gjson.String {
		require.NotContains(t, input.String(), codexImageGenerationBridgeMarker)
		return
	}
	input.ForEach(func(_, item gjson.Result) bool {
		content := item.Get("content")
		if content.Type == gjson.String {
			require.NotContains(t, content.String(), codexImageGenerationBridgeMarker)
		} else if content.IsArray() {
			content.ForEach(func(_, part gjson.Result) bool {
				require.NotContains(t, part.Get("text").String(), codexImageGenerationBridgeMarker)
				return true
			})
		}
		return true
	})
}

func TestApplyCodexOAuthInstructionsAsDeveloper(t *testing.T) {
	const (
		developer      = `{"type":"message","role":"developer","content":[{"type":"input_text","text":" base prompt "}]}`
		existingDev    = `{"type":"message","role":"developer","content":"client prefix"}`
		existingDevArr = `{"type":"message","role":"developer","content":[{"type":"input_text","text":"client prefix"}]}`
		blankDev       = `{"type":"message","role":"developer","content":" \t\n"}`
		user           = `{"type":"message","role":"user","content":"hello"}`
		toolsA         = `{"type":"additional_tools","id":"at_a"}`
		toolsB         = `{"type":"additional_tools","id":"at_b"}`
		toolsC         = `{"type":"additional_tools","id":"at_c"}`
	)
	for _, tt := range []struct {
		name     string
		before   string
		after    string
		modified bool
	}{
		{
			name:   "missing instructions",
			before: `{"input":"hello"}`,
			after:  `{"input":"hello"}`,
		},
		{
			name:     "blank instructions",
			before:   `{"instructions":" \t\n","input":"hello"}`,
			after:    `{"input":"hello"}`,
			modified: true,
		},
		{
			name:     "null instructions",
			before:   `{"instructions":null}`,
			after:    `{}`,
			modified: true,
		},
		{
			name:     "non-string instructions",
			before:   `{"instructions":42,"input":[` + user + `]}`,
			after:    `{"input":[` + user + `]}`,
			modified: true,
		},
		{
			name:     "array input",
			before:   `{"instructions":" base prompt ","input":[` + user + `]}`,
			after:    `{"input":[` + developer + `,` + user + `]}`,
			modified: true,
		},
		{
			name:     "skip only leading additional tools",
			before:   `{"instructions":" base prompt ","input":[` + toolsA + `,` + toolsB + `,` + user + `,` + toolsC + `]}`,
			after:    `{"input":[` + toolsA + `,` + toolsB + `,` + developer + `,` + user + `,` + toolsC + `]}`,
			modified: true,
		},
		{
			name:   "preserve turn-scoped instructions when leading developer string exists",
			before: `{"instructions":"only this turn","input":[` + existingDev + `,` + user + `]}`,
			after:  `{"instructions":"only this turn","input":[` + existingDev + `,` + user + `]}`,
		},
		{
			name:   "preserve turn-scoped instructions when leading developer array exists after additional tools",
			before: `{"instructions":"only this turn","input":[` + toolsA + `,` + existingDevArr + `,` + user + `]}`,
			after:  `{"instructions":"only this turn","input":[` + toolsA + `,` + existingDevArr + `,` + user + `]}`,
		},
		{
			name:     "delete blank instructions even when leading developer exists",
			before:   `{"instructions":" \t","input":[` + existingDev + `,` + user + `]}`,
			after:    `{"input":[` + existingDev + `,` + user + `]}`,
			modified: true,
		},
		{
			name:     "blank leading developer does not suppress migration",
			before:   `{"instructions":" base prompt ","input":[` + blankDev + `,` + user + `]}`,
			after:    `{"input":[` + developer + `,` + blankDev + `,` + user + `]}`,
			modified: true,
		},
		{
			name:     "late developer does not suppress migration",
			before:   `{"instructions":" base prompt ","input":[` + user + `,` + existingDev + `]}`,
			after:    `{"input":[` + developer + `,` + user + `,` + existingDev + `]}`,
			modified: true,
		},
		{
			name:     "string input",
			before:   `{"instructions":" base prompt ","input":"hello"}`,
			after:    `{"input":[` + developer + `,` + user + `]}`,
			modified: true,
		},
		{
			name:     "blank string input",
			before:   `{"instructions":" base prompt ","input":" \t\n"}`,
			after:    `{"input":[` + developer + `]}`,
			modified: true,
		},
		{
			name:     "missing input",
			before:   `{"instructions":" base prompt "}`,
			after:    `{"input":[` + developer + `]}`,
			modified: true,
		},
		{
			name:   "object input",
			before: `{"instructions":" base prompt ","input":` + user + `}`,
			after:  `{"instructions":" base prompt ","input":` + user + `}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var body map[string]any
			require.NoError(t, json.Unmarshal([]byte(tt.before), &body))

			require.Equal(t, tt.modified, applyCodexOAuthInstructionsAsDeveloper(body))
			got, err := json.Marshal(body)
			require.NoError(t, err)
			require.JSONEq(t, tt.after, string(got))
		})
	}
}

func TestPrependCodexOAuthInstructionsAsDeveloper(t *testing.T) {
	var body map[string]any
	require.NoError(t, json.Unmarshal([]byte(`{
		"instructions":"forced template",
		"input":[
			{"type":"additional_tools","id":"at_a"},
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"client system"}]},
			{"type":"message","role":"user","content":"hello"}
		]
	}`), &body))

	require.True(t, prependCodexOAuthInstructionsAsDeveloper(body))
	got, err := json.Marshal(body)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(got, "instructions").Exists())
	require.Equal(t, "additional_tools", gjson.GetBytes(got, "input.0.type").String())
	requireCodexDeveloperInstructions(t, got, 1, "forced template")
	require.Equal(t, "client system", gjson.GetBytes(got, "input.2.content.0.text").String())
	require.Equal(t, "user", gjson.GetBytes(got, "input.3.role").String())
}

func TestHasCodexLeadingDeveloperPrefixInRawBody(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want bool
	}{
		{name: "string developer", body: `{"input":[{"role":"developer","content":"rules"},{"role":"user","content":"hi"}]}`, want: true},
		{name: "array input_text developer", body: `{"input":[{"type":"message","role":"developer","content":[{"type":"input_text","text":"rules"}]},{"role":"user","content":"hi"}]}`, want: true},
		{name: "after additional_tools", body: `{"input":[{"type":"additional_tools","tools":[]},{"type":"message","role":"developer","content":"rules"}]}`, want: true},
		{name: "leading system message", body: `{"input":[{"role":"system","content":"rules"},{"role":"user","content":"hi"}]}`, want: true},
		{name: "legacy prompt alias", body: `{"prompt":[{"role":"developer","content":"rules"}]}`, want: true},
		{name: "blank developer", body: `{"input":[{"role":"developer","content":"  \t"},{"role":"user","content":"hi"}]}`, want: false},
		{name: "blank array developer", body: `{"input":[{"role":"developer","content":[{"type":"input_text","text":" "}]}]}`, want: false},
		{name: "late developer", body: `{"input":[{"role":"user","content":"hi"},{"role":"developer","content":"rules"}]}`, want: false},
		{name: "non-message item", body: `{"input":[{"type":"function_call","role":"developer","content":"rules"}]}`, want: false},
		{name: "string input", body: `{"input":"hello"}`, want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, hasCodexLeadingDeveloperPrefixInRawBody([]byte(tt.body)))
		})
	}
}

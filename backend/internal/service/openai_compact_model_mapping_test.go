package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayService_Forward_CompactOnlyModelMappingOverridesOAuthUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-compact-map"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_123","status":"completed","model":"gpt-5.4-openai-compact","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          1,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":          "oauth-token",
			"chatgpt_account_id":    "chatgpt-acc",
			"model_mapping":         map[string]any{"gpt-5.4": "gpt-5.3-codex"},
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.4", result.Model)
	require.Equal(t, "gpt-5.4-openai-compact", result.UpstreamModel)
	require.Equal(t, "gpt-5.4-openai-compact", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, defaultCodexSynthInstructions("gpt-5.4-openai-compact"), gjson.GetBytes(upstream.lastBody, "instructions").String())
	require.Equal(t, "user", gjson.GetBytes(upstream.lastBody, "input.0.role").String())
	opsModel, exists := c.Get(OpsUpstreamModelKey)
	require.True(t, exists)
	require.Equal(t, "gpt-5.4-openai-compact", opsModel)
}

func TestOpenAIGatewayService_Forward_CompactSystemInstructions(t *testing.T) {
	const (
		textOnly     = `{"role":"system","content":[{"type":"input_text","text":"compact "},{"type":"input_text","text":"JSON"}]}`
		mixedContent = `[{"type":"input_text","text":"keep diagrams"},{"type":"input_image","image_url":"https://example.org/chart.png"}]`
		user         = `{"role":"user","content":"summarize"}`
	)
	for _, tc := range []struct {
		name, fields, input, wantInstructions, wantInput string
	}{
		{
			name:             "promote text and retain mixed content",
			fields:           `"instructions":"keep filenames",`,
			input:            textOnly + `,{"role":"system","content":` + mixedContent + `},` + user,
			wantInstructions: "compact JSON\n\nkeep diagrams\n\nkeep filenames",
			wantInput:        `[{"role":"developer","content":` + mixedContent + `},` + user + `]`,
		},
		{
			name:             "retain JSON mode guidance in history",
			fields:           `"text":{"format":{"type":"json_object"}},"instructions":"keep filenames",`,
			input:            textOnly + `,` + user,
			wantInstructions: "compact JSON\n\nkeep filenames",
			wantInput:        `[` + strings.Replace(textOnly, `"system"`, `"developer"`, 1) + `,` + user + `]`,
		},
		{
			name:      "bridge keeps empty instructions",
			fields:    `"prompt_cache_key":"anthropic-metadata-compact",`,
			input:     user,
			wantInput: `[` + user + `]`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(`{"model":"gpt-5.4",` + tc.fields + `"input":[` + tc.input + `]}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
			upstream := &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}},
				Body: io.NopCloser(strings.NewReader(`{"id":"resp_compact","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
			}}
			svc := &OpenAIGatewayService{httpUpstream: upstream}
			_, err := svc.Forward(context.Background(), c, instructionsEchoOAuthAccount(false), body)
			require.NoError(t, err)
			instructions := gjson.GetBytes(upstream.lastBody, "instructions")
			require.Equal(t, gjson.String, instructions.Type)
			require.Equal(t, tc.wantInstructions, instructions.String())
			require.JSONEq(t, tc.wantInput, gjson.GetBytes(upstream.lastBody, "input").Raw)
		})
	}
}

func TestOpenAIGatewayService_Forward_APIKeyCompactSanitizesStatelessReplayAfterStoreWasDropped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.5","parallel_tool_calls":true,"input":[` +
		`{"type":"reasoning","id":"rs_server_only","summary":[]},` +
		`{"type":"item_reference","id":"rs_server_only"},` +
		`{"type":"message","role":"user","content":"compact this"}` +
		`]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_compact","status":"completed","output":[{"type":"compaction","encrypted_content":"cipher"}],"usage":{"input_tokens":1,"output_tokens":1}}`,
		)),
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		cfg: &config.Config{Security: config.SecurityConfig{
			URLAllowlist: config.URLAllowlistConfig{Enabled: false},
		}},
	}
	account := &Account{
		ID: 7, Name: "azure-openai", Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"api_key": "test-key"}, Status: StatusActive, Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, gjson.GetBytes(upstream.lastBody, "parallel_tool_calls").Exists())
	require.Equal(t, int64(1), gjson.GetBytes(upstream.lastBody, "input.#").Int())
	require.Equal(t, "message", gjson.GetBytes(upstream.lastBody, "input.0.type").String())
}

func TestOpenAIGatewayService_Forward_NormalizesCompactionTriggerAfterHistoryCleanup(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","stream":false,"input":[{"type":"compaction_trigger"},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{}"},{"type":"function_call_output","call_id":"call_1","output":"ok"},{"type":"message","role":"user","content":"visible"}]}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_trigger","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}
	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID: 4, Name: "openai-oauth", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-acc"},
		Status:      StatusActive, Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	items := gjson.GetBytes(upstream.lastBody, "input").Array()
	require.Len(t, items, 5)
	require.Equal(t, "function_call", items[1].Get("type").String())
	require.Equal(t, "function_call_output", items[2].Get("type").String())
	require.Equal(t, "message", items[3].Get("type").String())
	require.Equal(t, "compaction_trigger", items[4].Get("type").String())
}

func TestOpenAIGatewayService_Forward_NonCompactRequestIgnoresCompactOnlyModelMapping(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := []byte(`{"model":"gpt-5.4","stream":false,"instructions":"normal-test","input":"hello"}`)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-normal-map"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"resp_124","status":"completed","model":"gpt-5.4","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          2,
		Name:        "openai-oauth",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":          "oauth-token",
			"chatgpt_account_id":    "chatgpt-acc",
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.4", result.Model)
	require.Equal(t, "gpt-5.4", result.UpstreamModel)
	require.Equal(t, "gpt-5.4", gjson.GetBytes(upstream.lastBody, "model").String())
}

func TestOpenAIGatewayService_OAuthPassthrough_CompactOnlyModelMappingOverridesUpstreamModel(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", bytes.NewReader(nil))
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.1.0")
	c.Request.Header.Set("Content-Type", "application/json")

	originalBody := []byte(`{"model":"gpt-5.4","stream":true,"store":true,"instructions":"compact-pass","input":[{"type":"text","text":"compact me"}]}`)
	upstream := &httpUpstreamRecorder{resp: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}, "x-request-id": []string{"rid-compact-pass-map"}},
		Body:       io.NopCloser(strings.NewReader(`{"id":"cmp_124","model":"gpt-5.4-openai-compact","usage":{"input_tokens":2,"output_tokens":3}}`)),
	}}

	svc := &OpenAIGatewayService{httpUpstream: upstream}
	account := &Account{
		ID:          3,
		Name:        "openai-oauth-pass",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token":          "oauth-token",
			"chatgpt_account_id":    "chatgpt-acc",
			"compact_model_mapping": map[string]any{"gpt-5.4": "gpt-5.4-openai-compact"},
		},
		Extra:       map[string]any{"openai_passthrough": true},
		Status:      StatusActive,
		Schedulable: true,
	}

	result, err := svc.Forward(context.Background(), c, account, originalBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, "gpt-5.4", result.Model)
	require.Equal(t, "gpt-5.4-openai-compact", result.UpstreamModel)
	require.Equal(t, "gpt-5.4-openai-compact", gjson.GetBytes(upstream.lastBody, "model").String())
	require.Equal(t, "gpt-5.4", gjson.GetBytes(rec.Body.Bytes(), "model").String())
	opsModel, exists := c.Get(OpsUpstreamModelKey)
	require.True(t, exists)
	require.Equal(t, "gpt-5.4-openai-compact", opsModel)
}

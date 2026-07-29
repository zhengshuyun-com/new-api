package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadPromptAuditConfigFromEnv(t *testing.T) {
	t.Setenv("PROMPT_AUDIT_ENDPOINT_URL", " https://audit.example.com/check ")
	t.Setenv("PROMPT_AUDIT_SECRET", "secret")
	t.Setenv("PROMPT_AUDIT_WAIT_MS", "800")
	t.Setenv("PROMPT_AUDIT_QUEUE_SIZE", "32")
	t.Setenv("PROMPT_AUDIT_WORKER_COUNT", "2")

	cfg := loadPromptAuditConfig()
	if !isPromptAuditConfigEnabled(cfg) {
		t.Fatal("expected prompt audit to be enabled")
	}
	if cfg.EndpointURL != "https://audit.example.com/check" {
		t.Fatalf("unexpected endpoint: %q", cfg.EndpointURL)
	}
	if cfg.Secret != "secret" || cfg.WaitMS != 800 || cfg.TimeoutMS != defaultPromptAuditTimeout || cfg.QueueSize != 32 || cfg.WorkerCount != 2 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadPromptAuditConfigDefaultsToDisabled(t *testing.T) {
	t.Setenv("PROMPT_AUDIT_WAIT_MS", "")
	t.Setenv("PROMPT_AUDIT_ENDPOINT_URL", "")
	t.Setenv("PROMPT_AUDIT_QUEUE_SIZE", "")
	t.Setenv("PROMPT_AUDIT_WORKER_COUNT", "")

	cfg := loadPromptAuditConfig()
	if cfg.WaitMS != defaultPromptAuditWaitMS {
		t.Fatalf("unexpected default wait ms: %d", cfg.WaitMS)
	}
	if isPromptAuditConfigEnabled(cfg) {
		t.Fatal("expected prompt audit to be disabled by default")
	}
	assert.Equal(t, defaultPromptAuditQueue, cfg.QueueSize)
	assert.Equal(t, defaultPromptAuditWorkers, cfg.WorkerCount)
}

func TestValidatePromptAuditEndpointURL(t *testing.T) {
	tests := []struct {
		name        string
		endpointURL string
		wantErr     bool
	}{
		{
			name:        "http",
			endpointURL: "http://127.0.0.1:8080/test/prompt-audit",
		},
		{
			name:        "https",
			endpointURL: "https://audit.example.com/check?token=secret",
		},
		{
			name:        "empty",
			endpointURL: "",
			wantErr:     true,
		},
		{
			name:        "unsupported scheme",
			endpointURL: "ftp://audit.example.com/check",
			wantErr:     true,
		},
		{
			name:        "missing host",
			endpointURL: "http:///check",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePromptAuditEndpointURL(tt.endpointURL)
			if tt.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestMaskPromptAuditEndpoint(t *testing.T) {
	maskedEndpoint := maskPromptAuditEndpoint("https://audit.example.com/check?token=secret")
	if strings.Contains(maskedEndpoint, "audit.example.com") || strings.Contains(maskedEndpoint, "secret") {
		t.Fatalf("endpoint was not masked: %s", maskedEndpoint)
	}
}

func TestBuildPromptAuditPayloadPreservesFullText(t *testing.T) {
	info := &relaycommon.RelayInfo{
		RequestId:       "req-1",
		RequestURLPath:  "/v1/chat/completions",
		RelayFormat:     types.RelayFormatOpenAI,
		RelayMode:       2,
		OriginModelName: "gpt-test",
		UserId:          11,
		UserEmail:       "user@example.com",
		UserGroup:       "default",
		UsingGroup:      "default",
		TokenId:         22,
		TokenGroup:      "default",
		IsStream:        true,
	}
	meta := &types.TokenCountMeta{CombineText: strings.Repeat("a", 1048577)}

	payload, ok := buildPromptAuditPayload(nil, info, &dto.BaseRequest{}, meta)
	require.True(t, ok)
	assert.Equal(t, meta.CombineText, payload.Prompt.Text)
	assert.Equal(t, len(meta.CombineText), payload.Prompt.TextBytes)
	assert.Equal(t, "req-1", payload.Request.RequestID)
	assert.True(t, payload.Request.Stream)
	assert.Equal(t, 11, payload.User.ID)
	assert.Equal(t, 22, payload.Token.ID)
}

func TestEnqueuePromptAuditDropsWhenQueueFull(t *testing.T) {
	oldCfg := promptAuditCfg
	oldQueue := promptAuditQueue
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
		promptAuditQueue = oldQueue
	})

	promptAuditCfg = promptAuditConfig{
		EndpointURL: "http://127.0.0.1/audit",
		WaitMS:      0,
	}
	promptAuditQueue = make(chan promptAuditJob, 1)
	promptAuditQueue <- promptAuditJob{}

	done := make(chan struct{})
	go func() {
		EnqueuePromptAudit(nil, &relaycommon.RelayInfo{RequestId: "req-full"}, &dto.BaseRequest{}, &types.TokenCountMeta{CombineText: "hello"})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("enqueue blocked when queue was full")
	}
}

func TestSendPromptAuditPostsPayload(t *testing.T) {
	oldCfg := promptAuditCfg
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
	})

	var got promptAuditPayload
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected content type: %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("X-NewAPI-Audit-Version") != promptAuditVersion {
			t.Errorf("unexpected audit version header: %s", r.Header.Get("X-NewAPI-Audit-Version"))
		}
		if !strings.HasPrefix(r.Header.Get("X-NewAPI-Audit-Signature"), "sha256=") {
			t.Errorf("missing signature header")
		}
		if err := common.DecodeJson(r.Body, &got); err != nil {
			t.Errorf("decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	promptAuditCfg = promptAuditConfig{
		EndpointURL: server.URL,
		Secret:      "secret",
		WaitMS:      0,
		TimeoutMS:   1000,
	}

	payload := promptAuditPayload{
		Version: promptAuditVersion,
		EventID: "req-post",
		SentAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Source:  "new-api",
		Request: promptAuditRequest{RequestID: "req-post"},
		Prompt:  promptAuditPrompt{Text: "hello", TextBytes: 5},
	}

	if err := sendPromptAudit(payload); err != nil {
		t.Fatalf("send prompt audit: %v", err)
	}
	if got.EventID != payload.EventID || got.Prompt.Text != payload.Prompt.Text {
		t.Fatalf("unexpected posted payload: %+v", got)
	}
}

func TestAuditPromptSyncRejectsOnExplicitReject(t *testing.T) {
	oldCfg := promptAuditCfg
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":"SUCCESS","data":{"action":"REJECT","reason":"blocked"}}`))
	}))
	defer server.Close()

	promptAuditCfg = promptAuditConfig{
		EndpointURL: server.URL,
		WaitMS:      1000,
	}

	err := AuditPrompt(nil, &relaycommon.RelayInfo{RequestId: "req-reject"}, &dto.BaseRequest{}, &types.TokenCountMeta{CombineText: "hello"})
	if !errors.Is(err, errPromptAuditRejected) {
		t.Fatalf("expected prompt audit rejection, got %v", err)
	}
}

func TestParsePromptAuditRejected(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		wantRejected bool
		wantValid    bool
	}{
		{
			name:         "explicit reject",
			body:         `{"code":"SUCCESS","data":{"action":"REJECT"}}`,
			wantRejected: true,
			wantValid:    true,
		},
		{
			name:      "allow",
			body:      `{"code":"SUCCESS","data":{"action":"ALLOW"}}`,
			wantValid: true,
		},
		{
			name:      "empty action",
			body:      `{"code":"SUCCESS","data":{"action":""}}`,
			wantValid: true,
		},
		{
			name:      "unknown action",
			body:      `{"code":"SUCCESS","data":{"action":"REVIEW"}}`,
			wantValid: true,
		},
		{
			name:      "lowercase reject",
			body:      `{"code":"SUCCESS","data":{"action":"reject"}}`,
			wantValid: true,
		},
		{
			name:      "missing data",
			body:      `{"code":"SUCCESS"}`,
			wantValid: true,
		},
		{
			name: "non-success with reject",
			body: `{"code":"INTERNAL_ERROR","data":{"action":"REJECT"}}`,
		},
		{
			name: "invalid json",
			body: `{`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejected, valid := parsePromptAuditRejected([]byte(tt.body))
			assert.Equal(t, tt.wantRejected, rejected)
			assert.Equal(t, tt.wantValid, valid)
		})
	}
}

func TestAuditPromptSyncDowngradesOnNonOKStatus(t *testing.T) {
	oldCfg := promptAuditCfg
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	promptAuditCfg = promptAuditConfig{
		EndpointURL: server.URL,
		WaitMS:      1000,
	}

	err := AuditPrompt(nil, &relaycommon.RelayInfo{RequestId: "req-non-ok"}, &dto.BaseRequest{}, &types.TokenCountMeta{CombineText: "hello"})
	if err != nil {
		t.Fatalf("expected prompt audit non-200 status to downgrade, got %v", err)
	}
}

func TestAuditPromptSyncDowngradesOnServerFailure(t *testing.T) {
	oldCfg := promptAuditCfg
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	promptAuditCfg = promptAuditConfig{
		EndpointURL: server.URL,
		WaitMS:      1000,
	}

	err := AuditPrompt(nil, &relaycommon.RelayInfo{RequestId: "req-downgrade"}, &dto.BaseRequest{}, &types.TokenCountMeta{CombineText: "hello"})
	if err != nil {
		t.Fatalf("expected prompt audit server failure to downgrade, got %v", err)
	}
}

func TestAuditPromptSyncDowngradesOnTimeout(t *testing.T) {
	oldCfg := promptAuditCfg
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
	})

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	promptAuditCfg = promptAuditConfig{
		EndpointURL: server.URL,
		WaitMS:      10,
	}

	err := AuditPrompt(nil, &relaycommon.RelayInfo{RequestId: "req-timeout"}, &dto.BaseRequest{}, &types.TokenCountMeta{CombineText: "hello"})
	if err != nil {
		t.Fatalf("expected prompt audit timeout to downgrade, got %v", err)
	}
}

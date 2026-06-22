package service

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
)

func TestLoadPromptAuditConfigFromEnv(t *testing.T) {
	t.Setenv("PROMPT_AUDIT_ENABLED", "true")
	t.Setenv("PROMPT_AUDIT_ENDPOINT_URL", " https://audit.example.com/check ")
	t.Setenv("PROMPT_AUDIT_SECRET", "secret")
	t.Setenv("PROMPT_AUDIT_TIMEOUT_MS", "1200")
	t.Setenv("PROMPT_AUDIT_QUEUE_SIZE", "32")
	t.Setenv("PROMPT_AUDIT_WORKER_COUNT", "2")
	t.Setenv("PROMPT_AUDIT_MAX_TEXT_BYTES", "4096")

	cfg := loadPromptAuditConfig()
	if !cfg.Enabled {
		t.Fatal("expected prompt audit to be enabled")
	}
	if cfg.EndpointURL != "https://audit.example.com/check" {
		t.Fatalf("unexpected endpoint: %q", cfg.EndpointURL)
	}
	if cfg.Secret != "secret" || cfg.TimeoutMS != 1200 || cfg.QueueSize != 32 || cfg.WorkerCount != 2 || cfg.MaxTextBytes != 4096 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
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

func TestBuildPromptAuditPayloadTruncatesByUTF8Bytes(t *testing.T) {
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
	meta := &types.TokenCountMeta{CombineText: "你好hello"}

	payload, ok := buildPromptAuditPayload(nil, info, &dto.BaseRequest{}, meta, promptAuditConfig{MaxTextBytes: 7})
	if !ok {
		t.Fatal("expected payload")
	}
	if payload.Prompt.Text != "你好h" {
		t.Fatalf("unexpected truncated text: %q", payload.Prompt.Text)
	}
	if !payload.Prompt.Truncated {
		t.Fatal("expected truncated flag")
	}
	if payload.Prompt.TextBytes != len([]byte(payload.Prompt.Text)) {
		t.Fatal("unexpected text byte count")
	}
	if payload.Request.RequestID != "req-1" || !payload.Request.Stream || payload.User.ID != 11 || payload.Token.ID != 22 {
		t.Fatalf("unexpected payload metadata: %+v", payload)
	}
}

func TestEnqueuePromptAuditDropsWhenQueueFull(t *testing.T) {
	oldCfg := promptAuditCfg
	oldQueue := promptAuditQueue
	t.Cleanup(func() {
		promptAuditCfg = oldCfg
		promptAuditQueue = oldQueue
	})

	promptAuditCfg = promptAuditConfig{
		Enabled:      true,
		EndpointURL:  "http://127.0.0.1/audit",
		MaxTextBytes: defaultPromptAuditMaxText,
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
		Enabled:     true,
		EndpointURL: server.URL,
		Secret:      "secret",
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

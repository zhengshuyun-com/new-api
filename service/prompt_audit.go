package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

const (
	promptAuditVersion        = "prompt_audit.v1"
	defaultPromptAuditTimeout = 3000
	defaultPromptAuditQueue   = 3000
	defaultPromptAuditWorkers = 8
	defaultPromptAuditMaxText = 1048576
)

type promptAuditConfig struct {
	Enabled      bool
	EndpointURL  string
	Secret       string
	TimeoutMS    int
	QueueSize    int
	WorkerCount  int
	MaxTextBytes int
}

type promptAuditPayload struct {
	Version string             `json:"version"`
	EventID string             `json:"event_id"`
	SentAt  string             `json:"sent_at"`
	Source  string             `json:"source"`
	Request promptAuditRequest `json:"request"`
	User    promptAuditUser    `json:"user"`
	Token   promptAuditToken   `json:"token"`
	Prompt  promptAuditPrompt  `json:"prompt"`
}

type promptAuditRequest struct {
	RequestID   string `json:"request_id"`
	Path        string `json:"path"`
	RelayFormat string `json:"relay_format"`
	RelayMode   int    `json:"relay_mode"`
	Model       string `json:"model"`
	Stream      bool   `json:"stream"`
}

type promptAuditUser struct {
	ID         int    `json:"id"`
	Email      string `json:"email,omitempty"`
	Group      string `json:"group,omitempty"`
	UsingGroup string `json:"using_group,omitempty"`
}

type promptAuditToken struct {
	ID    int    `json:"id"`
	Group string `json:"group,omitempty"`
}

type promptAuditPrompt struct {
	Text      string `json:"text"`
	TextBytes int    `json:"text_bytes"`
	Truncated bool   `json:"truncated"`
}

type promptAuditJob struct {
	payload promptAuditPayload
}

var (
	promptAuditCfg   promptAuditConfig
	promptAuditQueue chan promptAuditJob
)

func InitPromptAudit() {
	cfg := loadPromptAuditConfig()
	promptAuditCfg = cfg
	if !cfg.Enabled {
		return
	}
	if err := validatePromptAuditEndpointURL(cfg.EndpointURL); err != nil {
		common.FatalLog("invalid prompt audit config: " + err.Error())
	}

	promptAuditQueue = make(chan promptAuditJob, cfg.QueueSize)
	for i := 0; i < cfg.WorkerCount; i++ {
		go promptAuditWorker(i)
	}
	common.SysLog(fmt.Sprintf("prompt audit enabled, endpoint: %s, workers: %d, queue: %d", maskPromptAuditEndpoint(cfg.EndpointURL), cfg.WorkerCount, cfg.QueueSize))
}

func PromptAuditEnabled() bool {
	return isPromptAuditConfigEnabled(promptAuditCfg)
}

func EnqueuePromptAudit(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request, meta *types.TokenCountMeta) {
	if !PromptAuditEnabled() || promptAuditQueue == nil || info == nil || meta == nil {
		return
	}

	payload, ok := buildPromptAuditPayload(c, info, request, meta, promptAuditCfg)
	if !ok {
		return
	}

	select {
	case promptAuditQueue <- promptAuditJob{payload: payload}:
	default:
		logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit queue full, dropped event request_id=%s", payload.Request.RequestID))
	}
}

func loadPromptAuditConfig() promptAuditConfig {
	cfg := promptAuditConfig{
		Enabled:      common.GetEnvOrDefaultBool("PROMPT_AUDIT_ENABLED", false),
		EndpointURL:  strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_ENDPOINT_URL", "")),
		Secret:       common.GetEnvOrDefaultString("PROMPT_AUDIT_SECRET", ""),
		TimeoutMS:    common.GetEnvOrDefault("PROMPT_AUDIT_TIMEOUT_MS", defaultPromptAuditTimeout),
		QueueSize:    common.GetEnvOrDefault("PROMPT_AUDIT_QUEUE_SIZE", defaultPromptAuditQueue),
		WorkerCount:  common.GetEnvOrDefault("PROMPT_AUDIT_WORKER_COUNT", defaultPromptAuditWorkers),
		MaxTextBytes: common.GetEnvOrDefault("PROMPT_AUDIT_MAX_TEXT_BYTES", defaultPromptAuditMaxText),
	}
	if cfg.TimeoutMS <= 0 {
		cfg.TimeoutMS = defaultPromptAuditTimeout
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultPromptAuditQueue
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = defaultPromptAuditWorkers
	}
	if cfg.MaxTextBytes <= 0 {
		cfg.MaxTextBytes = defaultPromptAuditMaxText
	}
	return cfg
}

func isPromptAuditConfigEnabled(cfg promptAuditConfig) bool {
	return cfg.Enabled && strings.TrimSpace(cfg.EndpointURL) != ""
}

func validatePromptAuditEndpointURL(endpointURL string) error {
	if strings.TrimSpace(endpointURL) == "" {
		return fmt.Errorf("PROMPT_AUDIT_ENDPOINT_URL is required when PROMPT_AUDIT_ENABLED=true")
	}
	parsedURL, err := url.Parse(endpointURL)
	if err != nil {
		return fmt.Errorf("PROMPT_AUDIT_ENDPOINT_URL format is invalid")
	}
	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return fmt.Errorf("PROMPT_AUDIT_ENDPOINT_URL scheme must be http or https")
	}
	if parsedURL.Host == "" {
		return fmt.Errorf("PROMPT_AUDIT_ENDPOINT_URL host is required")
	}
	return nil
}

func maskPromptAuditEndpoint(endpointURL string) string {
	return common.MaskSensitiveInfo(endpointURL)
}

func buildPromptAuditPayload(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request, meta *types.TokenCountMeta, cfg promptAuditConfig) (promptAuditPayload, bool) {
	text := ""
	if meta != nil {
		text = meta.CombineText
	}
	if strings.TrimSpace(text) == "" {
		return promptAuditPayload{}, false
	}

	text, truncated := truncateTextByBytes(text, cfg.MaxTextBytes)
	requestID := info.RequestId
	if requestID == "" && c != nil {
		requestID = c.GetString(common.RequestIdKey)
	}
	path := info.RequestURLPath
	if c != nil && c.Request != nil && c.Request.URL != nil {
		path = c.Request.URL.Path
	}

	return promptAuditPayload{
		Version: promptAuditVersion,
		EventID: requestID,
		SentAt:  time.Now().UTC().Format(time.RFC3339Nano),
		Source:  "new-api",
		Request: promptAuditRequest{
			RequestID:   requestID,
			Path:        path,
			RelayFormat: string(info.RelayFormat),
			RelayMode:   info.RelayMode,
			Model:       info.OriginModelName,
			Stream:      isPromptAuditStream(c, info, request),
		},
		User: promptAuditUser{
			ID:         info.UserId,
			Email:      info.UserEmail,
			Group:      info.UserGroup,
			UsingGroup: info.UsingGroup,
		},
		Token: promptAuditToken{
			ID:    info.TokenId,
			Group: info.TokenGroup,
		},
		Prompt: promptAuditPrompt{
			Text:      text,
			TextBytes: len([]byte(text)),
			Truncated: truncated,
		},
	}, true
}

func isPromptAuditStream(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) bool {
	if info != nil {
		return info.IsStream
	}
	if request != nil {
		return request.IsStream(c)
	}
	return false
}

func truncateTextByBytes(text string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", text != ""
	}
	if len([]byte(text)) <= maxBytes {
		return text, false
	}

	var builder strings.Builder
	builder.Grow(maxBytes)
	written := 0
	for _, r := range text {
		runeLen := utf8.RuneLen(r)
		if runeLen < 0 {
			runeLen = len(string(r))
		}
		if written+runeLen > maxBytes {
			break
		}
		builder.WriteRune(r)
		written += runeLen
	}
	return builder.String(), true
}

func promptAuditWorker(workerID int) {
	for job := range promptAuditQueue {
		if err := sendPromptAudit(job.payload); err != nil {
			logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit send failed worker=%d request_id=%s error=%s", workerID, job.payload.Request.RequestID, common.MaskSensitiveInfo(err.Error())))
		}
	}
}

func sendPromptAudit(payload promptAuditPayload) error {
	cfg := promptAuditCfg
	if !isPromptAuditConfigEnabled(cfg) {
		return nil
	}

	body, err := common.Marshal(payload)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(cfg.TimeoutMS)*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.EndpointURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-NewAPI-Audit-Version", promptAuditVersion)
	req.Header.Set("X-NewAPI-Request-ID", payload.Request.RequestID)
	req.Header.Set("X-NewAPI-Audit-Event-ID", payload.EventID)
	if cfg.Secret != "" {
		timestamp := fmt.Sprintf("%d", time.Now().Unix())
		req.Header.Set("X-NewAPI-Audit-Timestamp", timestamp)
		req.Header.Set("X-NewAPI-Audit-Signature", signPromptAuditBody(cfg.Secret, timestamp, body))
	}

	client := GetHttpClient()
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status code %d", resp.StatusCode)
	}
	return nil
}

func signPromptAuditBody(secret string, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

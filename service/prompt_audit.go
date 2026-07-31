package service

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"

	"github.com/gin-gonic/gin"
)

const (
	promptAuditVersion        = "v1"
	promptAuditSuccessCode    = "SUCCESS"
	promptAuditRejectAction   = "REJECT"
	defaultPromptAuditTimeout = 3000
	defaultPromptAuditQueue   = 128
	defaultPromptAuditWorkers = 8
	defaultPromptAuditWaitMS  = -1
	promptAuditResponseLimit  = 4096
)

var errPromptAuditRejected = errors.New("prompt audit rejected")

type promptAuditConfig struct {
	EndpointURL string
	Secret      string
	WaitMS      int
	TimeoutMS   int
	QueueSize   int
	WorkerCount int
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
}

type promptAuditServiceResponse struct {
	Code string                       `json:"code"`
	Data *promptAuditDecisionResponse `json:"data,omitempty"`
}

type promptAuditDecisionResponse struct {
	Action string `json:"action"`
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
	if !shouldPromptAudit(cfg) {
		return
	}
	if err := validatePromptAuditEndpointURL(cfg.EndpointURL); err != nil {
		common.FatalLog("invalid prompt audit config: " + err.Error())
	}

	if cfg.WaitMS == 0 {
		promptAuditQueue = make(chan promptAuditJob, cfg.QueueSize)
		for i := 0; i < cfg.WorkerCount; i++ {
			go promptAuditWorker(i)
		}
		common.SysLog(fmt.Sprintf("prompt audit enabled, mode: async, endpoint: %s, workers: %d, queue: %d", maskPromptAuditEndpoint(cfg.EndpointURL), cfg.WorkerCount, cfg.QueueSize))
		return
	}
	common.SysLog(fmt.Sprintf("prompt audit enabled, mode: sync, wait_ms: %d, endpoint: %s", cfg.WaitMS, maskPromptAuditEndpoint(cfg.EndpointURL)))
}

func PromptAuditEnabled() bool {
	return isPromptAuditConfigEnabled(promptAuditCfg)
}

func EnqueuePromptAudit(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request, meta *types.TokenCountMeta) {
	if !PromptAuditEnabled() || promptAuditQueue == nil || info == nil || meta == nil {
		return
	}

	payload, ok := buildPromptAuditPayload(c, info, request, meta)
	if !ok {
		return
	}

	select {
	case promptAuditQueue <- promptAuditJob{payload: payload}:
	default:
		logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit queue full, dropped event request_id=%s", payload.Request.RequestID))
	}
}

func AuditPrompt(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request, meta *types.TokenCountMeta) error {
	cfg := promptAuditCfg
	if !isPromptAuditConfigEnabled(cfg) || info == nil || meta == nil {
		return nil
	}
	if cfg.WaitMS == 0 {
		EnqueuePromptAudit(c, info, request, meta)
		return nil
	}

	payload, ok := buildPromptAuditPayload(c, info, request, meta)
	if !ok {
		return nil
	}
	statusCode, responseBody, err := doPromptAudit(payload, cfg.WaitMS)
	if err != nil {
		logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit sync failed, downgraded request_id=%s error=%s", payload.Request.RequestID, common.MaskSensitiveInfo(err.Error())))
		return nil
	}
	if statusCode != http.StatusOK {
		logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit sync returned status_code=%d, downgraded request_id=%s", statusCode, payload.Request.RequestID))
		return nil
	}

	rejected, ok := parsePromptAuditRejected(responseBody)
	if !ok {
		logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit sync response invalid, downgraded request_id=%s", payload.Request.RequestID))
		return nil
	}
	if rejected {
		logger.LogWarn(context.Background(), fmt.Sprintf("prompt audit rejected request_id=%s", payload.Request.RequestID))
		return errPromptAuditRejected
	}
	return nil
}

func loadPromptAuditConfig() promptAuditConfig {
	cfg := promptAuditConfig{
		EndpointURL: strings.TrimSpace(common.GetEnvOrDefaultString("PROMPT_AUDIT_ENDPOINT_URL", "")),
		Secret:      common.GetEnvOrDefaultString("PROMPT_AUDIT_SECRET", ""),
		WaitMS:      common.GetEnvOrDefault("PROMPT_AUDIT_WAIT_MS", defaultPromptAuditWaitMS),
		TimeoutMS:   defaultPromptAuditTimeout,
		QueueSize:   common.GetEnvOrDefault("PROMPT_AUDIT_QUEUE_SIZE", defaultPromptAuditQueue),
		WorkerCount: common.GetEnvOrDefault("PROMPT_AUDIT_WORKER_COUNT", defaultPromptAuditWorkers),
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultPromptAuditQueue
	}
	if cfg.WorkerCount <= 0 {
		cfg.WorkerCount = defaultPromptAuditWorkers
	}
	return cfg
}

func isPromptAuditConfigEnabled(cfg promptAuditConfig) bool {
	return shouldPromptAudit(cfg) && strings.TrimSpace(cfg.EndpointURL) != ""
}

func shouldPromptAudit(cfg promptAuditConfig) bool {
	return cfg.WaitMS >= 0
}

func validatePromptAuditEndpointURL(endpointURL string) error {
	if strings.TrimSpace(endpointURL) == "" {
		return fmt.Errorf("PROMPT_AUDIT_ENDPOINT_URL is required when PROMPT_AUDIT_WAIT_MS >= 0")
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

func buildPromptAuditPayload(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request, meta *types.TokenCountMeta) (promptAuditPayload, bool) {
	text := ""
	if meta != nil {
		text = meta.CombineText
	}
	if strings.TrimSpace(text) == "" {
		return promptAuditPayload{}, false
	}

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
			TextBytes: len(text),
		},
	}, true
}

func isPromptAuditStream(c *gin.Context, info *relaycommon.RelayInfo, request dto.Request) bool {
	if info != nil {
		return info.IsStream
	}
	if request != nil {
		if c == nil {
			return request.IsStream(nil)
		}
		return request.IsStream(c.Request)
	}
	return false
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

	statusCode, _, err := doPromptAudit(payload, cfg.TimeoutMS)
	if err != nil {
		return err
	}
	if statusCode < http.StatusOK || statusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("status code %d", statusCode)
	}
	return nil
}

func doPromptAudit(payload promptAuditPayload, timeoutMS int) (int, []byte, error) {
	cfg := promptAuditCfg
	if !isPromptAuditConfigEnabled(cfg) {
		return http.StatusOK, nil, nil
	}
	if timeoutMS <= 0 {
		timeoutMS = defaultPromptAuditTimeout
	}

	body, err := common.Marshal(payload)
	if err != nil {
		return 0, nil, err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMS)*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.EndpointURL, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
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
		return 0, nil, err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, promptAuditResponseLimit))
	if err != nil {
		return resp.StatusCode, nil, err
	}

	return resp.StatusCode, responseBody, nil
}

func parsePromptAuditRejected(responseBody []byte) (bool, bool) {
	var auditResponse promptAuditServiceResponse
	if err := common.Unmarshal(responseBody, &auditResponse); err != nil {
		return false, false
	}
	if auditResponse.Code != promptAuditSuccessCode {
		return false, false
	}
	if auditResponse.Data == nil {
		return false, true
	}
	return auditResponse.Data.Action == promptAuditRejectAction, true
}

func signPromptAuditBody(secret string, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

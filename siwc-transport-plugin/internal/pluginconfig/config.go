// Package pluginconfig 定义 SIWC 传输插件的配置：结构、默认值、严格校验与规范化。
package pluginconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
)

const (
	defaultDirectBaseURL             = "https://api.openai.com"
	defaultUserAgent                 = "sub2api-siwc-transport/0.1.0"
	defaultDialTimeoutSeconds        = 30
	defaultResponseHeaderTimeoutSecs = 300
	defaultAccountDirectoryTTLSecs   = 30
	// Matches the host's gateway.max_body_size default: a lower limit rejects
	// image-heavy Responses requests the host has already accepted.
	defaultMaxBufferedBodyBytes = 256 << 20
	// Concurrent requests one upstream connection carries before the account
	// opens another connection; it does not cap an account's concurrency.
	defaultMaxStreamsPerConnection = 16
	maxStreamsPerConnectionCeiling = 100
)

// Config 是插件的全部可配置项（JSON 字段统一 snake_case）。
type Config struct {
	// DirectBaseURL 是 SIWC 直连路由的 base（文档固定为 https://api.openai.com，
	// 仅排障/私有网关时才需要改写）。
	DirectBaseURL string `json:"direct_base_url"`
	// UserAgent 是 SIWC 出站请求的 UA。
	UserAgent string `json:"user_agent"`
	// ForceStoreFalse 对 /responses 请求强制 store=false（SIWC 必需）。
	ForceStoreFalse bool `json:"force_store_false"`
	// ForceStream 对 /responses 请求强制 stream=true（SIWC 必需）。
	ForceStream bool `json:"force_stream"`
	// RewriteChatGPTCodexURLs 把 chatgpt.com/backend-api/codex/* 改写为
	// DirectBaseURL/v1/*。关闭后 SIWC 账号的 codex 形 URL 将原样发出（必然 404）。
	RewriteChatGPTCodexURLs bool `json:"rewrite_chatgpt_codex_urls"`
	// DialTimeoutSeconds 建连（含代理 CONNECT）超时。
	DialTimeoutSeconds int `json:"dial_timeout_seconds"`
	// ResponseHeaderTimeoutSeconds 等待上游响应头的超时（收到响应头后不限制流时长）。
	ResponseHeaderTimeoutSeconds int `json:"response_header_timeout_seconds"`
	// AccountDirectoryTTLSeconds 宿主账号目录（SIWC 判定缓存）的刷新间隔。
	AccountDirectoryTTLSeconds int `json:"account_directory_ttl_seconds"`
	// MaxBufferedBodyBytes 单个请求体的缓冲上限（宿主在发送侧同样全量缓冲）。
	MaxBufferedBodyBytes int64 `json:"max_buffered_body_bytes"`
	// MaxStreamsPerConnection is how many concurrent requests one upstream
	// connection of an account carries before another connection is opened.
	MaxStreamsPerConnection int `json:"max_streams_per_connection"`
}

// Default 返回带全部默认值的配置。
func Default() Config {
	return Config{
		DirectBaseURL:                defaultDirectBaseURL,
		UserAgent:                    defaultUserAgent,
		ForceStoreFalse:              true,
		ForceStream:                  true,
		RewriteChatGPTCodexURLs:      true,
		DialTimeoutSeconds:           defaultDialTimeoutSeconds,
		ResponseHeaderTimeoutSeconds: defaultResponseHeaderTimeoutSecs,
		AccountDirectoryTTLSeconds:   defaultAccountDirectoryTTLSecs,
		MaxBufferedBodyBytes:         defaultMaxBufferedBodyBytes,
		MaxStreamsPerConnection:      defaultMaxStreamsPerConnection,
	}
}

// Validate 严格解析并规范化一段配置 JSON：
//   - 拒绝未知字段（防止拼写错误静默失效）；
//   - 空对象 / null 规范化为完整默认配置；
//   - 字段范围与 URL 形态校验。
//
// 返回规范化后的 JSON 字节，供宿主保存与 ApplyConfig 复用同一逻辑。
func Validate(raw []byte) (Config, []byte, error) {
	config := Default()
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) != 0 && !bytes.Equal(trimmed, []byte("null")) {
		decoder := json.NewDecoder(bytes.NewReader(trimmed))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&config); err != nil {
			return Config{}, nil, fmt.Errorf("config JSON invalid: %w", err)
		}
		if err := decoder.Decode(&struct{}{}); err == nil {
			return Config{}, nil, errors.New("config JSON must contain a single object")
		}
	}

	normalized, err := Normalize(config)
	if err != nil {
		return Config{}, nil, err
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		return Config{}, nil, err
	}
	return normalized, encoded, nil
}

// Normalize 对解析后的配置补默认值并做范围校验。
func Normalize(config Config) (Config, error) {
	if strings.TrimSpace(config.DirectBaseURL) == "" {
		config.DirectBaseURL = defaultDirectBaseURL
	}
	parsed, err := url.Parse(strings.TrimSpace(config.DirectBaseURL))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return Config{}, fmt.Errorf("direct_base_url must be an https URL, got %q", config.DirectBaseURL)
	}
	if strings.ContainsAny(parsed.RawQuery, "?#") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return Config{}, errors.New("direct_base_url must not carry query or fragment")
	}
	config.DirectBaseURL = strings.TrimRight(parsed.String(), "/")

	if strings.TrimSpace(config.UserAgent) == "" {
		config.UserAgent = defaultUserAgent
	}
	if len(config.UserAgent) > 256 {
		return Config{}, errors.New("user_agent too long (max 256)")
	}
	if config.DialTimeoutSeconds <= 0 {
		config.DialTimeoutSeconds = defaultDialTimeoutSeconds
	}
	if config.DialTimeoutSeconds > 600 {
		return Config{}, errors.New("dial_timeout_seconds must be <= 600")
	}
	if config.ResponseHeaderTimeoutSeconds <= 0 {
		config.ResponseHeaderTimeoutSeconds = defaultResponseHeaderTimeoutSecs
	}
	if config.ResponseHeaderTimeoutSeconds > 3600 {
		return Config{}, errors.New("response_header_timeout_seconds must be <= 3600")
	}
	if config.AccountDirectoryTTLSeconds <= 0 {
		config.AccountDirectoryTTLSeconds = defaultAccountDirectoryTTLSecs
	}
	if config.AccountDirectoryTTLSeconds > 3600 {
		return Config{}, errors.New("account_directory_ttl_seconds must be <= 3600")
	}
	if config.MaxBufferedBodyBytes <= 0 {
		config.MaxBufferedBodyBytes = defaultMaxBufferedBodyBytes
	}
	if config.MaxBufferedBodyBytes > 256<<20 {
		return Config{}, errors.New("max_buffered_body_bytes must be <= 256MiB")
	}
	if config.MaxStreamsPerConnection <= 0 {
		config.MaxStreamsPerConnection = defaultMaxStreamsPerConnection
	}
	if config.MaxStreamsPerConnection > maxStreamsPerConnectionCeiling {
		return Config{}, fmt.Errorf("max_streams_per_connection must be <= %d", maxStreamsPerConnectionCeiling)
	}
	return config, nil
}

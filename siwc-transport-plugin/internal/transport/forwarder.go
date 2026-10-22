package transport

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"

	"siwc-transport-plugin/internal/hostdir"
	"siwc-transport-plugin/internal/pluginconfig"
)

// AccountResolver 是 Forwarder 依赖的账号目录能力（由 hostdir.Directory
// 实现）；抽象成接口便于离线单测 SIWC 决策链。
type AccountResolver interface {
	Account(ctx context.Context, accountID int64) (hostdir.AccountMeta, bool)
	ResolveOutboundIdentity(ctx context.Context, accountID int64) (*hostdir.OutboundIdentity, error)
}

// Forwarder holds the outbound HTTP connection pools (one per account and
// proxy) and performs the full rewrite and forwarding of one request. The
// caller (the Forward RPC loop) always closes the upstream response body.
type Forwarder struct {
	config atomic.Pointer[pluginconfig.Config]
	// directory may be nil when the host offers no HostService: every account
	// is then forwarded transparently and the SIWC path fails explicitly.
	directory AccountResolver

	upstream upstreamPools

	// Metrics shown on the Health status page.
	RequestsTotal     atomic.Int64
	SIWCRequests      atomic.Int64
	PassthroughReqs   atomic.Int64
	ErrorsTotal       atomic.Int64
	InFlight          atomic.Int64
	LastSIWCAccountID atomic.Int64

	SIWCResponsesRequests     atomic.Int64
	PromptCacheKeyFromClient  atomic.Int64
	PromptCacheKeySynthesized atomic.Int64
	ToolSearchStripped        atomic.Int64
	ErrorRewrites             atomic.Int64
	recentCacheKeys           cacheKeyRing
}

// RecentCacheKeys returns the latest prompt_cache_key samples, oldest first.
func (forwarder *Forwarder) RecentCacheKeys() []CacheKeySample {
	return forwarder.recentCacheKeys.snapshot()
}

// NewForwarder creates a forwarder with its initial config.
func NewForwarder(directory AccountResolver, config pluginconfig.Config) *Forwarder {
	forwarder := &Forwarder{directory: directory}
	forwarder.config.Store(&config)
	return forwarder
}

// ApplyConfig switches the config atomically; new requests open connections
// built from it while in-flight requests finish on their current ones.
func (forwarder *Forwarder) ApplyConfig(config pluginconfig.Config) error {
	forwarder.config.Store(&config)
	forwarder.upstream.reset()
	return nil
}

// UpstreamStats reports the per-account upstream connection pools.
func (forwarder *Forwarder) UpstreamStats() UpstreamStats {
	return forwarder.upstream.stats()
}

func (forwarder *Forwarder) currentConfig() pluginconfig.Config {
	return *forwarder.config.Load()
}

// BodyLimit 返回当前配置的请求体缓冲上限。
func (forwarder *Forwarder) BodyLimit() int64 {
	return forwarder.currentConfig().MaxBufferedBodyBytes
}

// newUpstreamClient builds the client of one upstream lane. Each lane gets its
// own transport so it opens its own connection.
func newUpstreamClient(proxyURL string, config pluginconfig.Config) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   time.Duration(config.DialTimeoutSeconds) * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// Only reached when the upstream falls back to HTTP/1.1: a lane then
		// needs up to MaxStreamsPerConnection connections of its own.
		MaxIdleConns:          config.MaxStreamsPerConnection,
		MaxIdleConnsPerHost:   config.MaxStreamsPerConnection,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: time.Duration(config.ResponseHeaderTimeoutSeconds) * time.Second,
		ForceAttemptHTTP2:     true,
	}
	if strings.TrimSpace(proxyURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(proxyURL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return nil, fmt.Errorf("invalid proxy url %q", proxyURL)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	client := &http.Client{Transport: transport}
	// Redirects follow host semantics: upstream 3xx responses are relayed as-is.
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}

// CloseIdleConnections closes the idle connections of every pool (called on
// process shutdown).
func (forwarder *Forwarder) CloseIdleConnections() {
	forwarder.upstream.closeIdleConnections()
}

// Plan is the full decision for forwarding one request.
type Plan struct {
	SIWC      bool
	AccountID int64
	Method    string
	TargetURL string
	Headers   http.Header
	ProxyURL  string
	Body      []byte
	// RewriteNotes records the rewrites applied (diagnostics and status).
	RewriteNotes []string
}

// Decide 依据账号渠道判定构造转发计划。在真正发出上游请求之前调用，
// 因此这里的任何错误都属于 request_sent=false。
func (forwarder *Forwarder) Decide(ctx context.Context, method, rawURL string, headers http.Header, body []byte, proxyURL string, accountID int64) (*Plan, error) {
	config := forwarder.currentConfig()
	plan := &Plan{AccountID: accountID, Method: strings.TrimSpace(method), TargetURL: rawURL, Headers: headers.Clone(), ProxyURL: proxyURL, Body: body}
	if plan.Method == "" {
		plan.Method = http.MethodGet
	}
	StripHopByHopHeaders(plan.Headers)

	meta, known := hostdir.AccountMeta{}, false
	if forwarder.directory != nil {
		meta, known = forwarder.directory.Account(ctx, accountID)
	}
	if !known || !meta.SIWC {
		// 非 SIWC 账号（或目录不可达）：完全透明转发，宿主构造的 URL、
		// Authorization 与身份头原样发出。
		return plan, nil
	}

	plan.SIWC = true
	forwarder.LastSIWCAccountID.Store(accountID)

	identity, err := forwarder.directory.ResolveOutboundIdentity(ctx, accountID)
	if err != nil {
		return nil, fmt.Errorf("siwc identity: %w", err)
	}

	if rewritten, changed, err := RewriteURL(rawURL, config); err != nil {
		return nil, fmt.Errorf("siwc url rewrite: %w", err)
	} else if changed {
		plan.TargetURL = rewritten
		plan.RewriteNotes = append(plan.RewriteNotes, "url→direct")
	}

	plan.Headers = BuildSIWCHeaders(headers, identity.Token, config)
	plan.RewriteNotes = append(plan.RewriteNotes, "headers→minimal")

	isResponses := IsResponsesRequest(plan.TargetURL)
	plan.Body, _ = ApplySIWCBodyRules(body, config, isResponses)
	// Convert agent_message into an assistant message before stripping
	// computer_call items: conversion keeps the content and a deterministic
	// prefix, stripping is the last resort.
	converted := false
	plan.Body, converted = ConvertSIWCAgentMessages(plan.Body)
	stripped := false
	plan.Body, stripped = StripUnsupportedSIWCItems(plan.Body)
	toolsStripped := false
	cacheKeySynthesized := false
	if isResponses {
		forwarder.SIWCResponsesRequests.Add(1)
		plan.Body, toolsStripped = StripUnsupportedSIWCTools(plan.Body)
		if toolsStripped {
			forwarder.ToolSearchStripped.Add(1)
		}
		var cacheKey string
		plan.Body, cacheKey, cacheKeySynthesized = EnsureSIWCPromptCacheKey(plan.Body)
		source := CacheKeySourceNone
		switch {
		case cacheKeySynthesized:
			source = CacheKeySourceSynthesized
			forwarder.PromptCacheKeySynthesized.Add(1)
		case cacheKey != "":
			source = CacheKeySourceClient
			forwarder.PromptCacheKeyFromClient.Add(1)
		}
		if cacheKey != "" {
			// Codex sends its conversation id both as prompt_cache_key and as the
			// session_id header; mirror that pairing on the direct route.
			plan.Headers.Set("session_id", cacheKey)
		}
		forwarder.recentCacheKeys.add(newCacheKeySample(source, cacheKey, gjson.GetBytes(plan.Body, "input.#").Int()))
	}
	if !bytes.Equal(plan.Body, body) {
		note := "body→store/stream"
		if converted {
			note += "+agent-msg-convert"
		}
		if stripped {
			note += "+strip-computer-call"
		}
		if toolsStripped {
			note += "+strip-tool-search"
		}
		if cacheKeySynthesized {
			note += "+prompt-cache-key"
		}
		plan.RewriteNotes = append(plan.RewriteNotes, note)
	}

	// 代理优先用宿主按账号解析的值（含账号绑定代理），回退请求帧携带值。
	if strings.TrimSpace(identity.ProxyURL) != "" {
		plan.ProxyURL = identity.ProxyURL
	}
	return plan, nil
}

// Execute sends the planned upstream request on a connection of the plan's
// account. The caller closes the returned response body. Any error from here
// on counts as request_sent=true because the upstream may have been reached.
func (forwarder *Forwarder) Execute(ctx context.Context, plan *Plan, body io.Reader) (*http.Response, error) {
	config := forwarder.currentConfig()
	client, release, err := forwarder.upstream.acquire(
		upstreamPoolKey{accountID: plan.AccountID, proxyURL: strings.TrimSpace(plan.ProxyURL)},
		config.MaxStreamsPerConnection,
		func() (*http.Client, error) { return newUpstreamClient(plan.ProxyURL, config) },
	)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, plan.Method, plan.TargetURL, body)
	if err != nil {
		release()
		return nil, err
	}
	request.Header = plan.Headers
	response, err := client.Do(request)
	if err != nil {
		release()
		return nil, err
	}
	response.Body = &releasingBody{ReadCloser: response.Body, release: release}
	return response, nil
}

// releasingBody returns the request's stream slot when the caller closes the
// response body: the HTTP/2 stream stays open until then.
type releasingBody struct {
	io.ReadCloser
	release func()
}

func (body *releasingBody) Close() error {
	err := body.ReadCloser.Close()
	body.release()
	return err
}

//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// 用户实测的 SIWC 计划窗口耗尽形态（2026-10-17）：上游不出 SSE 事件直接掐流，
// 错误文本嵌在读取错误里；或以流中 response.failed 携带。
const siwcUsageLimitMessage = "The ChatGPT user has reached their Subscription Sharing usage limit. Ask the user to try again after their usage limit resets or use an API key instead."

// subscriptionSharingCooldownRepo 记录 SetRateLimited 的时间以断言冷却窗口。
type subscriptionSharingCooldownRepo struct {
	mockAccountRepoForGemini
	rateLimitedID    int64
	rateLimitedUntil *time.Time
	deletedID        int64
}

func (r *subscriptionSharingCooldownRepo) SetRateLimited(_ context.Context, id int64, until time.Time) error {
	r.rateLimitedID = id
	copied := until
	r.rateLimitedUntil = &copied
	return nil
}

func (r *subscriptionSharingCooldownRepo) Delete(_ context.Context, id int64) error {
	r.deletedID = id
	return nil
}

func TestIsOpenAISubscriptionSharingUsageLimitSignal(t *testing.T) {
	failedPayload := []byte(`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"The ChatGPT user has reached their Subscription Sharing usage limit."}}}`)

	assert.True(t, isOpenAISubscriptionSharingUsageLimitSignal(failedPayload, ""),
		"response.failed error.code 必须命中")
	assert.True(t, isOpenAISubscriptionSharingUsageLimitSignal(nil, siwcUsageLimitMessage),
		"错误消息文本必须命中（流断开路径）")
	assert.True(t, isOpenAISubscriptionSharingUsageLimitSignal([]byte(`{}`), "stream reset: "+siwcUsageLimitMessage),
		"嵌在读取错误里的消息必须命中")
	assert.False(t, isOpenAISubscriptionSharingUsageLimitSignal(nil, "rate limit exceeded"),
		"普通 rate limit 不应误命中")
	assert.False(t, isOpenAISubscriptionSharingUsageLimitSignal(nil, ""),
		"空信号不应命中")
}

func TestOpenAIStreamFailedEventSemanticStatus_SubscriptionSharingIs429(t *testing.T) {
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"The ChatGPT user has reached their Subscription Sharing usage limit."}}}`)

	require.Equal(t, http.StatusTooManyRequests,
		openAIStreamFailedEventSemanticStatus(payload, siwcUsageLimitMessage),
		"SIWC 计划窗口耗尽必须映射为 429，否则既不触发账号停用，客户端还收到 502")
	require.Equal(t, http.StatusTooManyRequests, openAIStreamFailureStatus(payload, siwcUsageLimitMessage))
	assert.True(t, openAIStreamFailedEventShouldFailover(payload, siwcUsageLimitMessage),
		"429 语义应允许 failover 到其他账号")
}

func TestHandle429_SubscriptionSharingSetsBoundedCooldown(t *testing.T) {
	repo := &subscriptionSharingCooldownRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 42, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{},
		[]byte(`{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"The ChatGPT user has reached their Subscription Sharing usage limit."}}`))

	require.Equal(t, int64(42), repo.rateLimitedID, "SIWC 计划窗口耗尽必须触发账号限流停用")
	require.NotNil(t, repo.rateLimitedUntil)
	assert.InDelta(t, before.Add(openAISubscriptionSharing429Cooldown).Unix(),
		repo.rateLimitedUntil.Unix(), 30,
		"冷却应约为 %s", openAISubscriptionSharing429Cooldown)
}

func TestClassifyOpenAIOAuth429_SubscriptionSharingIsQuotaExhaustion(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"error":{"code":"subscription_sharing_usage_limit_exceeded","type":"invalid_request_error","message":"` + siwcUsageLimitMessage + `"}}`),
		[]byte(`{"error":{"code":"subscription_sharing_usage_limit_exceeded","type":"usage_limit_reached","message":"` + siwcUsageLimitMessage + `","resets_in_seconds":1800}}`),
		[]byte(`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"` + siwcUsageLimitMessage + `"}}}`),
	} {
		disposition, resetAt := classifyOpenAIOAuth429(http.Header{}, body)
		require.Equal(t, openAIOAuth429QuotaReset, disposition, "body: %s", body)
		require.NotNil(t, resetAt)
	}

	disposition, _ := classifyOpenAIOAuth429(http.Header{}, []byte(`{"error":{"message":"Rate limit reached, please retry"}}`))
	require.Equal(t, openAIOAuth429Transient, disposition, "a plain 429 must stay transient")
}

func TestSubscriptionSharingStreamFailureSwitchesAccountImmediately(t *testing.T) {
	account := &Account{ID: 47, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModeSIWC}}
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"` + siwcUsageLimitMessage + `"}}}`)

	gateway := &OpenAIGatewayService{}
	failoverErr := gateway.newOpenAIAccountFailoverErrorWithClassificationHeaders(
		account, http.StatusTooManyRequests, http.Header{}, nil, payload, siwcUsageLimitMessage, false, false)

	require.False(t, failoverErr.RetryableOnSameAccount, "an exhausted SIWC account must not be retried")
	require.True(t, failoverErr.ShouldRetryNextAccount())
}

func TestHandle429_PlainRateLimitKeepsLegacyBehavior(t *testing.T) {
	repo := &subscriptionSharingCooldownRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{ID: 43, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	before := time.Now()
	svc.handle429(context.Background(), account, http.Header{},
		[]byte(`{"error":{"message":"You exceeded your current quota"}}`))

	// 既有兜底：无信号时走默认 429 冷却（5 秒量级），不应落到 30 分钟的
	// subscription_sharing 分支。
	require.NotNil(t, repo.rateLimitedUntil)
	assert.Less(t, repo.rateLimitedUntil.Unix(), before.Add(time.Minute).Unix(),
		"普通 429 保持秒级默认冷却，不应命中 SIWC 30 分钟分支")
}

// SIWC 账号的 compact 能力必须归零：remote compact 不在直连路由的授权操作内，
// 发出去必然 401 ChatPass 并污染账号状态。
func TestOpenAICompactSupportTier_SIWCIsUnsupported(t *testing.T) {
	siwcAccount := &Account{
		ID:          1,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModeSIWC},
	}
	require.Equal(t, 0, openAICompactSupportTier(siwcAccount),
		"SIWC 账号必须被视为 compact 不支持，禁止调度到 /responses/compact")

	// 对照：普通 codex OAuth 账号保持原语义（未知=1）。
	codexAccount := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	assert.Equal(t, 1, openAICompactSupportTier(codexAccount))
}

// ChatPass 操作级未授权（如 compact 打到直连路由）不得暂停/禁用账号：
// token 对 /v1/responses 依然有效。
func TestHandleUpstreamError_ChatPassOperationUnauthorizedDoesNotPoisonAccount(t *testing.T) {
	repo := &subscriptionSharingCooldownRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{
		ID:          44,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModeSIWC, "refresh_token": "rt"},
	}

	body := []byte(`{"error":{"message":"This ChatPass credential is not authorized for the requested operation.","type":"unauthorized"}}`)
	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusUnauthorized, http.Header{}, body)

	assert.False(t, shouldDisable, "ChatPass 操作级未授权不应禁用账号")
	assert.Equal(t, int64(0), repo.rateLimitedID, "ChatPass 操作级未授权不应触发限流停用")
	assert.Nil(t, repo.rateLimitedUntil)
}

// SIWC 账号收到 403 access_denied（退款/断开的废号信号）时必须自动删除。
func TestHandleUpstreamError_SIWCAccessDeniedAutoDeletes(t *testing.T) {
	repo := &subscriptionSharingCooldownRepo{}
	svc := NewRateLimitService(repo, nil, nil, nil, nil)
	account := &Account{
		ID:          45,
		Name:        "dead-siwc",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"auth_mode": OpenAIAuthModeSIWC, "refresh_token": "rt"},
	}

	shouldDisable := svc.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{},
		[]byte(`{"detail":{"error_code":"access_denied"}}`))

	assert.True(t, shouldDisable)
	assert.Equal(t, int64(45), repo.deletedID, "403 access_denied 的 SIWC 废号必须被自动删除")
	assert.Equal(t, int64(0), repo.rateLimitedID)
}

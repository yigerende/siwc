package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

const siwcManualRedirect = "http://127.0.0.1:1456/auth/callback"

type siwcRemoteGroup struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Platform string `json:"platform"`
	Status   string `json:"status"`
}

type siwcRemoteFlow struct {
	SessionID string
	State     string
	Groups    []int64
}

type siwcRemoteTarget struct {
	Owner   int64
	BaseURL string
	Token   string
	Expires time.Time
	Groups  []siwcRemoteGroup
	Flow    *siwcRemoteFlow
	Busy    bool
}

// SIWCRemoteHandler relays only the existing SIWC APIs. Tokens stay in bounded,
// expiring memory; neither remote credentials nor token responses reach the UI.
type SIWCRemoteHandler struct {
	mu      sync.Mutex
	targets map[string]*siwcRemoteTarget
	client  *http.Client
}

func NewSIWCRemoteHandler() *SIWCRemoteHandler {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = siwcRemoteDial
	return &SIWCRemoteHandler{
		targets: make(map[string]*siwcRemoteTarget),
		client: &http.Client{Transport: transport, Timeout: 90 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func normalizeSIWCRemoteURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return "", errors.New("请输入完整的 http:// 或 https:// sub2api 地址，不要附带账号、查询参数或页面路径")
	}
	u.Path = strings.TrimSuffix(strings.TrimRight(u.Path, "/"), "/api/v1")
	// Fixed root endpoints prevent a supplied path from changing API semantics.
	if u.Path != "" || u.RawPath != "" {
		return "", errors.New("请输入 sub2api 站点根地址，例如 https://sub2api.example.com")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func siwcRemoteIPAllowed(ip net.IP) bool {
	// Private and loopback installations are supported. Metadata/link-local and
	// multicast endpoints are never valid sub2api targets, including after DNS.
	return (ip.IsGlobalUnicast() || ip.IsLoopback()) && !ip.IsLinkLocalUnicast() && !ip.Equal(net.ParseIP("100.100.100.200"))
}

func siwcRemoteDial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(ips) == 0 {
		return nil, errors.New("目标地址无法解析")
	}
	for _, ip := range ips {
		if !siwcRemoteIPAllowed(ip.IP) {
			return nil, errors.New("不支持此目标地址")
		}
	}
	dialer := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range ips {
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

func (h *SIWCRemoteHandler) call(ctx context.Context, target *siwcRemoteTarget, method, path string, body, out any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target.BaseURL+"/api/v1"+path, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("目标 sub2api 地址无效")
	}
	req.Header.Set("Content-Type", "application/json")
	if target.Token != "" {
		req.Header.Set("Authorization", "Bearer "+target.Token)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return errors.New("无法连接目标 sub2api，请检查地址、证书和服务器网络")
	}
	defer res.Body.Close()
	// Do not relay arbitrary upstream bodies, cookies, token fields or errors.
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("目标 sub2api 返回 HTTP %d；请检查账号密码、管理员权限、SIWC 接口或目标服务日志", res.StatusCode)
	}
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 2<<20)).Decode(&envelope); err != nil || envelope.Code != 0 || len(envelope.Data) == 0 {
		return errors.New("目标返回了无效的 sub2api API 响应")
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return errors.New("目标返回的数据格式不兼容")
	}
	return nil
}

func siwcRemoteOwner(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "请先登录当前 sub2api 管理员账号")
		return 0, false
	}
	c.Header("Cache-Control", "no-store")
	return subject.UserID, true
}

func (h *SIWCRemoteHandler) Connect(c *gin.Context) {
	owner, ok := siwcRemoteOwner(c)
	if !ok {
		return
	}
	var req struct {
		URL      string `json:"url" binding:"required"`
		Email    string `json:"email" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "请输入地址、管理员邮箱和密码")
		return
	}
	base, err := normalizeSIWCRemoteURL(req.URL)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	target := &siwcRemoteTarget{Owner: owner, BaseURL: base, Expires: time.Now().Add(30 * time.Minute)}
	var login struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
		Requires2FA bool   `json:"requires_2fa"`
	}
	if err := h.call(c.Request.Context(), target, http.MethodPost, "/auth/login", map[string]string{"email": strings.TrimSpace(req.Email), "password": req.Password}, &login); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if login.Requires2FA || login.AccessToken == "" {
		response.BadRequest(c, "目标要求二次验证或验证码；请直接登录目标站点，使用其 SIWC 授权菜单")
		return
	}
	target.Token = login.AccessToken
	if login.ExpiresIn > 0 && login.ExpiresIn < 1800 {
		target.Expires = time.Now().Add(time.Duration(login.ExpiresIn) * time.Second)
	}
	var groups []siwcRemoteGroup
	if err := h.call(c.Request.Context(), target, http.MethodGet, "/admin/groups/all?platform=openai", nil, &groups); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	target.Groups = make([]siwcRemoteGroup, 0, len(groups))
	for _, g := range groups {
		if g.Platform == "openai" && g.Status == "active" {
			target.Groups = append(target.Groups, g)
		}
	}
	id := uuid.NewString()
	h.mu.Lock()
	for key, item := range h.targets {
		if !time.Now().Before(item.Expires) {
			delete(h.targets, key)
		}
	}
	ownerCount := 0
	for _, item := range h.targets {
		if item.Owner == owner {
			ownerCount++
		}
	}
	if len(h.targets) >= 128 || ownerCount >= 8 {
		h.mu.Unlock()
		response.Error(c, http.StatusTooManyRequests, "连接过多，请先断开已有连接")
		return
	}
	h.targets[id] = target
	h.mu.Unlock()
	time.AfterFunc(time.Until(target.Expires), func() { h.mu.Lock(); delete(h.targets, id); h.mu.Unlock() })
	response.Success(c, gin.H{"target_id": id, "url": base, "expires_at": target.Expires, "groups": target.Groups})
}

// claim serializes changes to a target and returns a snapshot without holding a
// global lock during network requests. Target IDs are scoped to the local admin.
func (h *SIWCRemoteHandler) claim(c *gin.Context) (*siwcRemoteTarget, bool) {
	owner, ok := siwcRemoteOwner(c)
	if !ok {
		return nil, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	target := h.targets[c.Param("target")]
	if target == nil || target.Owner != owner || !time.Now().Before(target.Expires) {
		response.BadRequest(c, "目标连接已失效，请重新连接并生成授权链接")
		return nil, false
	}
	if target.Busy {
		response.Error(c, http.StatusConflict, "此目标正在处理授权，请稍候")
		return nil, false
	}
	target.Busy = true
	copy := *target
	return &copy, true
}

func (h *SIWCRemoteHandler) release(id string, flow *siwcRemoteFlow, replace bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if target := h.targets[id]; target != nil {
		target.Busy = false
		if replace {
			target.Flow = flow
		}
	}
}

func (h *SIWCRemoteHandler) Generate(c *gin.Context) {
	var req struct {
		GroupIDs       []int64 `json:"group_ids"`
		ExtAgentHostID string  `json:"ext_agent_host_id"`
	}
	if c.ShouldBindJSON(&req) != nil || len(req.GroupIDs) == 0 {
		response.BadRequest(c, "请先选择至少一个分组")
		return
	}
	target, ok := h.claim(c)
	if !ok {
		return
	}
	var flow *siwcRemoteFlow
	defer func() { h.release(c.Param("target"), flow, true) }()
	allowed := map[int64]bool{}
	for _, g := range target.Groups {
		allowed[g.ID] = true
	}
	groups := make([]int64, 0, len(req.GroupIDs))
	for _, id := range req.GroupIDs {
		if !allowed[id] {
			response.BadRequest(c, "所选分组不属于目标站点或不是可用的 OpenAI 分组")
			return
		}
		groups = append(groups, id)
		delete(allowed, id)
	}
	var result service.SIWCAuthURLResult
	if err := h.call(c.Request.Context(), target, http.MethodPost, "/admin/openai/siwc/generate-auth-url", OpenAISIWCGenerateAuthURLRequest{
		RedirectURI: siwcManualRedirect, AgentNameHint: "sub2api", ExtAgentHostID: req.ExtAgentHostID,
	}, &result); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	u, err := url.Parse(result.AuthURL)
	if err != nil || u.Scheme != "https" || u.Host != "auth.openai.com" || u.Query().Get("state") == "" || result.SessionID == "" || u.Query().Get("redirect_uri") != siwcManualRedirect {
		response.BadRequest(c, "目标返回的 SIWC 授权链接无效")
		return
	}
	flow = &siwcRemoteFlow{SessionID: result.SessionID, State: u.Query().Get("state"), Groups: groups}
	response.Success(c, result)
}

func (h *SIWCRemoteHandler) Import(c *gin.Context) {
	var req OpenAISIWCCreateAccountRequest
	if c.ShouldBindJSON(&req) != nil {
		response.BadRequest(c, "授权参数不完整，请粘贴完整回调地址")
		return
	}
	target, ok := h.claim(c)
	if !ok {
		return
	}
	consume := false
	defer func() { h.release(c.Param("target"), nil, consume) }()
	if target.Flow == nil || req.SessionID != target.Flow.SessionID || req.State != target.Flow.State || req.IssuedClientID == "" {
		response.BadRequest(c, "回调不属于当前授权或已被提交，请重新生成链接")
		return
	}
	// Use the group snapshot from generation, never caller-supplied group IDs.
	req.GroupIDs = target.Flow.Groups
	req.RedirectURI = siwcManualRedirect
	req.ProxyID = nil
	consume = true // A timed-out mutation may have succeeded. Never replay it.
	var account struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	}
	if err := h.call(c.Request.Context(), target, http.MethodPost, "/admin/openai/siwc/create-from-oauth", req, &account); err != nil {
		response.BadRequest(c, err.Error()+"；请先查看目标账号列表，再重新授权，勿重复提交旧回调")
		return
	}
	response.Success(c, account)
}

func (h *SIWCRemoteHandler) Disconnect(c *gin.Context) {
	owner, ok := siwcRemoteOwner(c)
	if !ok {
		return
	}
	h.mu.Lock()
	if target := h.targets[c.Param("target")]; target != nil && target.Owner == owner {
		delete(h.targets, c.Param("target"))
	}
	h.mu.Unlock()
	response.Success(c, gin.H{"disconnected": true})
}

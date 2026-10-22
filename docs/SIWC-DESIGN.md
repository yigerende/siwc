# SIWC（Sign in with ChatGPT）渠道接入设计与对接指南

> 交付日期：2026-10-17 · sub2api 基线 v0.2.17（本仓库改造版） · 插件 `local.siwc.openai.transport` v0.1.0
>
> 官方文档：https://developers.openai.com/siwc/token-sharing-open-source（sign-in、models-and-inference、token-reference、errors-and-recovery）

---

## 一、SIWC 机制整理（官方文档要点）

### 1.1 这是什么渠道

SIWC 是「第三方客户端登录」：用户在**你自己的工具**里用 ChatGPT 账号登录，
之后该工具可以用**用户的 ChatGPT 订阅额度**发起推理请求。开源/自托管客户端
走 **token-sharing open-source** 流程 —— **不需要向 OpenAI 申请开发者应用、
不需要 client secret**，用 `dynamic_agent_client` 动态注册即可。

### 1.2 授权流程（本地自定义授权 = 动态注册）

```
授权端点  https://auth.openai.com/api/accounts/authorize
Token端点 https://auth.openai.com/api/accounts/oauth/token
```

**首次注册**（新 ChatGPT 账号）：

| 参数 | 值 |
| --- | --- |
| `client_id` | `dynamic_agent_client`（仅首次注册入口，**不是**要保存的身份） |
| `agent_name_hint` | 应用名（如 `sub2api`），跨安装保持一致 |
| `ext_agent_host_id` | 宿主稳定标识，`urn:uuid:<uuidv4>`；同宿主复用同值 |
| `response_type` | `code` |
| `redirect_uri` | `http://127.0.0.1:<port>/auth/callback`（**127.0.0.1，不要 localhost**；端口可变、路径固定） |
| `scope` | `openid profile email offline_access resource.invoke chatgpt.tokens.use.direct` |
| `resource` | `https://api.openai.com/v1` |
| `state` / `nonce` | 每次尝试新生成（nonce 用于 ID token 校验） |
| `code_challenge` / `code_challenge_method` | PKCE S256 |

浏览器完成登录/授权后回跳 loopback 回调，回调带回
`code & state & client_id`（**签发的 client_id，形如 `oaiapp_...`**）。
签发的 client_id 必须保存：后续换 token、刷新、复授权都用它。

**换 token**（POST form 到 token 端点，无 secret）：
`grant_type=authorization_code, client_id=<签发>, code, redirect_uri, code_verifier, resource`。

**复授权**（同账号再次登录）：用签发 client_id 替代 `dynamic_agent_client`，
可带 `id_token_hint`（跳过账号选择）与 `login_hint`；**不带** `agent_name_hint`。
回调若带回与注册不一致的 client_id，必须拒绝而不是替换。

**ID token 校验**（换 token 成功后必须做）：对 OpenAI 公开 JWKS 验签；
`iss == https://auth.openai.com`；`aud` 含本次签发 client_id；`exp` 未过期；
`nonce` 与授权请求一致；`sub` 即账号身份。**granted scopes 必须含
`chatgpt.tokens.use.direct` 才允许推理**（没有则保留登录、标记计划用量未启用）。

### 1.3 Token 生命周期

| 项 | 值 |
| --- | --- |
| access_token | 1 小时（`expires_in: 3600`），aud=`https://api.openai.com/v1` |
| refresh_token | 30 天，**每次刷新轮换**（旧 refresh 即弃） |
| 刷新 | `grant_type=refresh_token` + 签发 client_id + refresh_token + `resource`；**省略 scope**（保留原授权） |
| 刷新失败码 | `invalid_grant` / `refresh_token_expired` / `refresh_token_reused` 等 → 清 token 重新 OAuth |
| 吊销 | openid-configuration 的 `revocation_endpoint`，`token=<rt>&token_type_hint=refresh_token&client_id=<签发>` |

### 1.4 推理请求（端点与 Codex 渠道完全不同）

SIWC 授权后**不走** `chatgpt.com/backend-api/codex`（官方文档明确强调），而是：

- **Bearer 就是 OAuth access_token 本身**（无需再换 API key）；
- 推理**只支持** `POST https://api.openai.com/v1/responses`；
- **每次请求必须 `store:false` 且 `stream:true`**，以事件流消费；
- 成功判定：收到 `response.completed` 事件才算成功；
- 模型列表：`GET https://api.openai.com/v1/models` → `.models[]`，
  UI 展示取 `visibility == "list"` 的 `display_name`，推理传 `slug`；
- 常见错误（流中 `response.failed` 或 HTTP 错误体）：
  - `subscription_sharing_usage_limit_exceeded`（429，5 小时额度用尽，暂停请求）
  - `subscription_sharing_usage_unavailable`（503，有界退避重试）
  - `subscription_sharing_user_not_eligible`（403，不可重试）
  - `subscription_sharing_unsupported_capability`（400，去掉不支持的输入）
  - `subscription_sharing_route_not_supported`（403，端点/方法不对）
  - `subscription_sharing_invalid_user`（401，让用户重新登录）
  - 直连准入层（流开始前）：401/403/503，body 可能是 `{"detail":"..."}` 非标准错误体
- SIWC 直连路由**没有 WebSocket**。

### 1.5 关键约束

- `ext_agent_host_id`：一个「宿主」（一台部署实例）一个稳定值，重启不变；
  同一 client 可跨宿主复用，但每宿主有自己的 host id。
- credentials 每账号一份（按签发 client_id + sub 区分），不与其他注册混写。
- email 不是身份；`sub` 才是。两个注册可以同 email。

---

## 二、总体架构

```
┌────────────────────┐  1. generate-auth-url / exchange-code / create
│ 你的 OAuth 接口程序 │ ──────────────────────────────────────────┐
│ (本地浏览器+loopback│                                             ▼
│  回调监听)          │                        ┌──────────────────────────┐
└────────────────────┘                        │  sub2api（宿主，已改造）  │
      2. 浏览器 → auth.openai.com              │  /api/v1/admin/openai/   │
         /api/accounts/authorize               │    siwc/*（新接口）       │
      3. 回调 127.0.0.1:<port>/auth/callback   │  + SIWC token 刷新分支    │
         code+state+签发client_id ───────────► └──────────┬───────────────┘
                                                          │ 4. 出站请求
                                                  (SIWC 账号恒路由到插件)
                                                          ▼
                                        ┌───────────────────────────────┐
                                        │ SIWC 传输插件（独立进程）      │
                                        │ .s2plugin / go-plugin gRPC    │
                                        │ · URL→api.openai.com/v1/*     │
                                        │ · 最小请求头+SIWC Bearer      │
                                        │ · store=false / stream=true   │
                                        │ · 非SIWC账号→透明转发         │
                                        └───────────────┬───────────────┘
                                                        ▼
                                        https://api.openai.com/v1/responses
```

**分工原则**（与 sub2api 插件体系一致）：

- **授权、token 存取、刷新** → 宿主（sub2api）。token 是账号凭据，加密存于
  账号 `credentials`，插件永不经手持久化。
- **出站改写与转发** → 插件。宿主把构造好的 codex 形请求交给插件，插件按
  账号渠道决定改写（SIWC）或透传（其他），响应解析/计费/SSE 仍在宿主核心链。

---

## 三、sub2api 宿主改造清单（已完成）

| 文件 | 改动 |
| --- | --- |
| `internal/pkg/openai/oauth.go` | SIWC 常量（端点/scope/resource/默认回调/issuer）、`BuildSIWCAuthorizationURL`、`SIWCIDTokenClaims`、`OAuthSession` 增加 `Flow/Nonce/ExtAgentHostID` 字段 |
| `internal/pkg/openai/siwc_idtoken.go`（新） | ID token JWKS 验签（RS256、发现文档缓存、kid 轮换强刷）+ iss/aud/exp/nonce 校验 |
| `internal/service/oauth_service.go` | `OpenAIOAuthClient` 接口新增 `ExchangeSIWCCode` / `RefreshSIWCToken` |
| `internal/repository/openai_oauth_service.go` | 上述两方法的 HTTP 实现（form 带 `resource`，刷新省略 `scope`，拒绝用 `dynamic_agent_client` 换 token） |
| `internal/service/account.go` | `OpenAIAuthModeSIWC`、`IsOpenAISIWC()`、`GetOpenAISIWCHostID/ClientID` |
| `internal/service/openai_siwc_flow.go`（新） | `GenerateSIWCAuthURL`（PKCE+nonce+host-id）、`ExchangeSIWCCode`（state 常量时间校验、签发 client_id 规则、JWKS 验签、scope 检查）、`RefreshSIWCAccountToken`、loopback 回调校验（强制 127.0.0.1+http+固定路径） |
| `internal/service/openai_oauth_service.go` | `RefreshAccountToken` SIWC 分支（新端点/新参数）；`BuildAccountCredentials` 写入 `auth_mode=siwc`、签发 client_id、host-id、granted scopes、sub；`OpenAITokenInfo` 增加 SIWC 字段 |
| `internal/handler/admin/openai_oauth_handler.go` | 三个新 handler（见下） |
| `internal/server/routes/admin.go` | 路由注册 |
| `internal/service/plugin_manager.go` | `ShouldRouteOpenAIOAuth`：**SIWC 账号绕过灰度分桶恒路由到插件**（未启用插件时回落宿主默认路径并在上游得到明确 401/403；同时保证 WS 入口自动切 HTTP Bridge） |
| `internal/service/plugin_runtime_integration_test.go` | 集成测试插件无关化（env 定制配置/UI/版本/信任密钥，默认行为不变） |

**凭据落盘**（SIWC 账号 `credentials`）：`access_token / refresh_token / id_token /
expires_at / client_id（签发）/ auth_mode=siwc / ext_agent_host_id / granted_scopes /
plan_use_enabled / subject / email`；同时 `extra.auth_provider=siwc`（插件可见的
渠道标记，credentials 不进插件元数据快照而 extra 会）。

**刷新链路**：定时器（token_refresh_service）与请求路径（openai_token_provider）
都汇到 `RefreshAccountToken` → SIWC 分支 → 新端点。`invalid_grant` 等按既有
刷新失败处理（账号置错误态，重新授权即恢复）。

---

## 四、SIWC 传输插件（`siwc-transport-plugin/`）

能力：`openai.oauth.outbound_transport.v1`（宿主 v0.2.x 唯一支持的插件能力，
恰好覆盖 OpenAI OAuth 出站）。详见插件目录 `README.md`。

核心决策（`internal/transport/forwarder.go`）：

```
Forward(start{account_id, url=chatgpt.com/backend-api/codex/..., headers, ...})
  ├─ ListAccounts 缓存(30s) 判定 extra.auth_provider==siwc?
  │    ├─ 否 → 透传：原 URL/头/代理（宿主语义不变）
  │    └─ 是 → ResolveOutboundIdentity(账号) → {token, proxy}
  │         ├─ URL: chatgpt.com/backend-api/codex/X → <direct_base>/v1/X
  │         ├─ 头:  重建最小集 {Authorization: Bearer <siwc token>,
  │         │        Content-Type, Accept, User-Agent}，丢弃全部 codex 身份头
  │         ├─ 体:  /responses POST → 强制 store=false、stream=true
  │         └─ 代理: 账号解析值优先，回退请求帧携带值
  └─ Execute(上游) → 响应帧序回传（start/chunks/end；错误帧带 request_sent）
```

已验证：
- 插件单测（改写规则、决策链、配置、SIWC 判定）全部通过；
- **宿主×插件真实进程集成测试通过**（签名校验→启动→握手→配置→真实转发→失败路径）；
- 产物 `dist/local.siwc.openai.transport.s2plugin`（win/linux/darwin 三平台运行时 + Ed25519 签名）。

---

## 五、你的 OAuth 接口程序对接指南

假设 sub2api 部署在 `https://s2a.example.com`，管理员 token 为 `$TOKEN`
（`Authorization: Bearer $TOKEN`）。程序跑在用户本地机器。

### 5.1 全新注册（首次）

**① 生成授权 URL**（sub2api 生成并保管 PKCE/state/nonce）：

```bash
curl -X POST https://s2a.example.com/api/v1/admin/openai/siwc/generate-auth-url \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "redirect_uri": "http://127.0.0.1:1455/auth/callback",
    "agent_name_hint": "my-tool",
    "proxy_id": 1
  }'
# → {"auth_url":"https://auth.openai.com/api/accounts/authorize?...",
#    "session_id":"...","ext_agent_host_id":"urn:uuid:...","registration":true}
```

- `redirect_uri`：你本地监听的回调（端口自选，路径保持 `/auth/callback`）。
- **`ext_agent_host_id` 必须持久化**并在该 sub2api 实例后续所有 SIWC 注册中回传
  （不传会每次生成新值，OpenAI 侧按宿主归因用量）。
- 复授权时传 `client_id`（该账号已保存的签发值）+ 可选 `id_token_hint`/`login_hint`。

**② 本地监听 + 打开浏览器**：监听 `127.0.0.1:1455`，用系统浏览器打开
`auth_url`。用户登录并授权后，回调收到
`?code=...&state=...&client_id=oaiapp_...`（`error=access_denied` 则终止）。

**③ 交换 + 建号**（一步完成）：

```bash
curl -X POST https://s2a.example.com/api/v1/admin/openai/siwc/create-from-oauth \
  -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" \
  -d '{
    "session_id": "<①的session_id>",
    "code": "<回调的code>",
    "state": "<回调的state>",
    "issued_client_id": "<回调的client_id，首次注册必填>",
    "proxy_id": 1,
    "name": "my SIWC account",
    "group_ids": [1]
  }'
```

服务端完成：state 校验 → 签发 client_id 规则校验 → token 交换（带 resource）→
ID token JWKS 验签（iss/aud/exp/nonce）→ scope 检查 → 建号
（`auth_mode=siwc`、`extra.auth_provider=siwc`）。

只要 token 就不建号：用 `/siwc/exchange-code`（同参数，返回 tokenInfo，
其中 `plan_use_enabled=false` 表示用户未同意 ChatGPT 计划用量）。

**④ 复授权/换绑**：①里传 `client_id=<保存的签发值>`、`ext_agent_host_id=<持久化的>`
（可带 `id_token_hint`）；③同样调用（`issued_client_id` 可省）。

### 5.2 账号建立后

- 无需任何额外开关：该账号的网关请求**自动**走 SIWC 插件（恒路由，与灰度无关，
  前提是插件已启用）。
- token 到期前由宿主自动刷新（新端点 + 轮换 refresh token）。
- 刷新彻底失败（用户在 ChatGPT 设置里断开等）→ 账号转错误态 → 重新走 ①②③。

---

## 六、sub2api 部署方配置

```yaml
plugins:
  allow_unsigned: false
  trusted_publishers:
    siwc-publisher-v1: "X5VTSczPu9IN/7kqGNmz2IP6hF1R1f9zmat6drqU7mE="  # build/keys/siwc-publisher.public 的内容
```

管理面板 → 插件 → 上传 `dist/local.siwc.openai.transport.s2plugin` → 确认
状态「兼容（0.2.17 已测试）」→ 启用（rollout 只影响非 SIWC 账号的透传接管比例；
**SIWC 账号只要插件启用就恒走插件**）。

---

## 七、风险与已知边界（无账号实测前必读）

1. **直连路由响应形态**：`/v1/responses` 的事件流与 codex backend 的
   Responses 流同构（`response.created/delta/completed`），宿主现有 SSE 解析
   预期兼容，但**未经真实账号验证**。首次实测请开启账号级日志逐事件比对。
2. **模型列表**：SIWC 的 `/v1/models` 返回 `models[]`（slug/display_name/
   visibility），与标准 `data[]` 形态不同；宿主「模型广场/同步」对 SIWC 账号
   可能显示不全——不影响推理（模型名直接透传），影响展示。
3. **额度错误语义**：`subscription_sharing_usage_limit_exceeded`(429) 是 ChatGPT
   计划 5 小时窗口（Plus 用户全 App 共享）。宿主现有 429→限流冷却机制会按
   账号暂停，行为合适；但「额度」页显示的 credits 概念不适用于 SIWC。
4. **不支持的能力**：直连路由仅 Responses API（`store:false/stream:true`）。
   图片/ embeddings / count_tokens / WebSearch 计费附加等 codex 专属能力在
   SIWC 账号上会得到 `subscription_sharing_unsupported_capability` 或 404——
   建议把 SIWC 账号放进独立分组，只放行对话模型。
5. **WebSocket**：SIWC 账号恒走 HTTP Bridge（直连路由无 WS），并发行为与
   codex 账号的 WS 池不同。
6. **需要外网**：授权/刷新/JWKS 都要访问 `auth.openai.com`；受限网络给
   `generate-auth-url` 传 `proxy_id`，账号也绑定同一代理。
7. **`response.failed` 流中错误**：宿主会把流中失败按非流式错误呈现；
   `subscription_sharing_*` 错误码原样保留在错误信息里，据此排障。

---

## 八、验证状态

| 项 | 状态 |
| --- | --- |
| 宿主 SIWC 流程单测（URL 参数/state/client-id 规则/nonce/scope/凭据构建/刷新分支） | ✅ 通过 |
| 宿主全量相关包回归（service/repository/handler/server/routes，135s 全套） | ✅ 通过 |
| 插件单测（改写/决策/配置/SIWC 判定） | ✅ 通过 |
| 宿主×插件真实进程集成测试（签名→握手→配置→转发→失败路径） | ✅ 通过 |
| 真实 SIWC 账号端到端（授权→建号→推理→刷新） | ⏳ 待有账号后按 §5 流程实测 |

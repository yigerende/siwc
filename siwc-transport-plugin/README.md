# SIWC OpenAI Transport（sub2api 插件）

`local.siwc.openai.transport` v0.1.0 — sub2api 的 OpenAI OAuth 出站传输插件，
为 **SIWC（Sign in with ChatGPT，token-sharing open-source）渠道** 而构建。

## 职责

对命中插件的 OpenAI OAuth 账号请求（`POST /v1/responses` 等上游调用）：

| 账号 | 行为 |
| --- | --- |
| SIWC 账号（`extra.auth_provider == "siwc"`） | URL 从 `chatgpt.com/backend-api/codex/*` 改写为 `api.openai.com/v1/*`；重建最小请求头（Bearer = 宿主刷新后的 SIWC access token）；请求体强制 `store=false`、`stream=true`；代理优先用宿主按账号解析的值 |
| 其他账号 | 完全透明转发（宿主原样语义），不改 URL、头与体 |

身份与 token 由宿主解析（`HostService.ResolveOutboundIdentity`，必要时宿主先完成
SIWC 刷新），插件不持久化任何 token。SIWC 判定经 `HostService.ListAccounts` 的
`metadata_json.extra.auth_provider`（30s TTL 缓存，宿主服务不可达时退化为透传）。

## 目录

```
cmd/siwc-transport/        进程入口（只装配 + pluginv1.Serve）
internal/pluginconfig/     配置结构 / 默认值 / 严格校验（snake_case）
internal/pluginrpc/        TransportPlugin gRPC 服务端（Forward 帧循环）
internal/transport/        URL/头/体改写规则 + HTTP 转发器（按代理分池）
internal/hostdir/          HostService 反向连接 + 账号目录缓存
ui/                        配置页（UI Bridge v1，无外部依赖）
tools/pack/                .s2plugin 打包器（交叉编译 + 哈希 + 可选签名）
tools/keygen/              Ed25519 发布密钥生成
manifest.source.json       包清单源（打包器补全 runtimes/files）
```

## 构建与测试

```bash
go test ./... -count=1          # 单元测试（含 SIWC 改写决策链）
go run ./tools/keygen -out build/keys/siwc-publisher   # 首次：生成发布密钥
go run ./tools/pack \
  -targets windows-amd64,linux-amd64,darwin-arm64 \
  -signing-key build/keys/siwc-publisher.private \
  -key-id siwc-publisher-v1
# → dist/local.siwc.openai.transport.s2plugin
```

开发阶段可打未签名包（省略 `-signing-key`），并在 sub2api 配置
`plugins.allow_unsigned: true`（仅限隔离环境，测完即关）。

### 宿主侧集成测试（真实插件进程）

```powershell
cd ..\sub2api\backend
$pub=(Get-Content ..\..\siwc-transport-plugin\build\keys\siwc-publisher.public -Raw).Trim()
$env:SUB2API_TEST_PLUGIN_PACKAGE='<绝对路径>\dist\local.siwc.openai.transport.s2plugin'
$env:SUB2API_TEST_TRUSTED_KEY_ID='siwc-publisher-v1'
$env:SUB2API_TEST_TRUSTED_PUBLIC_KEY=$pub
$env:SUB2API_TEST_PLUGIN_HOST_VERSION='0.2.17'
$env:SUB2API_TEST_PLUGIN_UI_FILES='ui/index.html,ui/assets/app.js'
$env:SUB2API_TEST_PLUGIN_CONFIG='{}'
go test ./internal/service -run '^TestPluginRuntimeIntegration$' -count=1 -v
```

覆盖：签名/清单校验 → 进程启动 → gRPC 握手 → 配置应用 → 真实 Forward
转发（状态码/头/体/ContentLength 逐项断言）→ 请求体读取失败路径。

## 配置（json，snake_case；`{}` 即全默认）

| 字段 | 默认 | 说明 |
| --- | --- | --- |
| `direct_base_url` | `https://api.openai.com` | SIWC 直连路由 base（https） |
| `user_agent` | `sub2api-siwc-transport/0.1.0` | 出站 UA |
| `force_store_false` | `true` | `/responses` 强制 `store=false`（SIWC 必需） |
| `force_stream` | `true` | `/responses` 强制 `stream=true`（SIWC 必需） |
| `rewrite_chatgpt_codex_urls` | `true` | `chatgpt.com/backend-api/codex/*` → `<base>/v1/*` |
| `dial_timeout_seconds` | `30` | 建连超时（≤600） |
| `response_header_timeout_seconds` | `300` | 响应头超时（≤3600；流开始后不限） |
| `account_directory_ttl_seconds` | `30` | 账号目录缓存 TTL（≤3600） |
| `max_buffered_body_bytes` | `268435456` | 请求体缓冲上限（≤256MiB，与宿主 `gateway.max_body_size` 一致） |
| `max_streams_per_connection` | `16` | 每个账号单条上游连接承载的并发请求数，满了自动新开连接（≤100，不限制账号总并发） |

## request_sent 语义

- `false`：改写/身份解析/请求体读取/代理配置失败（上游必然未收到请求）。
- `true`：已调用上游 HTTP 传输后的任何失败（含建连失败——无法证明上游未收到）。
  宿主据此禁止对同一请求的自动账号重试重放。

## 兼容性

`requires.sub2api: >=0.2.17 <0.3.0`；`plugin_protocol/transport_api/ui_bridge` 均
v1。密钥轮换顺序：先发布含新公钥的宿主配置 → 再发新签名包 → 最后停用旧密钥。

# sub2api + SIWC Transport Plugin 源码包（2026-10-20）

## 内容

- `sub2api/` — sub2api v0.2.17 改造版（上游 Wei-Shaw/sub2api + SIWC 支持）。相对 10-17 版新增：
  - SIWC 账号收到上游普通 429（Rate limit exceeded）时不再原地重试，立即换号并迁移会话绑定；
    所有账号都撞限时按 Retry-After 短暂等待后再轮一遍（单请求最多等 2 分钟）
    （`internal/service/openai_account_runtime_block_fastpath.go`、`internal/handler/openai_siwc_429_wait.go`、
    `internal/handler/openai_gateway_handler.go`、`internal/handler/openai_chat_completions.go`）；
  - SIWC 订阅额度耗尽信号映射为配额 429 并冷却；插件热更新/停用时旧运行时后台排空（最长 30 分钟）
    （`internal/service/plugin_manager.go`）；
  - SIWC 导入账号默认并发改为 70（`internal/handler/admin/openai_oauth_handler.go`）。
- `siwc-transport-plugin/` — SIWC 出站传输插件 v0.2.5（独立 Go 工程）：
  - 按账号分池的上游 HTTP/2 连接，每条连接最多 16 个并发请求，满了自动开新连接；
  - `session_id` 跟随 `prompt_cache_key`，保证上游提示缓存命中；
  - SIWC 订阅额度耗尽（400）改写为配额 429；请求体缓冲上限 256 MiB；
  - 修复账号目录在缓存未命中时重复解锁导致的进程崩溃；目录刷新改为后台刷新 + 同一时间只刷新一次；
  - RPC 处理中的 panic 只让单个请求失败，崩溃与 panic 记录写入 `/tmp/siwc-transport-*.log`，健康数据显示 `panics_total`；
  - 打包器 `tools/pack`（`.s2plugin` + Ed25519 签名）、`tools/keygen`、配置 UI。
- `oauth-tool/siwc_login.py`、`oauth-tool-10100/siwc_login.py` — 本地 SIWC 授权导入工具（服务器地址、管理员凭据已替换为占位符，默认并发 70）。
- `docs/` — SIWC 设计文档与参考资料。

## 已脱敏 / 未包含

插件签名私钥（`siwc-transport-plugin/build/`）、构建产物（`dist/` 下的 `.s2plugin`）、服务器部署与运维脚本（`server-deploy/`）、
服务器地址、管理员账号密码、数据库/Redis/JWT 等密钥、本机状态文件、`.git` 历史、日志与压缩包。
`sub2api/deploy/.env.example` 为 SIWC 独立 Docker 部署模板，只含占位值。

## 构建

- 插件：`cd siwc-transport-plugin && go run ./tools/keygen` 生成自己的密钥，再
  `go run ./tools/pack -targets linux-amd64 -signing-key <私钥> -key-id <id>`；宿主 `config.yaml` 的
  `plugins.trusted_publishers` 填对应公钥。
- 宿主：`cd sub2api && docker build -t sub2api-siwc:latest -f Dockerfile.siwc .`（详见各 README 与 `docs/SIWC-DESIGN.md`）。

## 与现有 sub2api 并行部署

Docker 部署已使用独立的 `sub2api-siwc` 镜像、Compose 项目、容器、网络和数据目录，默认宿主机端口 `10099`。完整步骤见 [SIWC Docker 部署说明](sub2api/deploy/DOCKER.md)。上传时保留整个源码目录结构。

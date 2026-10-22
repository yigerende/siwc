# sub2api-siwc Docker 部署

此源码包构建独立的 `sub2api-siwc` 镜像。前端在构建时打包进后端二进制，不挂载原版 sub2api 的前端目录。

## 与已有 sub2api 并行运行

| 项目 | SIWC 默认值 |
| --- | --- |
| Compose 项目 / 应用服务 / 应用容器 | `sub2api-siwc` |
| 应用镜像 | `sub2api-siwc:latest` |
| PostgreSQL / Redis 容器 | `sub2api-siwc-postgres` / `sub2api-siwc-redis` |
| Compose 网络 | `sub2api-siwc_sub2api-siwc-network` |
| 宿主机端口 → 容器端口 | `10099` → `8080` |
| 新建数据库 / 用户 | `sub2api_siwc` |
| 本地目录版的数据目录 | `sub2api-siwc-data/`、`sub2api-siwc-postgres-data/`、`sub2api-siwc-redis-data/` |
| 命名卷版的卷名 | `sub2api-siwc_sub2api-siwc-data`、`sub2api-siwc_sub2api-siwc-postgres-data`、`sub2api-siwc_sub2api-siwc-redis-data` |

将整份源码上传到独立目录，例如 `/opt/sub2api-siwc-src/`，保留 `sub2api/`、`siwc-transport-plugin/` 等目录结构。下面命令均在这份源码的 `sub2api/deploy/` 中运行。需要 Docker Engine、Docker Compose v2 和 openssl。

不要使用原版的下载部署命令，或复制原版 `.env` 中的 `COMPOSE_PROJECT_NAME`、镜像名和数据挂载路径。Compose 文件自带项目名，无需额外指定 `-p`。如果 `10099` 已被占用，在这份 `.env` 中设置其他空闲的 `SERVER_PORT`。反向代理为 SIWC 配置独立域名，指向 SIWC 的端口。随包 `Caddyfile` 使用占位域名 `siwc.example.com` 并指向 `localhost:10099`；按实际域名和端口修改后，将站点块加入现有配置，不要覆盖原站点配置。

## 推荐：本地目录版，附带独立 PostgreSQL 和 Redis

```bash
cd /opt/sub2api-siwc-src/sub2api/deploy
bash docker-deploy.sh
# 按需修改 .env 中的 ADMIN_EMAIL、ADMIN_PASSWORD、SERVER_PORT 等
nano .env
docker compose -f docker-compose.local.yml up -d --build
docker compose -f docker-compose.local.yml logs -f sub2api-siwc
```

准备脚本只使用此包的本地模板；首次运行生成独立的 JWT、TOTP 和数据库密码，再次运行保留已有 `.env` 和凭据。它不会启动容器。管理员密码留空时，首次启动会生成密码并写入应用日志。

默认访问 `http://服务器IP:10099`。数据库和 Redis 仅在 SIWC 的 Docker 网络内开放，独立于原版容器。

更新这份源码后，在同一目录执行：

```bash
docker compose -f docker-compose.local.yml up -d --build
docker compose -f docker-compose.local.yml ps
```

停止 SIWC：

```bash
docker compose -f docker-compose.local.yml down
```

持久化数据保存在上表的三个目录。迁移时先停止这套 SIWC 容器，再备份完整源码、这三个目录和 `.env`，保留文件权限。新版目录名称不会自动迁移旧 `data/`、`postgres_data/`、`redis_data/` 中的数据。

## 其他 Compose 配置

- `docker-compose.yml`：相同应用，使用独立命名卷；启动命令为 `docker compose up -d --build`。
- `docker-compose.standalone.yml`：只启动 SIWC 应用，连接自行提供的 PostgreSQL 和 Redis。
- `docker-compose.dev.yml`：开发项目 `sub2api-siwc-dev`、镜像 `sub2api-siwc:dev`、容器和目录带 `-dev` 后缀；生产与开发同时运行时要分别指定空闲的宿主机端口。

这些是替代配置，选择其中一套部署。不要混用本地目录版和命名卷版来启动同一个实例。

外部数据库模式需要在 `.env` 明确填写：

```dotenv
DATABASE_HOST=your-postgres-host
DATABASE_PORT=5432
DATABASE_USER=sub2api_siwc
DATABASE_PASSWORD=your-database-password
DATABASE_DBNAME=sub2api_siwc
REDIS_HOST=your-redis-host
REDIS_PORT=6379
REDIS_PASSWORD=your-redis-password
REDIS_DB=1
```

`REDIS_DB=1` 只是示例，应选择尚未被其他应用占用的逻辑库或独立 Redis。数据库地址必须能从容器访问；容器里的 `127.0.0.1` 指向容器自身。连接宿主机可用 `host.docker.internal`，同时确保数据库允许来自容器的连接。若想保持两套业务数据独立，请为 SIWC 创建独立 PostgreSQL 数据库，避免误填原版生产库。

```bash
docker compose -f docker-compose.standalone.yml up -d --build
```

## 仅构建或搬运镜像

在 `sub2api/` 目录执行：

```bash
bash deploy/build_image.sh
# 等价：docker build -t sub2api-siwc:latest -f Dockerfile.siwc .
docker save -o sub2api-siwc-image.tar sub2api-siwc:latest
```

在同架构服务器加载后，可使用已有镜像：

```bash
docker load -i sub2api-siwc-image.tar
# 回到这份源码的 sub2api/deploy 目录
docker compose -f docker-compose.local.yml up -d --no-build
```

Compose 设置 `pull_policy: never`，应用默认使用本地 SIWC 镜像；首次部署要带 `--build`，或先构建/加载镜像。PostgreSQL 和 Redis 仍正常拉取各自的基础镜像。自有仓库可通过 `SUB2API_SIWC_IMAGE` 指定专用镜像名，使用预构建镜像时先自行拉取再加 `--no-build` 启动。

源码的 Docker Hub / GHCR 发布配置也已使用 `/sub2api-siwc` 镜像名；本次修改没有发布镜像。

## SIWC 插件

镜像构建宿主服务和内嵌前端，插件仍按原流程在后台安装。Linux x86_64 服务器使用源码包最外层的 `siwc-transport-0.2.5-linux-amd64.s2plugin` 和随包信任公钥，参照 `安装说明.txt`；不要在 Linux 上传 Windows 插件包。ARM64 服务器需要另行构建匹配架构的插件。

本次调整仅涉及 Docker 部署标识与配套文件，没有修改 SIWC 授权、token 交换或刷新逻辑。

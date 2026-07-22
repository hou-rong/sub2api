# Kimi 分支服务器 Docker Compose 部署指南

本文介绍如何在 Linux 服务器上部署 `hou-rong/sub2api` 的长期维护分支 `kimi-main`。该分支在上游 Sub2API 的基础上补充了 Kimi Coding OAuth、账号用量和会员信息等能力。

> [!IMPORTANT]
> 请不要使用上游一键部署脚本，也不要直接拉取 Docker Hub 上的 `weishaw/sub2api:latest`。它们对应上游版本，不包含本分支的 Kimi 功能。下面会在服务器上从 `kimi-main` 源码构建镜像。

## 1. 准备服务器

建议准备：

- Linux x86_64 或 arm64 服务器；建议至少 2 核 CPU、4 GB 内存，并预留源码构建空间
- Git、Docker 20.10+、Docker Compose v2+
- 可访问 GitHub、容器镜像仓库以及 Kimi 登录和 API 服务的出站网络
- 一个域名和 HTTPS 反向代理；仅临时测试时也可以直接开放服务端口

确认 Docker Compose 可用：

```bash
docker --version
docker compose version
```

## 2. 获取 Kimi 分支

建议将部署目录放在 `/opt` 或其他专门的应用目录中：

```bash
cd /opt
git clone --branch kimi-main --single-branch https://github.com/hou-rong/sub2api.git
cd sub2api
git switch --detach origin/kimi-main
git rev-parse --short HEAD
```

部署检出使用 detached HEAD 是有意为之：`kimi-main` 会定期 rebase 到上游 `main`，提交历史可能重写；服务器只负责运行代码，不应直接修改这个源码目录。

## 3. 配置环境变量

```bash
cd /opt/sub2api/deploy
cp .env.example .env
chmod 600 .env
```

分别执行三次以下命令，生成 PostgreSQL 密码、JWT 密钥和 TOTP 加密密钥，并将结果填入 `.env`：

```bash
openssl rand -hex 32
```

至少检查和修改这些配置：

```dotenv
BIND_HOST=127.0.0.1
SERVER_PORT=8080
SERVER_MODE=release

POSTGRES_PASSWORD=替换为随机密码
JWT_SECRET=替换为随机密钥
TOTP_ENCRYPTION_KEY=替换为随机密钥

ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=替换为强密码
TZ=Asia/Shanghai
```

- 使用 Caddy 或 Nginx 反向代理时，推荐保留 `BIND_HOST=127.0.0.1`，避免绕过 HTTPS 直接访问源站。
- 仅在需要通过 `http://服务器IP:8080` 临时访问时，才设置 `BIND_HOST=0.0.0.0`，并限制防火墙来源。
- `ADMIN_PASSWORD` 留空时，首次启动会自动生成密码，可从应用日志中查询。
- 不要将 `.env` 上传到 Git 或发送给他人。

创建持久化目录：

```bash
mkdir -p /opt/sub2api/deploy/data
mkdir -p /opt/sub2api/deploy/postgres_data
mkdir -p /opt/sub2api/deploy/redis_data
```

## 4. 构建并启动

先从当前 Kimi 源码构建应用镜像：

```bash
cd /opt/sub2api
docker build --pull -t weishaw/sub2api:latest .
```

这里沿用 `weishaw/sub2api:latest` 这个本地镜像名，是为了复用仓库内的 `docker-compose.local.yml`；镜像内容来自当前服务器上的 `kimi-main` 源码。不要在此部署目录执行 `docker compose pull sub2api`，否则会被上游官方镜像覆盖。

启动应用、PostgreSQL 和 Redis：

```bash
cd /opt/sub2api/deploy
docker compose -f docker-compose.local.yml pull postgres redis
docker compose -f docker-compose.local.yml up -d --pull never
```

`--pull never` 会确保 Compose 使用刚才从 Kimi 源码构建的本地应用镜像，同时前一条命令只更新 PostgreSQL 和 Redis 镜像。

检查运行状态：

```bash
docker compose -f docker-compose.local.yml ps
curl -fsS http://127.0.0.1:8080/health
docker compose -f docker-compose.local.yml logs --tail=100 sub2api
```

如果未设置 `ADMIN_PASSWORD`，可查询首次启动时生成的密码：

```bash
docker compose -f docker-compose.local.yml logs sub2api | grep -i "admin password"
```

## 5. 配置 HTTPS 访问

推荐用域名反向代理到 `127.0.0.1:8080`。例如 Caddy 配置：

```caddyfile
sub2api.example.com {
    reverse_proxy 127.0.0.1:8080
}
```

将域名解析到服务器并启动 Caddy 后，访问 `https://sub2api.example.com`。若使用 Nginx，请同时保留流式响应能力、提高长请求超时，并正确传递 `Host`、`X-Forwarded-For` 和 `X-Forwarded-Proto`。

PostgreSQL 和 Redis 在此 Compose 配置中没有映射宿主机端口，不要额外向公网开放 5432 或 6379。

## 6. 绑定 Kimi 账号

1. 使用管理员账号登录 Sub2API。
2. 进入账号管理，添加 Kimi OAuth 账号。
3. 按页面提示完成 Kimi 设备授权。
4. 更新或查询账号信息，确认状态、会员类型和用量窗口可以正常显示。
5. 在 Sub2API 中创建 API Key，再将服务地址和该 Key 配置到需要接入的客户端。

Kimi 授权采用设备授权流程，不要求公网回调地址，但 Sub2API 容器必须能够主动访问 Kimi 登录和 API 服务。

## 7. 更新 Kimi 分支

`kimi-main` 会定期同步上游代码，但服务器不会自动重新构建。升级前先记录当前版本并备份数据库：

```bash
cd /opt/sub2api
git rev-parse HEAD

cd /opt/sub2api/deploy
docker compose -f docker-compose.local.yml exec -T postgres \
  sh -c 'pg_dump -U "$POSTGRES_USER" "$POSTGRES_DB"' \
  > "sub2api-$(date +%F-%H%M%S).sql"
```

获取远端最新代码并切换到新提交：

```bash
cd /opt/sub2api
git fetch origin kimi-main
git switch --detach origin/kimi-main
git rev-parse --short HEAD
```

重新构建并只更新应用容器：

```bash
cd /opt/sub2api
docker build --pull -t weishaw/sub2api:latest .

cd /opt/sub2api/deploy
docker compose -f docker-compose.local.yml up -d --pull never --force-recreate sub2api
docker compose -f docker-compose.local.yml ps
curl -fsS http://127.0.0.1:8080/health
```

应用启动时会自动执行数据库迁移。确认新版本稳定后，再按服务器的镜像保留策略清理旧的构建层；不要使用 `docker compose down -v`，它可能删除持久化数据。

如果升级后需要回退，使用升级前记录的完整提交 SHA 重新检出、构建并重建应用容器：

```bash
cd /opt/sub2api
git switch --detach <升级前的完整提交SHA>
docker build -t weishaw/sub2api:latest .

cd /opt/sub2api/deploy
docker compose -f docker-compose.local.yml up -d --pull never --force-recreate sub2api
```

如果新版本已经执行不可向后兼容的数据库迁移，还需要同时恢复升级前的数据库备份。

## 8. 常用运维命令

```bash
# 查看容器状态
docker compose -f docker-compose.local.yml ps

# 跟踪应用日志
docker compose -f docker-compose.local.yml logs -f sub2api

# 查看数据库和 Redis 健康状态
docker compose -f docker-compose.local.yml exec postgres pg_isready
docker compose -f docker-compose.local.yml exec redis redis-cli ping

# 重启应用
docker compose -f docker-compose.local.yml restart sub2api

# 停止服务但保留数据
docker compose -f docker-compose.local.yml down

# 再次启动
docker compose -f docker-compose.local.yml up -d --pull never
```

## 9. 常见问题

### 构建阶段下载依赖失败

先确认服务器可以访问 GitHub、Go 模块源、npm registry 和 Docker Hub。国内服务器可在构建时指定 npm registry：

```bash
docker build --pull \
  --build-arg NPM_CONFIG_REGISTRY=https://registry.npmmirror.com \
  -t weishaw/sub2api:latest .
```

### 容器正常但外部无法访问

- 使用反向代理时，确认代理目标为 `127.0.0.1:8080`。
- 直接访问时，确认 `.env` 中是 `BIND_HOST=0.0.0.0`，并检查云安全组和系统防火墙。
- 先在服务器本机执行 `curl -fsS http://127.0.0.1:8080/health` 区分应用问题和入口网络问题。

### Kimi 授权或调用失败

- 检查容器出站网络和 DNS，而不是开放新的入站端口。
- 查看 `docker compose -f docker-compose.local.yml logs --tail=200 sub2api` 中的 OAuth 或上游请求错误。
- 确认部署的确实是本地构建的 Kimi 镜像；升级时不要执行 `docker compose pull sub2api`。

### 数据如何备份

日常备份至少应包含：

- PostgreSQL 的 `pg_dump` 备份
- `/opt/sub2api/deploy/.env`
- `/opt/sub2api/deploy/data`

需要完整冷备份时，先停止服务，再备份 `data`、`postgres_data` 和 `redis_data` 三个目录；复制完成后立即重新启动。不要在数据库运行期间直接复制 PostgreSQL 数据目录作为唯一备份。

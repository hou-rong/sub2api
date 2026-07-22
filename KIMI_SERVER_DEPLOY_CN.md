# Kimi 版本服务器部署

`kimi-main` 是 `hou-rong/sub2api` 长期维护的 Kimi 版本，会定期同步上游 `Wei-Shaw/sub2api:main`，并保留 Kimi Coding OAuth、账号用量和会员信息等能力。

在 Linux 服务器上部署时，需要从 `kimi-main` 源码构建应用镜像。请不要直接使用上游一键部署脚本或 Docker Hub 的 `weishaw/sub2api:latest`，因为上游镜像不包含本分支的 Kimi 功能。

完整操作步骤请参阅：

- [Kimi 分支服务器 Docker Compose 部署指南](deploy/KIMI_DOCKER_COMPOSE_CN.md)

指南包含服务器准备、密钥配置、源码构建、Docker Compose 启动、HTTPS 反向代理、Kimi 账号绑定、定期更新、数据库备份、回退和常见问题处理。

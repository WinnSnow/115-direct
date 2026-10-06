# CMS 迁移文档索引

部署人员请根据环境选择以下文档：

- [已有 Jellyfin 与旧 CMS](DEPLOYMENT_CMS_EXISTING.md)
- [干净环境](DEPLOYMENT_CLEAN.md)

Compose 示例：

```text
deploy/examples/docker-compose.cms-existing.yml
deploy/examples/docker-compose.clean-with-jellyfin.yml
deploy/examples/docker-compose.clean-without-jellyfin.yml
```

本文档不包含真实内网地址、真实 Cookie、API Key 或当前机器目录。部署值应从目标机器的 Docker 挂载、Jellyfin 设置、旧 STRM 内容和 115 目录管理中获取。

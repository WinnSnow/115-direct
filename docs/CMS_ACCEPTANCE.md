# CMS 兼容验收

本目录中的 CMS 验收工具只用于本地隔离测试。真实 CMS、Jellyfin 地址、Cookie、播放链接、媒体文件名、TMDB ID 和部署记录不应写入仓库。

## 覆盖范围

- CMS 来源地址校验和带扩展名/无扩展名的 `/d/{pickcode}` 解析。
- 中文查询参数编码、PlaybackInfo 重写、302 直链和 CDN Range 响应。
- 未授权访问、票据绑定、缓存、请求预算和写入接口拦截。
- CMS 复用模式与独立 115 取链模式。
- 迁移只读模式不启动同步、整理、上传和清理 worker。

## 本地运行

使用测试域名或环境变量，不要把真实域名和凭据写入命令或报告：

```bash
CMS_URL=http://cms.example.test:9527 \
JELLYFIN_URL=http://jellyfin.example.test:8091 \
CMS_ACCEPTANCE_DIR=/var/tmp/115-direct-cms-acceptance \
python3 scripts/cms_acceptance.py catalog
```

可运行 `select`、`baseline`、`browser` 和 `verify` 阶段。测试数据、Cookie、主密钥、原始 API 快照和截图放在仓库外的临时目录，并设置目录权限为 0700。

## Compose 验收实例

`docker-compose.cms-acceptance.yml` 使用独立数据目录和测试域名占位符。启动前在本地覆盖 `CMS_URL`、`JELLYFIN_URL`、端口和测试数据路径。验收实例只允许必要的读取和播放请求，禁止远端管理写入。

## 回归命令

```bash
go test ./...
go test -race ./...
go vet ./...
python3 -m py_compile scripts/cms_acceptance.py
```

不要提交 `.env`、Cookie、OAuth token、数据库、主密钥、播放票据、日志或截图。

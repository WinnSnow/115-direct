# 115 Direct

115 分享转存、TMDB 自动整理、STRM 同步和 Jellyfin 115 CDN 直连网关。

## 当前能力

- 115 Cookie 手工导入和二维码登录，凭据 AES-GCM 加密落盘。
- 企业微信自建应用接收分享链接，成员白名单与消息幂等。
- 每个任务使用独立接收暂存目录，确认文件实际到账后再整理。
- 显式 TMDB ID 或标题/年份高置信匹配，低置信任务进入人工确认。
- 本地电影、标准剧集、多集、Season 00 和多版本 Jellyfin 命名。
- 基于 TMDB 国家、原始语言和类型自动生成电影及电视剧二级分类目录。
- 115 按一级、二级分类归档目录副本，保留原文件名与接收原件，不上传 NFO 或图片。
- 本地 TMDB 刮削：电影/电视剧/季/剧集 NFO、海报、背景图；目录管理提供手动整理入口。
- 独立待整理目录与成品目录；受限命名模板、手动选择预览、季集修正及四种整理方式。
- 整理目录增量同步与每日完整校准；源缺失标记不可用并进入待确认队列，不自动删除本地或云端文件。
- 共存、覆盖、首次入库时间及清晰度优先版本策略；独立的删除联动与版本淘汰策略。
- 持久文件执行步骤、重试与启动恢复，软链接依赖及共享元数据引用保护。
- 按服务配置 HTTP/HTTPS/SOCKS5 代理；分级日志、筛选、分页、导出、30天及总量保留上限。
- Jellyfin 统一反向代理，按实际客户端 User-Agent 获取 115 CDN URL并返回 302。
- Docker Compose、单二进制和 systemd 部署。

## Docker Compose

```bash
cp .env.example .env
# 修改 ADMIN_PASSWORD、MASTER_KEY_PATH、PUBLIC_GATEWAY_URL、STRM_HOST_PATH、PENDING_HOST_PATH 和 UPLOAD_HOST_PATH
docker compose up -d --build
```

管理台默认地址：`http://HOST:9527`。Jellyfin 客户端应连接网关：`http://HOST:9096`。

需要在同一台服务器快速验证完整播放链路时，可启动可选的本地 Jellyfin：

```bash
docker compose --profile local-jellyfin up -d jellyfin
```

Jellyfin 管理界面位于 `http://HOST:8096`，容器内媒体目录为 `/media/115-strm`。115 Direct 与 Jellyfin 使用同一个 Compose 网络时，Jellyfin 服务地址填写 `http://jellyfin:8096`；手机客户端仍连接 `http://HOST:9096` 网关。

首次登录后依次配置：

1. 在“系统设置 → 115 登录”扫码登录，或导入包含 `UID/CID/SEID/KID` 的 Cookie；运行概览页也保留相同入口。
2. 选择 115 接收目录和整理目录。
3. 成品目录默认 `/media/115-strm`，待整理目录默认 `/media/115-pending`，分别挂载且不得互相包含；再配置手机客户端可访问的网关地址。
4. 配置 TMDB API Read Access Token。
5. 配置 Jellyfin 地址及管理员 API Key。
6. 将宿主机 `/media/115-strm` 以只读方式挂载到 Jellyfin。电视剧媒体库分别添加 `电视剧/国漫`、`电视剧/国产剧` 等二级分类作为扫描位置，不要直接扫描 `电视剧`；电影媒体库同样添加各二级分类。
7. 在系统设置的企业微信板块生成并保存“回调入口 Token”和公网管理地址，点击“显示已保存回调地址”，将完整地址（含 `access_token` 参数）填写到企业微信后台；另填双方一致的企业微信签名 Token 与 EncodingAESKey。
8. 如需监控下载目录上传，在“本地上传”页面启用监控，填写容器内本地上传目录和 115 目标目录 CID。上传默认使用事件监听加低频扫描、单任务串行和稳定窗口；文件完成后会登记远端 ID、生成待整理 STRM 并进入现有整理队列。
9. 开放平台应用审核通过后，在“系统设置 → 115 Open OAuth”保存 `client_id`、`access_token` 和 `refresh_token`。上传通道选择“自动”时优先使用官方 Open API；未授权或官方初始化未命中时才使用当前扫码 Cookie 通道。两套凭据独立保存。

企业微信消息格式：

```text
https://115.com/s/SHARE_CODE 提取码:abcd
https://115.com/s/SHARE_CODE 提取码:abcd tmdb:movie:603
https://115.com/s/SHARE_CODE 提取码:abcd tmdb:tv:1399
```

## 本地构建

```bash
make build
cp .env.example .env
# 编辑 .env 后启动
set -a; . ./.env; set +a
go run ./cmd/115-direct
```

运行要求：Go 1.24、Node.js 22。生产运行只需要生成的 Go 二进制。

## 测试

```bash
make test
make test-race
```

单元和集成测试使用本地模拟服务，不需要真实 115、TMDB、企业微信或 Jellyfin 账号。

整理与刮削流程见 [整理与刮削](docs/ORGANIZATION.md)，本次升级和验收记录见 [升级说明](docs/UPGRADE.md)。

## 播放链路

Jellyfin 客户端必须通过 `9096` 网关访问。网关保留普通 Jellyfin API，只改写 115 STRM 的 PlaybackInfo。实际播放时验证 Jellyfin 用户权限，以客户端 UA 向 115 换取短时 CDN URL，并返回无响应体的 `302`。支持源格式的客户端直接向 CDN 发送 Range 请求，视频流量不经过本服务。

115 媒体采用严格原片直链模式，关闭 HLS/DASH、转码和服务器重新封装兜底。PlaybackInfo 不再提供转码地址，旧分片请求返回 `409 direct_play_required`。客户端负责源文件解码；格式不兼容时播放失败，不占用 Jellyfin 播放转码资源。Jellyfin 元数据探测仍保留，普通本地媒体保持原有行为。

播放网关支持带扩展名的直链地址、音频和下载入口、串行取链及一次失效重试。管理台提供 STRM 单文件诊断、播放事件和可选 CDN 轻量检查，详见 [播放诊断](docs/PLAYBACK.md)。

## 数据与安全

- `ADMIN_PASSWORD` 没有代码内默认值，必须在 `.env` 或服务环境中设置；`ADMIN_USERNAME`、监听地址和公网网关同样可在配置文件编辑。
- SQLite 默认位于 `DATA_DIR`，主密钥位于 `MASTER_KEY_PATH`；STRM、待整理和上传目录分别由 `STRM_PATH`、`PENDING_PATH` 和 `UPLOAD_PATH` 配置，Compose 默认使用项目目录下的 `media/` 挂载。
- `master.key` 权限为 `0600`，Cookie、TMDB Token、企业微信 Secret 和 Jellyfin API Key 均加密保存。
- 管理台使用 HttpOnly/SameSite 会话、CSRF 校验和严格安全响应头。
- 直链使用不可枚举媒体 ID、HMAC 签名和 Jellyfin 用户令牌。
- 外部 HTTPS 由 Nginx/Caddy 提供，示例见 `deploy/nginx.conf.example`。

## 文档

- [架构与状态机](docs/ARCHITECTURE.md)
- [部署检查表](docs/DEPLOYMENT.md)

## License

MIT。115driver 作为独立 MIT 依赖使用；本项目未复制 CloudMediaSync 或无明确许可证项目的代码。

## 社区 [LINXUDO](https://linux.do)

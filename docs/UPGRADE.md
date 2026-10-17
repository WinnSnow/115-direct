# 升级指南

## 2026-10-18 播放修复

本次更新包含四项变化：

- **减少重复探测**：可选 Jellyfin 12.1 扩展按 STRM 文件指纹复用 24 小时内的有效媒体信息；信息不足、文件变化、过期或主动刷新时重新探测。先用 5MB 采样，必要时扩大到 20MB，并限制执行时间和清理取消的探测进程。
- **修复 Docker 地址兼容**：Jellyfin 配置使用宿主机 IP 时，后台读取 STRM 的来源可能是容器 IP。网关在验证后台签名后，通过配置的 Jellyfin 服务确认内部地址；鉴权失败分别记录未登录、权限不足、签名错误和来源错误。
- **增加逐请求计时**：播放信息、取链和播放上报记录独立请求 ID，以及后端响应头等待、鉴权、取链和改写耗时。嵌套阶段不相加，多个请求之间的间隔不作为处理耗时。
- **明确配置用途**：“Jellyfin 服务器”填写后端地址（通常为 `8096`）；目录配置显示实际播放代理端口（默认 `9096`）；完整 STRM 连接地址收进高级设置。

升级 115 Direct 后，地址兼容、计时日志和设置页面即可生效。**减少重复探测需要另外安装 Jellyfin 扩展并重启 Jellyfin**，见 [编译、安装与回滚](../extensions/jellyfin-playback/README.md)。扩展当前针对 Jellyfin 12.1 / .NET 10，其他版本需重新编译并验证。无需修改现有 Jellyfin 地址、用户或 STRM 文件。

2026-10-17 同一集的实际请求：修复前 PlaybackInfo 在网关内耗时 16.144 秒；修复后的用户播放耗时 26.472 毫秒，首次视频取链 284.332 毫秒（其中请求 115 为 242.514 毫秒），后续取链 42.500 / 39.280 毫秒。需要重新探测时，已记录的完整请求为 1.39–5.29 秒，具体请求 ID 见扩展实测表。

以上衡量服务端接口处理。网关发送 302 后，播放器访问 CDN、读取媒体、缓冲和出首帧的耗时不在当前计时范围内，因此不据此承诺相同幅度的体感改善。

验收时检查 Jellyfin 插件状态为 `Active`、健康检查为 `Healthy`；复用请求应出现 `115 Direct metadata ... mode="reused"`，必要探测应记录采样档位。通过客户端实际播放和拖动，再结合 `X-115-Direct-Request-ID` 定位对应日志。插件测试覆盖 29 项断言，Go 测试覆盖地址发现、授权状态和冷/热缓存请求计时。

## 升级前

1. 复制 `DATA_DIR`，包含 SQLite 数据库和自动生成的主密钥。
2. 备份 `STRM_PATH`、`PENDING_PATH` 和 `UPLOAD_PATH` 指向的媒体目录。
3. 保存当前 `.env`，升级时继续使用同一组路径、管理员账号和主密钥配置。

## Docker Compose

```bash
cp .env.example .env
# 编辑 .env：ADMIN_PASSWORD、MASTER_KEY_PATH、媒体目录和公网网关地址
$EDITOR .env
docker compose pull
docker compose build --pull
docker compose up -d
docker compose logs -f direct
```

Compose 默认把数据放在项目目录下的 `data/` 和 `media/`。生产部署请在 `.env` 中改为实际路径；待整理目录和成品目录必须分开，Jellyfin 只读挂载成品目录。

## systemd

将 `deploy/115-direct.service` 复制到 systemd，并在 `/etc/115-direct.env` 中填写管理员密码、主密钥路径、媒体目录及公网地址。确认服务账号对数据目录和三个媒体目录具有所需权限，然后执行：

```bash
systemctl daemon-reload
systemctl enable --now 115-direct
systemctl status 115-direct
```

## 验证

```bash
curl -fsS http://127.0.0.1:9527/healthz
go test ./...
go test -race ./...
go vet ./...
```

升级失败时停止新实例，恢复备份的数据目录和媒体目录，再启动上一版本。不要把 `.env`、数据库、主密钥、Cookie、OAuth token 或运行日志提交到 Git。

# 升级指南

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

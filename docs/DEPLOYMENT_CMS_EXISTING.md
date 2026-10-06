# 部署方案：已有 Jellyfin 与旧 CMS

适用于旧 CMS 已运行一段时间、已经生成大量 STRM，且 Jellyfin 正在读取这些媒体目录的环境。本方案保留原 STRM、NFO、图片、Jellyfin 数据库和媒体库路径，通过只读继承逐步接管旧播放链路。

## 1. 确认旧媒体目录

Jellyfin 容器中看到的 `/媒体库` 是容器路径，不一定是宿主机路径。查看 Jellyfin 的 Docker 挂载配置，找到它对应的宿主机目录，记为 `HOST_MEDIA_ROOT`。

示意关系：

```text
HOST_MEDIA_ROOT  ->  Jellyfin:/媒体库
HOST_MEDIA_ROOT  ->  direct:/legacy-media (只读)
```

如果旧 CMS 文件在另一台服务器，先通过 NFS、SFTP 挂载或其他文件系统方式挂载到目标主机，再将该目录作为 `HOST_MEDIA_ROOT`。

## 2. Docker Compose

下面只使用服务名和占位符，不包含真实 IP、真实域名或当前机器路径。

```yaml
services:
  direct:
    image: 115-direct:latest
    container_name: direct
    restart: unless-stopped
    environment:
      TZ: ${TZ:-Asia/Shanghai}
      DATA_DIR: /data
      STRM_PATH: /strm
      PENDING_PATH: /pending
      UPLOAD_PATH: /upload
      LISTEN_ADDR: ":9527"
      GATEWAY_ADDR: ":9096"
      CMS_LEGACY_ADDR: ":9528"
      PUBLIC_GATEWAY_URL: ${PUBLIC_GATEWAY_URL:-http://direct:9096}
      ADMIN_USERNAME: ${ADMIN_USERNAME:-admin}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD:?set ADMIN_PASSWORD}
    ports:
      - "${DIRECT_ADMIN_PORT:-9527}:9527"
      - "${DIRECT_GATEWAY_PORT:-9096}:9096"
      - "${CMS_LEGACY_PORT:-9528}:9528"
    volumes:
      - ${HOST_DATA:?set HOST_DATA}:/data
      - ${HOST_MEDIA_ROOT:?set HOST_MEDIA_ROOT}:/legacy-media:ro
      - ${HOST_PENDING:?set HOST_PENDING}:/pending
      - ${HOST_STRM:?set HOST_STRM}:/strm
      - ${HOST_UPLOAD:?set HOST_UPLOAD}:/upload

  # 已有 Jellyfin 时不要启用 local-jellyfin profile。
  jellyfin:
    image: jellyfin/jellyfin:latest
    container_name: jellyfin
    profiles: ["local-jellyfin"]
    environment:
      TZ: ${TZ:-Asia/Shanghai}
    ports:
      - "${JELLYFIN_PORT:-8096}:8096"
    volumes:
      - ${HOST_JELLYFIN_CONFIG:?set HOST_JELLYFIN_CONFIG}:/config
      - ${HOST_JELLYFIN_CACHE:?set HOST_JELLYFIN_CACHE}:/cache
      - ${HOST_MEDIA_ROOT:?set HOST_MEDIA_ROOT}:/媒体库:ro
      - ${HOST_STRM:?set HOST_STRM}:/media/115-strm:ro
```

已有 Jellyfin 时只启动 `direct`，不要启动 `local-jellyfin` profile。

## 3. CMS 兼容设置

入口：`系统设置 → CMS 兼容与继承`。

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 启用 CMS 播放接管 | 关闭（验证后开启） | 本系统设置页 |
| 旧 CMS 播放源地址 | `http://cms:9527` | 打开任意旧 STRM，取 `/d/` 之前的部分 |
| 继承 STRM 根目录 | `/legacy-media` | 本系统容器内挂载路径 |
| CMS 现有 115 Cookie | `CMS_COOKIE` | 旧 CMS 配置或旧 CMS 浏览器会话 |
| 直链接口请求间隔 | `1000` | 默认值，降低请求频率 |
| 启用旧 `/d` 播放入口 | 关闭（验证后开启） | 本系统设置页 |
| 迁移只读模式 | 开启 | 本系统设置页，保存后重启 |

如果旧 STRM 内容是：

```text
http://cms:9527/d/PICKCODE.mkv?/影片文件名.mkv
```

旧播放源地址填写：

```text
http://cms:9527
```

不要填写完整的 `/d/...` 路径，也不要填写 Jellyfin 或本系统网关地址。多个来源每行一个。

CMS Cookie 只用于复用旧 CMS 使用的 115 会话。系统只执行状态检查和直链接口查询，不执行扫码登录、重新登录、SSO 或退出登录；界面显示时会脱敏。

## 4. Jellyfin 原始后端

入口：`系统设置 → Jellyfin 原始后端`。

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 协议 | `http` | Jellyfin 实际服务协议 |
| 主机 | `jellyfin` | Docker 服务名、DNS 名称或主机名 |
| 端口 | `8096` | Jellyfin 原始监听端口 |
| 基础路径 | 留空 | 仅反代到路径前缀时填写 |
| API Key | `JELLYFIN_API_KEY` | Jellyfin 管理后台 → API Keys |

推荐填写：

```text
协议: http
主机: jellyfin
端口: 8096
基础路径: 留空
API Key: JELLYFIN_API_KEY
```

这里填写的是 Jellyfin 原始后端，不是本系统 `9096` 网关端口或管理端口。

## 5. 本系统 115 和本地目录设置

入口：`系统设置 → 目录链路`。

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 115 接收目录 CID | `INBOX_CID` | 目录管理中打开接收目录，复制当前 CID |
| 115 分类目录 CID | `LIBRARY_CID` | 目录管理中打开分类目录，复制当前 CID |
| 待整理目录 | `/pending` | Compose 中 `HOST_PENDING:/pending` 的容器路径 |
| 成品 STRM 目录 | `/strm` | Compose 中 `HOST_STRM:/strm` 的容器路径 |
| 本地上传目录 | `/upload` | Compose 中 `HOST_UPLOAD:/upload` 的容器路径 |

入口：`系统设置 → 115 账号`。

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 115 Cookie | `PAN_COOKIE` | 扫码登录，或从当前 115 会话复制 |
| 接收码/分享密码 | 按分享实际填写 | 分享链接或企业微信消息中获取 |

CMS Cookie 和本系统 115 Cookie 是两个独立字段。旧 CMS Cookie 用于继承媒体的直链查询；本系统 115 Cookie 用于新转存、同步、分类和目录操作。

## 6. 播放和代理设置

入口：`系统设置 → 播放设置`。

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 客户端访问的网关地址 | `http://direct:9096` | 使用客户端实际能够访问的网关服务名或 DNS 名称 |
| 播放前检查 CDN | 关闭 | 首次建议关闭，完成播放验证后再开启 |

如果客户端和本系统不在同一个 Docker 网络，`direct` 需要替换为客户端可解析的 DNS 服务名。

## 7. 旧地址解析和继承预览

「旧地址解析」中粘贴一个 STRM 文件的完整内容：

```text
http://cms:9527/d/PICKCODE.mkv?/影片文件名.mkv
```

点击「解析预览」，确认显示 `pickcode: PICKCODE`。解析只检查格式和来源，不修改文件，也不扫描 115。

「只读继承预览」填写：

```text
根目录: /legacy-media
相对目录: 电影/动画电影
```

或：

```text
根目录: /legacy-media
相对目录: 电视剧/国产剧
```

点击「读取本级目录」，只选择状态为「可继承」的 STRM，再点击「继承所选文件」。继承只保存关联记录，不修改 STRM、NFO、图片，不移动文件，不删除云端文件，不修改 Jellyfin 数据库。

建议先继承少量电影和电视剧，确认播放正常后再分批执行。

## 8. 启用和回滚顺序

1. 配置 Jellyfin 原始后端。
2. 配置旧 CMS 来源和 `/legacy-media`。
3. 开启迁移只读模式并重启。
4. 解析一个 STRM。
5. 继承少量 STRM。
6. 检查 CMS Cookie。
7. 测试 Jellyfin 播放和 302 跳转。
8. 开启 CMS 播放接管。
9. 确认无误后，再开启旧 `/d` 入口。

回滚时关闭 CMS 接管，恢复旧 CMS 播放入口和客户端地址。原 STRM 和元数据不需要恢复。

## 9. 验收标准

- 旧 STRM 内容 SHA256 不变。
- Jellyfin 仍能读取原媒体库。
- 已继承来源可以 302 到 115 CDN。
- 未继承 pickcode 不会被接管。
- CMS Cookie 检查不触发登录或退出。
- 本地硬盘媒体仍可通过 Jellyfin 播放。

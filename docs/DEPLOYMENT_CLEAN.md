# 部署方案：干净环境

本方案适用于没有旧 CMS、没有旧 CMS STRM 的新环境，分为「有 Jellyfin」和「暂时没有 Jellyfin」两种模式。

## 1. 目录和占位符

将以下占位符替换为目标机器上的实际宿主机目录：

```text
HOST_DATA
HOST_PENDING
HOST_STRM
HOST_UPLOAD
HOST_JELLYFIN_CONFIG
HOST_JELLYFIN_CACHE
```

本系统容器内固定使用：

```text
/data     数据库、配置和密钥
/pending  待整理目录
/strm     成品 STRM 目录
/upload   本地上传目录
```

## 2. 干净环境：有 Jellyfin

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
    volumes:
      - ${HOST_DATA:?set HOST_DATA}:/data
      - ${HOST_PENDING:?set HOST_PENDING}:/pending
      - ${HOST_STRM:?set HOST_STRM}:/strm
      - ${HOST_UPLOAD:?set HOST_UPLOAD}:/upload

  jellyfin:
    image: jellyfin/jellyfin:latest
    container_name: jellyfin
    restart: unless-stopped
    environment:
      TZ: ${TZ:-Asia/Shanghai}
    ports:
      - "${JELLYFIN_PORT:-8096}:8096"
    volumes:
      - ${HOST_JELLYFIN_CONFIG:?set HOST_JELLYFIN_CONFIG}:/config
      - ${HOST_JELLYFIN_CACHE:?set HOST_JELLYFIN_CACHE}:/cache
      - ${HOST_STRM:?set HOST_STRM}:/media/115-strm:ro
```

本系统目录设置：

| 配置项 | 填写值 | 获取方式 |
|---|---|---|
| 待整理目录 | `/pending` | Compose 容器路径 |
| 成品 STRM 目录 | `/strm` | Compose 容器路径 |
| 上传目录 | `/upload` | Compose 容器路径 |
| 115 接收目录 CID | `INBOX_CID` | 目录管理中选择或浏览 115 目录 |
| 115 分类目录 CID | `LIBRARY_CID` | 目录管理中选择或浏览 115 目录 |

Jellyfin 媒体库路径填写：

```text
/media/115-strm
```

Jellyfin 原始后端：

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 协议 | `http` | Jellyfin 实际服务协议 |
| 主机 | `jellyfin` | Compose 服务名 |
| 端口 | `8096` | Jellyfin 容器服务端口 |
| 基础路径 | 留空 | 没有路径前缀时留空 |
| API Key | `JELLYFIN_API_KEY` | Jellyfin 管理后台 → API Keys |

本系统 115 Cookie 在「系统设置 → 115 账号」中配置，可使用扫码登录或粘贴当前 115 Cookie。它不是 CMS Cookie。

115 目录和本地目录设置：

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 115 接收目录 CID | `INBOX_CID` | 目录管理中选择接收目录并复制 CID |
| 115 分类目录 CID | `LIBRARY_CID` | 目录管理中选择分类目录并复制 CID |
| 待整理目录 | `/pending` | Compose 中的容器路径 |
| 成品 STRM 目录 | `/strm` | Compose 中的容器路径 |
| 本地上传目录 | `/upload` | Compose 中的容器路径 |

播放设置：

| 配置项 | 示例 | 获取方式 |
|---|---|---|
| 客户端访问的网关地址 | `http://direct:9096` | 使用客户端可访问的网关服务名或 DNS 名称 |
| 播放前检查 CDN | 关闭 | 首次建议关闭，验证成功后再开启 |

CMS 设置全部关闭或留空：

```text
启用 CMS 播放接管: 关闭
旧播放源地址: 留空
继承 STRM 根目录: 留空
CMS Cookie: 留空
启用旧 /d 播放入口: 关闭
```

## 3. 干净环境：没有 Jellyfin

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
      ADMIN_USERNAME: ${ADMIN_USERNAME:-admin}
      ADMIN_PASSWORD: ${ADMIN_PASSWORD:?set ADMIN_PASSWORD}
    ports:
      - "${DIRECT_ADMIN_PORT:-9527}:9527"
      - "${DIRECT_GATEWAY_PORT:-9096}:9096"
    volumes:
      - ${HOST_DATA:?set HOST_DATA}:/data
      - ${HOST_PENDING:?set HOST_PENDING}:/pending
      - ${HOST_STRM:?set HOST_STRM}:/strm
      - ${HOST_UPLOAD:?set HOST_UPLOAD}:/upload
```

本系统仍然可以使用：

- 115 Cookie 和账号检查。
- 115 接收目录和分类目录。
- 分享转存。
- 待整理扫描。
- TMDB 识别和刮削。
- STRM、NFO、图片生成。
- 本地上传和上传后整理。
- 任务、运行和文件操作日志。

暂时不能使用：

- Jellyfin API 测试。
- Jellyfin 媒体库刷新和扫描。
- Jellyfin 播放。

Jellyfin 设置全部留空：

```text
协议: 留空
主机: 留空
端口: 留空
基础路径: 留空
API Key: 留空
```

CMS 设置也全部留空或关闭：

```text
启用 CMS 播放接管: 关闭
旧播放源地址: 留空
继承 STRM 根目录: 留空
CMS Cookie: 留空
启用旧 /d 播放入口: 关闭
```

以后增加 Jellyfin 时：

1. 部署 Jellyfin 服务。
2. 把 `${HOST_STRM}` 挂载为 Jellyfin 容器内的 `/media/115-strm`。
3. 在 Jellyfin 媒体库中添加 `/media/115-strm`。
4. 创建 Jellyfin API Key。
5. 在本系统填写 `http://jellyfin:8096` 对应的协议、主机和端口。
6. 点击「测试连接」并刷新 Jellyfin 媒体库。

## 4. 代理和日志设置

### 代理

入口：`系统设置 → 代理`。

只有目标网络需要代理时填写 HTTP、HTTPS 或 SOCKS5 地址；代理用途按 TMDB、图片、115、Jellyfin 分开选择。代理地址从网络出口服务或网络管理员处获取。

### 日志

入口：`系统设置 → 日志`。

默认使用 `info`；排查识别或播放问题时临时改为 `debug`。日志保留天数和总量上限按磁盘容量设置。

## 5. 首次配置顺序

1. 创建宿主机目录并替换 Compose 中的 `HOST_*` 占位符。
2. 启动本系统。
3. 配置管理员账号和密码。
4. 配置本系统 115 Cookie。
5. 在目录管理中选择 115 接收目录和分类目录。
6. 保存待整理、STRM 和上传目录。
7. 有 Jellyfin 时配置原始后端并测试 API。
8. 用单个分享或小文件测试转存、STRM 生成和播放。
9. 验证通过后再启用定时同步、上传监控和自动整理。

## 6. 播放链路

115 STRM 媒体：

```text
客户端 → direct:9096 → 115 CDN
```

本地硬盘媒体：

```text
客户端 → direct:9096 → Jellyfin → 本地硬盘
```

没有 Jellyfin 时只能执行接收、整理和 STRM 生成，不能进行 Jellyfin 播放验证。

## 7. 验收标准

有 Jellyfin 时：

- Jellyfin API 测试成功。
- Jellyfin 可以扫描 `/media/115-strm`。
- 单个 STRM 可以播放。
- 115 媒体播放可以取得 302 并进入 CDN。
- 本地硬盘媒体可以正常播放。
- 整理任务完成后可以触发 Jellyfin 刷新。

没有 Jellyfin 时：

- 115 Cookie 检查成功。
- 分享转存成功。
- 待整理目录可以读取。
- TMDB 识别和刮削成功。
- STRM、NFO、图片生成成功。
- 日志和任务状态可追踪。
- Jellyfin 相关设置保持空白。

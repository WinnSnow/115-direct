# 原片直链与单文件诊断

## 播放入口

Jellyfin 客户端连接播放网关，而不是直接连接后端 Jellyfin。115 媒体的 PlaybackInfo 保留源文件容器信息，返回带扩展名的播放地址：`/direct/{mediaID}/file.mp4` 或 `file.mkv`。`Path` 为绝对地址，`DirectStreamUrl` 为同源相对地址；二者都仅包含客户端签名和用户令牌，不包含 Jellyfin 后台探测签名。

网关覆盖视频和音频的 `stream`、`stream.{container}`、音频 `universal`、条目 `Download` 与 `File` 入口，并识别 `/emby`、`/jellyfin` 前缀。字幕、图片和普通本地媒体继续由 Jellyfin 处理。客户端令牌支持大小写不敏感的查询参数、`X-Emby-Token`、`X-MediaBrowser-Token`、MediaBrowser/Emby 授权头和 Bearer。

115 媒体不提供服务器转码、重新封装或 HLS/DASH 兜底。旧播放列表及分片请求返回 `409 direct_play_required`，不会被重定向成整个原文件。客户端应支持源文件容器、视频和音频编码；网关不修改编码信息来伪装兼容性。

## 设置

系统设置按用途展示播放相关配置：

- **Jellyfin 服务器**：填写媒体服务器的主机、端口和 API Key，默认端口为 `8096`。
- **目录配置 → 播放代理端口**：显示服务实际监听的端口，默认 `9096`。该端口代理上面配置的 Jellyfin 服务，播放器通过此代理访问媒体库和播放接口。端口由部署配置 `GATEWAY_ADDR` 管理；Docker 部署的播放器使用映射到宿主机的端口。
- **高级设置 → 自定义 STRM 连接地址**：保留独立部署时使用的内部连接地址，例如 `http://direct:9096`。已有配置继续用于生成 STRM，普通目录配置无需填写域名或容器名。

“高级设置 → 播放链路”还包括：

- 客户端公网地址：填写客户端实际连接的根地址，例如 `https://media.example.com`。用于生成 PlaybackInfo 的绝对地址，避免穿透服务改写 Host 导致内部地址泄漏。留空时使用本次请求的 Host 与协议，不使用任意 `X-Forwarded-Host`。
- 播放前检查 CDN：默认关闭。启用后按实际客户端 UA 做 `Range: bytes=0-0` 检查，只读一个字节，不携带 Cookie 或用户授权头、不跟随额外重定向。成功检查缓存一分钟，避免每次播放重复检查。

所有取链及检查请求经过同一个串行队列，相同媒体与 UA 合并并发请求。缓存失效时重新取链；明确过期或 CDN 返回 401/403/404/410 时最多重试一次。115 API 请求失败、限流、CDN 网络异常等不会无限重试，不回退代理视频。后台元数据探测保留。

## Jellyfin 地址与后台探测

“Jellyfin 服务器”支持宿主机地址（例如 `http://192.168.31.135:8096`）和容器服务地址（例如 `http://jellyfin:8096`）。Docker 端口映射下，服务器的访问地址可能与它回访播放代理时的来源地址不同。

后台读取 STRM 使用文件中的专用签名，无需播放器用户登录。网关先验证媒体签名与后台签名，再匹配已配置的服务器地址。来源不同时，通过已配置的后端和管理员 API Key 请求 `/System/Info`，确认服务器报告的内部地址。结果缓存一分钟，修改后端地址或 API Key 后重新查询；该请求不跟随重定向，也不向请求来源地址发送 API Key。

原片直链入口的授权失败会在响应头 `X-115-Direct-Error` 和播放事件阶段中记录具体原因：

| 状态 | 阶段 | 原因 |
| --- | --- | --- |
| 401 | `login_required` / `token_rejected` | 未携带登录令牌，或令牌失效 |
| 401 | `backend_signature_invalid` | 后台探测签名无效 |
| 403 | `playback_forbidden` | 用户缺少播放权限 |
| 403 | `backend_origin_mismatch` | 请求来源不属于已确认的 Jellyfin 地址 |
| 502 | `backend_origin_unavailable` / `user_authorization_unavailable` | 后端地址验证或用户授权服务异常 |

## 播放请求计时

播放信息、原片入口和播放上报请求会记录 `gateway request` 日志。每条请求分配独立的 `request_id`，也通过响应头 `X-115-Direct-Request-ID` 返回；日志不包含查询参数、用户令牌或签名直链。

- `request_started` / `request_finished`：同一请求在网关内的进入和处理结束时间。
- `elapsed_ms`：网关进入到处理结束的实测耗时，包含等待上游接口及原有日志写入的时间；在写入本条计时日志前采样。
- `headers_ready_ms`：进入网关到调用最终响应 `WriteHeader` 的耗时。`Server-Timing` 响应头的 `gateway` 指标使用此值。它不表示客户端收到响应或视频首帧的时刻。
- `authorization_ms` / `media_lookup_ms` / `settings_ms` / `link_resolve_ms`：鉴权、媒体查询、播放设置读取和本次请求等待直链结果的耗时。
- `backend_headers_ms` / `backend_calls`：请求 Jellyfin 到收到响应头的累计等待和调用次数；不包含读取响应体的时间。此等待可能包含在鉴权阶段内，不能与所有阶段直接相加。
- `playback_info_rewrite_ms`：读取、解析和改写 Jellyfin 播放信息响应体的耗时。
- `link_job_queue_ms` / `link_job_115_ms` / `link_job_cdn_ms`：取链任务的排队、请求 115 及 CDN 检查耗时。`cache=shared` 时它们描述共享任务的完整工作，实际归属于本次请求的等待使用 `link_resolve_ms`；缓存命中时这些任务耗时为零。

这些指标用于测量具体请求，不将不同请求之间的时间间隔归为网关处理耗时；并行请求的耗时也不应直接求和作为用户等待时间。

## 减少 Jellyfin 重复探测

Jellyfin 12.1 会在 STRM 的 PlaybackInfo 请求中强制刷新媒体信息，即使已有有效的音视频信息。本项目提供 [Jellyfin 播放扩展](../extensions/jellyfin-playback/README.md)，安装到 Jellyfin 后可以复用指纹未变、24 小时内验证过的媒体信息；需要探测时先使用 5MB 小采样，信息不足再扩大到 20MB。

该扩展保留 Jellyfin 原有播放响应生成流程，配置范围仅包含本项目 STRM。文档包含编译、安装、强制刷新、回滚方式及按请求 ID 记录的修复前后耗时。

## 单文件诊断

在“目录管理”切换 STRM 本地，打开一个 STRM 文件后点击“开始诊断”。默认按当前浏览器 UA 获取直链；也可填入播放器实际 UA。勾选“检查 CDN”时执行小范围读取。接口只接受 STRM 根目录内、本项目签名有效的单个文件，不接受任意远程 URL。

诊断分别展示媒体台账、115 取链、CDN 检查的失败阶段，以及缓存状态、取链次数、CDN 域名、HTTP 状态。不向管理页面或日志输出完整 CDN 签名链接、Cookie 或用户令牌。

CDN 检查是从服务器出口执行的，并不等于用户设备或用户外网出口验证。`206` 说明该次 CDN 小范围请求成功，`200` 说明 CDN 返回内容但未证明 Range 支持。单文件诊断中的 `client_playback_verified` 始终为 false。

## 播放事件

运行日志增加“播放事件”视图，显示当前进程最近 200 条事件。302、取链失败和分片拦截另有持久化的脱敏操作记录；事件明细在服务重启后清空。

- 已返回 302：网关取得链接并发出重定向，不代表客户端已下载或解码。
- 客户端上报开始/进度/停止：Jellyfin 客户端发送的播放状态，不代表已独立验证 CDN 流量。
- 真正的外网验收：在同一集上验证开始播放、拖动、暂停恢复与续播，检查实际播放器请求的目标为 115 CDN；测试过程中 Jellyfin 不应出现新的播放转码进程。

网关不改写 Jellyfin Web 的 JavaScript，也不在浏览器 Console 自动打印直链。浏览器播放网络路径查看 Network；原生或外部播放器查看自身网络记录。

# Jellyfin 播放探测优化

本扩展修复 Jellyfin 12.1 对 STRM 在每次 PlaybackInfo 请求中强制刷新媒体信息的问题。等待发生在 Jellyfin 内部，因此扩展安装在 Jellyfin 的持久化插件目录。115 Direct 网关继续使用原来的服务器地址和播放地址。

## 两处修复

1. **复用有效媒体信息**：只处理指定目录、指定网关来源、带项目签名的 STRM。文件路径、修改时间、长度、内容共同生成 SHA-256 指纹；指纹未变、视频尺寸/主音轨/时长有效、验证时间在 24 小时内时，调用 Jellyfin 原服务但关闭本次重复探测。同一条目的并发请求合并刷新，之后每个请求分别生成播放响应。
2. **缩小必要的探测**：优先使用 5,000,000 字节采样、5 秒分析上限、12 秒执行超时；失败或主媒体信息不足时，扩大为 20,000,000 字节、10 秒分析上限、20 秒执行超时，最多两档。保留原来的请求头、协议、章节提取和媒体信息归一化。取消/超时会终止本次启动的 ffprobe，不影响其他探测进程。

采样大小和分析时长是上限，不代表必然读取这么多数据或等待这么久。额外 av3a 音轨缺少编码识别时，只要主视频、其他音轨和时长可用，就不再反复扩大采样；扩展保留该音轨，不改变编码或客户端解码能力。

扩展通过 DI 装饰 `IMediaSourceManager` 和 `IMediaEncoder`，原有用户检查、设备配置、音轨处理和会话生成仍由 Jellyfin 执行，不缓存 PlaybackInfo 响应或用户令牌，不修改 Jellyfin 程序文件。

## 编译与验证

需要 .NET 10 SDK，以及**实际运行的 Jellyfin 12.1** 安装目录中的程序集。下面的命令在仓库根目录执行：

```bash
JELLYFIN_CONTAINER=115-direct-jellyfin
BUILD_ROOT=/var/tmp/115-direct-playback-build
mkdir -p "$BUILD_ROOT/jellyfin" "$BUILD_ROOT/package"
docker cp "$JELLYFIN_CONTAINER:/jellyfin/." "$BUILD_ROOT/jellyfin/"
dotnet build extensions/jellyfin-playback/DirectPlayback.csproj -c Release \
  -p:JellyfinDir="$BUILD_ROOT/jellyfin" -o "$BUILD_ROOT/package"
dotnet run --project extensions/jellyfin-playback/tests/PolicyTests.csproj \
  -p:JellyfinDir="$BUILD_ROOT/jellyfin"
cp extensions/jellyfin-playback/meta.json "$BUILD_ROOT/package/"
```

测试覆盖范围边界、指纹变化、过期、未知附加音轨、回退、取消及子进程清理、并发合并和强制刷新。

## 安装

GitHub Actions 的 `CI` 流程会编译并测试扩展，成功后提供 `jellyfin-playback-12.1` 构建产物，其中包含插件 DLL、清单和本说明。使用其他 Jellyfin 版本时，按上面的步骤针对实际安装目录重新编译。

先备份 Jellyfin 配置和数据库，再将 `DirectPlayback.dll`、`meta.json` 放入 `/config/plugins/115 Direct Playback_0.1.0.0/`，重启 Jellyfin。插件目录必须使用持久化挂载。启动日志应包含：

```text
Loaded plugin: "115 Direct Playback" "0.1.0.0"
115 Direct playback policy enabled=True ...
```

不要将 Jellyfin 依赖 DLL 一并安装到插件目录。升级 Jellyfin 后需要重新编译并验证服务签名、探测参数和取消行为；编码器接口不匹配时，扩展记录警告并使用原编码器。

卸载时停止 Jellyfin，移走该插件目录后再启动。原有媒体信息、STRM、用户数据均不需要删除。

## 配置与失效

首次启动生成 `/config/plugins/configurations/115-direct-playback.json`，修改后重启 Jellyfin：

```json
{
  "Enabled": true,
  "StrmRoots": ["/media/115-strm"],
  "GatewayOrigins": ["http://direct:9096"],
  "InitialProbeBytes": 5000000,
  "InitialAnalyzeSeconds": 5,
  "InitialTimeoutSeconds": 12,
  "ExtendedProbeBytes": 20000000,
  "ExtendedAnalyzeSeconds": 10,
  "ExtendedTimeoutSeconds": 20,
  "MetadataMaxAgeHours": 24
}
```

`StrmRoots` 使用 Jellyfin **容器内路径**，`GatewayOrigins` 使用 STRM 文件实际写入的来源地址。其他来源、本地媒体和非项目 STRM 保持原处理。`Enabled=false` 关闭两项优化；配置无效时记录错误并关闭优化，保留 Jellyfin 服务。

指纹及验证时间存放于 `/config/data/115-direct-playback/metadata.json`，不保存签名 URL 或令牌。首次加载可以使用 Jellyfin 已持久化、刷新时间晚于 STRM 修改时间的有效信息；复用不会延长有效期。

STRM 变化、信息不足或验证超过 24 小时会自动重新探测。若远端文件变更而 STRM 内容和属性均未变化，需等待过期或主动重新探测。Jellyfin 的普通“刷新元数据”仍可使用，但其 12.1 API 不一定开启远端媒体探测；可以为某一条目设置一次性标记，**无需重启**：

```bash
JELLYFIN_ITEM_ID=替换为条目的32位ID
docker exec 115-direct-jellyfin mkdir -p /config/data/115-direct-playback/refresh
docker exec 115-direct-jellyfin touch "/config/data/115-direct-playback/refresh/$JELLYFIN_ITEM_ID"
```

条目 ID 使用 32 位无连字符 GUID。下次 PlaybackInfo 将重新探测；成功取得可用信息后清除标记，失败时保留标记。

## 实测记录

2026-10-17，《交锋》第 23 集，同一 PlaybackInfo 接口：

| 场景 | 网关内完整请求 | 说明 |
| --- | ---: | --- |
| 修复前用户播放 | 16,144.153 ms | `dm764s1wqi1o-3`，等待后端响应头 16,130.856 ms |
| 修复后，Jellyfin 刚启动 | 549.497 ms | `dm76ukfcrisx-o`，复用已有信息 |
| 随后两次请求 | 54.042 / 32.406 ms | `dm76ukp42zhx-p` / `dm76ukqeyhm4-q` |
| 人为过期后重新探测 | 5,292.041 ms | `dm76w1h621nn-s`，其中小采样探测 2,012.486 ms |
| 重新探测后两次复用 | 33.007 / 21.220 ms | `dm76w3x3bgd4-w` / `dm76w3y13a1q-x` |
| 最终版强制刷新、4 请求并发 | 1,392.329–1,426.823 ms | 只执行一次探测，探测 1,270.570 ms；4 个独立播放会话 |
| 最终版随后复用 | 21.837 ms | `dm76yy5822c0-1l` |
| 用户再次实际播放，22:57 | 26.472 ms | `dm779hwqlizk-1m`，复用媒体信息 4.913 ms |

这是不同请求的逐条实测，不是日志行之间的时间间隔，也不是播放器出首帧的时间。复用请求保持 5 条原有轨道和 2763.050667 秒时长；另外验证了真实用户令牌、独立播放会话、未登录 401，以及视频 Range 返回 206。

最终并发验收同时从进程列表捕获到实际 ffprobe 参数 `-analyzeduration 5000000 -probesize 5000000`。强制刷新标记成功清除，插件状态为 `Active`，Jellyfin 健康检查为 `Healthy`。

22:57 用户播放的首次视频取链请求 `dm779j1qvlrw-1o` 在网关内耗时 284.332 ms，其中请求 115 为 242.514 ms；之后两次命中缓存的取链请求为 42.500 / 39.280 ms。本次没有重新探测、鉴权失败或取链重试。服务端日志不覆盖 302 之后的 CDN 连接、媒体读取、缓冲和首帧时间，剩余体感等待需结合客户端记录继续定位。

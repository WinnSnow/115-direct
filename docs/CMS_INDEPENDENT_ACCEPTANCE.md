# 独立 115 直链验收

该流程验证旧 CMS `/d/{pickcode}` 地址由独立 115 解析器返回 302。它使用本地隔离实例，不启动正式同步、整理、上传、清理或企业微信 worker。

## 输入与隔离

- 目录 CID、文件 ID、pickcode 和祖先路径从本地私有范围文件读取。
- 只允许白名单中的 pickcode，范围外请求返回 404。
- 115 传输层只放行状态检查和下载链接读取接口，禁止登录、注销、上传、复制、移动、删除和目录写入。
- Cookie、OAuth token、主密钥、原始响应和截图保存在仓库外的 0700 临时目录。

## 启动示例

```bash
go build -o /var/tmp/cms-acceptance ./cmd/cms-acceptance
/var/tmp/cms-acceptance \
  -data=/var/tmp/115-direct-cms-independent \
  -mode=independent-115 \
  -gateway=127.0.0.1:29096 \
  -admin=127.0.0.1:29527 \
  -legacy=127.0.0.1:29528 \
  -backend=http://jellyfin.example.test:8091 \
  -cms=http://cms.example.test:9527 \
  -public=http://127.0.0.1:29096
```

测试脚本通过 `CMS_ACCEPTANCE_DIR`、`CMS_URL`、`JELLYFIN_URL` 和本地白名单文件注入环境。仓库只保留模拟测试，不保留任何真实目录、媒体、账号或网络地址。

## 验收项目

验证 302、Range/206、来源范围、票据绑定、缓存、请求预算、无 CMS 回退以及所有远端写入拦截。完成后停止隔离实例并删除临时凭据目录。

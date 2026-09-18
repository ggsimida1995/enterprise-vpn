# 企业内网客户端

这是独立客户端项目。它不管理 EasyTier 网络参数：用户打开客户端后仅登录，客户端从服务端获取运行配置，并启动随包携带的独立 `easytier-core`。

## 打包

客户端包必须在构建时固定服务端地址，并携带 EasyTier Core。先从 EasyTier 官方发布页下载指定版本的 Core：

```sh
cd client
EASYTIER_VERSION=v2.6.4 ./scripts/fetch-easytier-core.sh
SERVER_URL=https://vpn.example.com \
EASYTIER_CORE_DIR=./cores \
VERSION=0.3.0 \
./scripts/package-client.sh
```

生成的 `dist/` 仅包含：

- macOS Apple Silicon `.app` 压缩包
- macOS Intel `.app` 压缩包
- Windows x64 `.exe` 压缩包

用户不需要填写服务端地址、EasyTier 网络名、密钥、节点或路由。`-server`、`-core` 和 `-ui cli` 仅用于开发和故障排查。

## GitHub Release

在仓库 Variables 中设置 `VPN_SERVER_URL` 为部署好的服务端 HTTPS 地址。推送 `v*` 标签后，GitHub Actions 会下载 EasyTier Core、打包 macOS/Windows 客户端并创建 Release。

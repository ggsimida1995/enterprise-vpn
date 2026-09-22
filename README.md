# enterprise-vpn Tauri 2 桌面客户端

Rust 重构版企业内网客户端，使用 Tauri 2 + 原生 HTML 登录窗口，后台连接服务端并启动随包携带的 `easytier-core`。

## 本地开发

```bash
cd client-rust
./scripts/fetch-easytier-core.sh
cargo tauri dev
```

## 打包

```bash
cd client-rust
./scripts/fetch-easytier-core.sh
VPN_SERVER_URL=https://vpn.example.com TARGETS=native ./scripts-build.sh
```

生成的安装包位于 `dist/`。跨平台构建应在对应平台 runner 上执行；`TARGETS` 支持 `darwin/arm64`、`darwin/amd64`、`windows/amd64`。

未指定 `VPN_SERVER_URL` 时，默认连接 `http://172.22.159.5:4096`。

客户端通讯日志写入：

`~/Library/Application Support/EnterpriseVPN/client.log`

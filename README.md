# 企业内网访问客户端

第一阶段只实现一条链路：客户端登录，服务端按用户权限生成 EasyTier 配置，客户端自动启动底层 `easytier-core`。客户端不提供任何网络参数编辑入口。

## 启动服务端

```sh
go run ./server -addr :8080 -config ./server.json
```

服务端源代码在 `server/`，首次启动会在 `-config` 指定的位置创建配置文件。可以先复制仓库中的 `server.example.json`：

```sh
cp server.example.json server.json
```

模板账号是 `demo` / `demo`。修改密码时不要手工填写哈希，直接执行：

```sh
go run ./server -config ./server.json -set-password demo
```

该命令隐藏输入密码并只保存哈希。账号的 `network_ids`、网络的 `secret`、`subnets`、`proxy_networks` 和 `gateway_device_ids` 都在 `server.json` 中配置；修改后客户端下一次心跳会自动获取新配置。

服务端同时提供最小 Web 配置后台：

```sh
export VPN_ADMIN_USER=admin
read -s VPN_ADMIN_PASSWORD; export VPN_ADMIN_PASSWORD
go run ./server -addr :8080 -config ./server.json
```

浏览器打开 `http://127.0.0.1:8080/admin`，使用上面的后台账号登录。后台配置的是用户权限和 EasyTier 网络参数，不是 Core 可执行文件路径。

服务端是 HTTP API，可直接放在 HTTPS 反向代理后。也可以使用仓库自带的容器部署：

```sh
docker build -t enterprise-vpn-server .
docker run -d --name enterprise-vpn-server \
  -e VPN_ADMIN_USER=admin -e VPN_ADMIN_PASSWORD \
  -p 8080:8080 -v enterprise-vpn-data:/data \
  enterprise-vpn-server
```

容器把运行状态保存到 `/data/server.json`，重启或更新镜像不会丢失用户、设备和网络配置。

## 启动客户端

运行客户端即可，默认会打开本机登录页，用户只输入账号和密码。登录页只监听 `127.0.0.1`，不会提供任何 EasyTier 参数。无桌面环境可显式使用命令行登录。`easytier-core` 地址不是服务端配置：它必须存在于运行客户端或 Gateway 的机器上。客户端会优先使用 `VPN_CORE_PATH`，其次查找安装包同目录的 Core，最后回退到 `PATH`：

```sh
go run ./client -server http://127.0.0.1:8080
# 无桌面环境
go run ./client -ui cli -server http://127.0.0.1:8080
# 手工指定 Core 路径
go run ./client -core /opt/easytier-core -server http://127.0.0.1:8080
```

客户端首次运行会生成并持久化 Device ID，登录后把服务端返回的临时 TOML 交给 `easytier-core`。客户端每 15 秒发送心跳并检查 Core 生命周期：配置 revision 变化或 Core 异常退出时自动刷新/重启；临时服务端网络故障会继续重试，会话失效时自动停止 Core；退出登录或进程收到终止信号时也会停止 Core。

## 构建客户端

本地构建各平台客户端：

```sh
VERSION=0.1.0 ./scripts/build-client.sh
```

默认生成 Windows、macOS Intel/Apple Silicon 和 Linux amd64/arm64 构建。若要把各平台官方 `easytier-core` 一起打包，准备如下目录后执行 `EASYTIER_CORE_DIR=... ./scripts/package-client.sh`：

```text
easytier-core-darwin-arm64
easytier-core-darwin-amd64
easytier-core-windows-amd64
easytier-core-linux-amd64
easytier-core-linux-arm64
```

推送 `v*` Git tag 后，GitHub Actions 会自动构建并上传各平台客户端。服务端仍作为独立 Go API 部署，建议放在 HTTPS 反向代理后面。

发布包中客户端会自动发现同目录的独立 `easytier-core`（Windows 为 `easytier-core.exe`）。macOS 包为可双击的 `Enterprise VPN.app`，Windows 包为 `.exe`；用户不需要填写 Core 路径或任何 EasyTier 参数。Core 由 EasyTier 独立发布，打包时可通过 `EASYTIER_CORE_DIR` 放入客户端压缩包。

服务端 JSON 中每个用户的 `network_ids` 是网络授权来源；可选的 `allowed_subnets` 可把权限继续收窄到单个子网。网络的 `subnets`、`proxy_networks`、`virtual_cidr`、`peer_nodes` 和 `relay_nodes` 由服务端控制。下一次客户端心跳会热加载新授权并返回新的配置 revision。热加载文件可以省略 `devices` 和 `sessions`，运行中的设备租约和会话会由服务端保留。客户端响应只展示可访问网段，不展示网络密钥、节点或路由编辑项。

## Gateway / Subnet Proxy

Gateway 也是普通客户端程序，但只有被管理员列入 `gateway_device_ids` 的设备，才会收到 EasyTier 的 `[[proxy_network]]` 配置。普通用户设备收到的 TOML 始终是 `routes = []`，由 EasyTier 自动同步 Gateway 发布的子网路由。

管理员可在 `server.json` 中配置：

```json
{
  "networks": {
    "company": {
      "id": "company",
      "name": "公司内网",
      "secret": "由管理员保管的网络密钥",
      "virtual_cidr": "10.144.0.0/16",
      "subnets": ["192.168.10.0/24", "192.168.20.0/24"],
      "gateway_device_ids": ["Gateway 首次登录后生成的 Device ID"],
      "proxy_networks": [
        {"cidr": "192.168.10.0/24", "allow": ["tcp", "udp", "icmp"]},
        {"cidr": "192.168.20.0/24", "allow": ["tcp", "udp"]}
      ]
    }
  }
}
```

`proxy_networks` 省略时，Gateway 会自动把 `subnets` 转换为 Subnet Proxy。需要地址映射时可增加同前缀长度的 `mapped_cidr`。Gateway 设备必须同时出现在某个用户的 `network_ids` 中，才能获取对应网络配置。

如果同一 EasyTier 网络内需要区分用户权限，可在用户上增加子网白名单：

```json
{
  "id": "user-a",
  "username": "alice",
  "network_ids": ["company"],
  "allowed_subnets": {
    "company": ["192.168.10.0/24"]
  }
}
```

服务端会自动生成 EasyTier Forward ACL：允许白名单网段，其他转发目标默认丢弃。省略 `allowed_subnets` 时，用户可访问该网络声明的全部 `proxy_networks`；Gateway 设备不受用户子网白名单限制。配置了 `mapped_cidr` 时，管理员仍按真实内网 `cidr` 授权，但客户端显示和 ACL 使用用户实际访问的映射网段。

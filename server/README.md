# 企业内网服务端

这是独立服务端项目，部署到服务器。它提供：账号密码验证、设备记录、用户权限、EasyTier 运行配置生成和 Web 配置后台。

## 本地运行

```sh
cd server
cp server.example.json server.json
export VPN_ADMIN_USER=admin
read -rs VPN_ADMIN_PASSWORD; export VPN_ADMIN_PASSWORD
go run . -addr :8080 -config ./server.json
```

后台地址是 `http://服务器地址:8080/admin`。后台配置用户、网络密钥、Peer、Relay、Gateway、子网代理及用户可访问网段；保存后客户端通过心跳自动获取更新。

修改本地账号密码：

```sh
go run . -config ./server.json -set-password demo
```

## 服务端发布包

服务端只发布 Linux 包，先发布服务端并部署，再打客户端：

```sh
cd server
VERSION=server-v0.1.0 ./scripts/package-server.sh
```

在服务器上解压对应架构的包后执行：

```sh
cp server.example.json server.json
export VPN_ADMIN_USER=admin
read -rs VPN_ADMIN_PASSWORD; export VPN_ADMIN_PASSWORD
./run-server.sh
```

`server-v*` 标签会触发独立的服务端 GitHub Release。`linux-amd64` 适用于大多数 x86 云服务器；ARM 服务器使用 `linux-arm64`。

## Docker 部署

```sh
cd server
./scripts/build-image.sh
docker run -d --name enterprise-vpn-server \
  -e VPN_ADMIN_USER=admin \
  -e VPN_ADMIN_PASSWORD \
  -p 8080:8080 \
  -v enterprise-vpn-data:/data \
  enterprise-vpn-server:latest
```

将服务端置于 HTTPS 反向代理之后，并设置高强度后台密码。`/data/server.json` 是持久化配置，不应提交到 Git。

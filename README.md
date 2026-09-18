# Enterprise VPN

仓库包含两个完全独立的项目：

- [`client/`](./client)：只构建和发布 macOS/Windows 客户端。用户只登录，客户端接收服务端配置并调用独立 EasyTier Core。
- [`server/`](./server)：只部署到服务器，提供 Web 后台、用户权限和 EasyTier 运行配置下发。

两个目录各自拥有 Go module、依赖、README、构建脚本和部署方式，不共享构建产物。

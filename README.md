# MC WebSocket Proxy

Minecraft服务器WebSocket代理工具，用于通过Cloudflare等CDN转发Minecraft流量。

## 功能

- **入口模式(entry)**：接收玩家TCP连接，转发到WebSocket服务器
- **出口模式(exit)**：接收WebSocket连接，转发到本地Minecraft服务器

## 下载

从[Releases](https://github.com/YOUR_USERNAME/mc-ws-proxy/releases)页面下载对应系统的二进制文件：

- `mc-ws-proxy-linux-amd64` - Linux x64
- `mc-ws-proxy-linux-arm64` - Linux ARM64
- `mc-ws-proxy-windows-amd64.exe` - Windows x64
- `mc-ws-proxy-darwin-amd64` - macOS Intel
- `mc-ws-proxy-darwin-arm64` - macOS Apple Silicon

## 使用方法

### 入口机（玩家端）

```bash
./mc-ws-proxy -mode entry -listen :25565 -ws wss://mc.example.com/ws
```

参数说明：
- `-mode entry` - 入口模式
- `-listen :25565` - 监听端口（玩家连接此端口）
- `-ws wss://mc.example.com/ws` - WebSocket服务器地址

### 出口机（服务器端）

```bash
./mc-ws-proxy -mode exit -exit-listen :8080 -exit-target 127.0.0.1:25565
```

参数说明：
- `-mode exit` - 出口模式
- `-exit-listen :8080` - WebSocket监听端口
- `-exit-target 127.0.0.1:25565` - Minecraft服务器地址

## 编译

```bash
go build -o mc-ws-proxy .
```

## 发布新版本

推送tag即可自动编译并发布：

```bash
git tag v1.0.0
git push origin v1.0.0
```

## License

MIT
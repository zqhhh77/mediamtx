# WebSocket 编码视频输出

在现有 RTSP 发布、WebRTC 和 HLS 之外，新增 MiBeeNvr 视频协议兼容的 WebSocket 读取出口。直接订阅 tdt-media-stream 的共享 path，不另建 RTSP 拉流客户端，不解码、不转码。默认关闭。

```yaml
ws: true
wsAddress: :8894
paths:
  camera/001:
```

发布端继续推送 `rtsp://主机:8554/camera/001`。观看端使用 `ws://主机:8894/camera/001`，同一路径可以同时通过 WebRTC、HLS、RTSP、WS 读取。多级目录原样保留。环境变量为 `MTX_WS=true`、`MTX_WSADDRESS=:8894`；配置文件和现有配置 API 的修改会重建 WS 监听器并关闭旧连接。

每个 WS 连接注册为现有 path 的一个 reader，复用 read 权限、按需源、最大读取者限制、`runOnRead` / `runOnUnread`。`MTX_READER_TYPE=wsSession`，`MTX_READER_ID` 为连接 UUID。path API 的 readers 会显示此类型；没有新增独立 WS 会话管理 API。原本仅匹配 `webRTCSession` 的外部关键帧脚本需要自行适配，tdt-media-stream 不调用厂商 SDK。

## 鉴权与反向代理

支持现有 HTTP Basic / Bearer 请求头；浏览器原生 WebSocket 可用 URL 查询参数 `user` / `pass` 或 `token`。例如 `new WebSocket('wss://video.example.com/ws/camera/001?token=' + encodeURIComponent(token))`。外部 HTTP 鉴权收到 `protocol=ws`、`action=read`，凭据进入原有认证流程；JWT 的过期时间也会终止会话。tdt-media-stream 的 HTTP 调试日志会隐去上述查询凭据；代理也应避免记录带凭据的完整 URL。查询仍按原有规则传给鉴权服务和读取钩子，钩子应按敏感信息处理。

WS 不校验 Origin，允许任意来源的浏览器连接，无需配置来源白名单。读取权限由现有 read 鉴权控制。不使用转发头替代鉴权 IP，WS 看到的是直接连接方地址。TLS 由代理终止：

```nginx
location /ws/ {
    proxy_pass http://127.0.0.1:8894/;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
    proxy_read_timeout 75s;
}
```

升级前，参数/编码错误返回 400、鉴权失败 401、无流 404、依赖不可用 503，HTTP 错误体统一为 `{"code":状态码,"message":"中文说明","data":null}`。升级后使用二进制媒体消息。

## 消息合同

多字节整数均为大端序，一个 binary message 对应一条消息：

| 类型 | 内容 |
| --- | --- |
| `0x01` CodecInfo | `type:u8, codec:u8, profile:u8, level:u8, sps_length:u16, sps, pps_length:u16, pps`；H.265 追加 `vps_length:u16, vps` |
| `0x02` VideoFrame | `type:u8, pts:u64, key:u8, nalu_count:u16`，随后重复 `nalu_length:u32, nalu` |
| `0xff` EOS | 结束提示，尽力发送后关闭 |
| `0x04` 客户端控制 | 单字节关键帧请求，识别后等待自然关键帧；当前共享接口没有上游请求能力，不返回伪造的成功响应 |

codec 值 4 为 H.264，5 为 H.265。profile/level 与参考服务一致，分别使用 SPS 的字节 1、3；H.265 的真实解码配置仍由完整 VPS/SPS/PPS 决定。NALU 不带起始码或额外 AVCC 长度。每条 VideoFrame 是完整 AU，不是 RTP 包。PTS 保留原始 90kHz 时间关系及解码顺序，首个关键帧归一化至 `2^32`，为 B 帧重排留出前置时间；倒退超过 10 秒认为时间线重置并结束连接。不填入无法可靠取得的 ingest_at，不发送 QualityInfo、音频或业务协商。

首帧前等待完整参数集和新的随机访问帧，最多等待全局 `readTimeout`。H.265 CRA 起播丢弃其前导 RADL/RASL；编码参数改变时结束连接，由播放器重连。单帧最大 32 MiB，参数集和 NALU 数量受协议 u16 上限约束。发送使用现有 `writeQueueSize` 和 `writeTimeout`；WS 队列满直接退出，不丢失参考帧后继续发送。单个连接不会阻塞共享输入或其他协议。控制消息限制为 64 字节；只接受单字节 `0x04`，其它消息结束连接。30 秒 ping，40 秒未收到 pong 退出。

## 播放及验证

使用 `websocket-player.html`，通过本机 localhost HTTP 服务或 HTTPS 打开，在输入框填入 WS/WSS 地址。它使用浏览器 WebCodecs，显示解码帧数及错误；不包含 H.265 WASM 软件解码器。浏览器不支持 H.265 时应使用已有 MiBeeNvr 播放器的 WASM 路径。普通 `<video src="ws://...">` 不能解码此格式。

GitHub Actions 的 `websocket` workflow 执行竞态测试、现有输出服务器回归，生成 `tdt-media-stream-linux-amd64` 与 `tdt-media-stream-windows-amd64.exe` 及本说明和验证页。集成测试以 RTSP 发布，核对三个 WS 共享读取者、H.264/H.265 首包/首帧、100 次加入退出及源退出 EOS。浏览器测试还保存真实 WS 消息，将 NALU 还原为 Annex B 后用 FFmpeg 验证连续解码；这是 CI 验证工具，服务端输出不依赖 FFmpeg。请以实际 Actions 运行结果判定构建是否通过。

现场验收仍需：真实摄像机、MiBeeNvr 解析/硬解/WASM 分别验证、跨域连接与外部鉴权、慢连接与 WebRTC/HLS 同时观看、至少 30 分钟播放、首帧 10 次中位数/P95 和可靠画面时钟延迟。单元/集成测试不等于设备或延迟验收，也不预先承诺 WS 比 WebRTC 快。

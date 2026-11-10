# tdt-media-stream WebSocket 推流与客户端对接

本文供业务后端和播放器前端对接使用。发布端继续通过 RTSP 推流，观看端通过 WebSocket 接收 H.264 / H.265 编码视频。WS 与 WebRTC、HLS、RTSP 读取同一条共享流；每增加一个观看者，只增加一个 reader，不会重新拉取一路 RTSP 输入。

当前 WS 只输出视频，不输出音频，也不在服务端解码或转码。它是自定义二进制协议，不能直接写到 `<video src="ws://...">` 中播放。

## 1. 服务配置与 RTSP 推流

在现有 `mediamtx.yml` 中合并以下配置，已有鉴权和路径配置按实际部署保留：

```yaml
rtsp: true
rtspAddress: :8554

ws: true
wsAddress: :8894

paths:
  camera/001:
    source: publisher
```

上例中，`camera/001` 是流路径，由业务后端分配。不同摄像机使用不同路径，同一摄像机的多个观看者使用同一路径。

发布端向下面的 RTSP 地址持续推送 H.264 或 H.265 视频：

```text
rtsp://192.168.1.100:8554/camera/001
```

如果已经有程序或摄像机在向该路径推流，直接复用这一路输入。WS 观看端不需要再次推流，也不需要创建另一条流。

FFmpeg 推流命令示例（在发布设备执行，按需替换输入文件和地址）：

```bash
ffmpeg -re -stream_loop -1 -i input.mp4 -an -c:v copy -rtsp_transport tcp -f rtsp rtsp://192.168.1.100:8554/camera/001
```

`-c:v copy` 保留原视频编码，输入视频须为 H.264 或 H.265。若启用了发布鉴权，RTSP 地址使用发布账号，例如 `rtsp://publisher:password@192.168.1.100:8554/camera/001`；WS 观看凭据应使用具有 read 权限的账号或令牌。

## 2. 根据推流路径生成 WS 地址

### 2.1 地址规则

```text
RTSP 推流地址 = RTSP 发布入口 + / + 流路径
WS 播放地址   = WS 公开入口   + / + 同一流路径
```

| 场景 | RTSP 推流地址 | WS 播放地址 |
| --- | --- | --- |
| 直连服务器 | `rtsp://192.168.1.100:8554/camera/001` | `ws://192.168.1.100:8894/camera/001` |
| 多级路径 | `rtsp://192.168.1.100:8554/site/a/camera/001` | `ws://192.168.1.100:8894/site/a/camera/001` |
| HTTPS 页面经代理观看 | `rtsp://192.168.1.100:8554/camera/001` | `wss://video.example.com/ws/camera/001` |

直连 WS 服务时，路径就是 `/camera/001`，没有额外的 `/ws`、`/play` 或文件后缀。表中 `/ws/` 是反向代理公开前缀，代理转发时会去掉它，配置见第 6 节。

**无需调用“生成 WS 地址”接口。** 当前服务根据连接 URL 中的路径查找共享流，业务后端可以直接拼接并返回地址。请分别配置 RTSP 发布入口和 WS 公开入口：内网推流 IP、浏览器可访问的域名和端口可能不同，不要只对 RTSP URL 替换协议。

地址可提前生成；实际连接时，该路径需要有可用的视频源。URL 中的发布账号、密码和查询参数不应直接复制到 WS 地址。

### 2.2 地址生成示例

以下 JavaScript 可放在业务后端或前端。`streamPath` 传入未做 URL 编码的业务路径，`wsBase` 由部署配置提供：

```javascript
function buildWSUrl(wsBase, streamPath, { token, user, pass } = {}) {
  const url = new URL(wsBase);
  if (url.protocol !== 'ws:' && url.protocol !== 'wss:') {
    throw new Error('WS 入口必须使用 ws:// 或 wss://');
  }
  if (url.username || url.password || url.search || url.hash) {
    throw new Error('WS 入口不能包含账号、查询参数或片段');
  }
  const parts = streamPath.split('/');
  if (parts.some(p => !p || p === '.' || p === '..')) {
    throw new Error('流路径不能为空或包含空目录、.、..');
  }
  if (token != null && (user != null || pass != null)) {
    throw new Error('请选择 token 或账号密码中的一种鉴权方式');
  }
  url.pathname = url.pathname.replace(/\/$/, '') + '/' +
    parts.map(encodeURIComponent).join('/');
  if (token != null) {
    url.searchParams.set('token', token);
  } else {
    if (user != null) url.searchParams.set('user', user);
    if (pass != null) url.searchParams.set('pass', pass);
  }
  return url.toString();
}

buildWSUrl('ws://192.168.1.100:8894', 'camera/001');
// ws://192.168.1.100:8894/camera/001

buildWSUrl('wss://video.example.com/ws/', 'camera/001', { token: 'YOUR_TOKEN' });
// wss://video.example.com/ws/camera/001?token=YOUR_TOKEN
```

流路径本身仍需符合服务端路径校验规则；URL 编码不能把不合法的业务路径变成合法路径。

## 3. 客户端连接与鉴权

### 3.1 建立连接

```javascript
const ws = new WebSocket('ws://192.168.1.100:8894/camera/001');
ws.binaryType = 'arraybuffer';

ws.onopen = () => console.log('已连接，等待编码信息和首个关键帧');
ws.onmessage = ({ data }) => {
  // data 为 ArrayBuffer，按第 4 节解析，再交给视频解码器。
};
ws.onerror = () => console.error('连接失败，请检查地址、鉴权和服务日志');
ws.onclose = () => console.log('连接结束，释放解码器和画面资源');

// 用户退出观看时调用：
// ws.close();
```

连接 URL 已指定订阅的流，**建立连接后不发送订阅 JSON、播放命令或应用层心跳**。服务器会先等待参数集和新的关键帧，再发送 CodecInfo 和 VideoFrame。`onopen` 只表示握手完成，不代表已经显示首帧。

WS 不校验 Origin，无需配置 `wsAllowOrigins`。HTTPS 页面使用 `wss://`；浏览器可能阻止 HTTPS 页面连接明文 `ws://`。

### 3.2 鉴权方式

| 客户端 | 方式 | 示例 |
| --- | --- | --- |
| 浏览器，无需凭据的 read 权限 | 不带参数 | `ws://host:8894/camera/001` |
| 浏览器，账号密码 | 查询参数 `user` / `pass` | `wss://video.example.com/ws/camera/001?user=viewer&pass=PASSWORD` |
| 浏览器，令牌 | 查询参数 `token` | `wss://video.example.com/ws/camera/001?token=TOKEN` |
| 可自定义请求头的客户端 | HTTP Basic 或 Bearer | `Authorization: Basic ...` 或 `Authorization: Bearer TOKEN` |

浏览器原生 `new WebSocket()` 不提供自定义 Authorization 请求头的参数，因此使用查询参数。参数值通过 `URLSearchParams` 或 `encodeURIComponent` 编码。凭据是否有效，由当前 `authMethod` 及 read 权限决定；增加 `token` 参数本身不会开启或替代服务端鉴权。

服务端优先使用请求头中的凭据，只有请求头没有识别到凭据时才读取上述查询参数。业务端选用一种方式即可。带凭据的 URL 应按敏感信息处理，代理日志避免记录完整查询串。

使用现有外部 HTTP 鉴权时，WS 读取请求同样进入原鉴权服务，其中 `protocol` 为 `ws`，`action` 为 `read`，`path` 为 `camera/001`。鉴权服务若按协议过滤，需要允许 `ws` 读取。

### 3.3 握手失败

升级成功的 HTTP 状态为 `101 Switching Protocols`，之后传输二进制消息。升级前的业务失败使用统一 JSON：

```json
{"code":401,"message":"读取鉴权失败","data":null}
```

| HTTP 状态 | 常见原因 | 处理建议 |
| --- | --- | --- |
| 400 | 路径不合法，或视频轨不是 H.264 / H.265 | 核对流路径及发布编码 |
| 401 | read 鉴权失败 | 核对凭据、权限和外部鉴权结果 |
| 404 | 流不存在或尚未就绪 | 确认 RTSP 发布端正在向同一路径推流 |
| 503 | 流暂不可用等其他读取失败 | 检查源、读取者限制和服务日志 |

浏览器 WebSocket API 不向脚本暴露失败握手的 HTTP 响应体，通常只能收到 `error` / `close`。通过浏览器开发者工具的网络面板、代理日志或服务日志定位具体原因，不要在前端假设能从 `onerror` 中读取上述 JSON。

## 4. WS 二进制消息协议

所有多字节整数均为**大端序**。一个 WebSocket binary message 就是一条协议消息；不要把不同消息拼成字节流后再猜边界。当前不发送文本消息，也没有子协议协商。

### 4.1 CodecInfo：`0x01`

连接起播时先发送一次，客户端据此创建解码器。

| 字段 | 长度 | 说明 |
| --- | --- | --- |
| type | 1 字节 | 固定 `0x01` |
| codec | 1 字节 | `4` = H.264，`5` = H.265 |
| profile | 1 字节 | SPS 字节下标 1 的值 |
| level | 1 字节 | SPS 字节下标 3 的值 |
| sps_length | 2 字节 | SPS 长度 |
| sps | sps_length | SPS 原始 NALU |
| pps_length | 2 字节 | PPS 长度 |
| pps | pps_length | PPS 原始 NALU |
| vps_length | 2 字节 | 仅 H.265 存在 |
| vps | vps_length | 仅 H.265 存在，VPS 原始 NALU |

参数集没有 Annex B 起始码。H.265 的 profile / level 字段为兼容现有协议保留，实际解码配置应从完整 VPS / SPS / PPS 解析。H.264 的 WebCodecs codec 字符串可由 SPS 的第 1～3 字节生成，例如 `avc1.640028`。

### 4.2 VideoFrame：`0x02`

| 字段 | 偏移 / 长度 | 说明 |
| --- | --- | --- |
| type | 0 / 1 字节 | 固定 `0x02` |
| pts | 1 / 8 字节 | 无符号 64 位整数，90 kHz 显示时间戳 |
| key | 9 / 1 字节 | `1` = 随机访问关键帧，`0` = 非关键帧 |
| nalu_count | 10 / 2 字节 | 本帧 NALU 数量 |
| nalu_length + nalu | 从偏移 12 开始重复 | 每个 NALU 为 `4 字节长度 + 原始内容` |

每条 VideoFrame 是一个完整 Access Unit（AU），应整体提交给解码器。NALU 内容既没有起始码，也不包含额外的 AVCC 长度。要向接受 Annex B 的解码器提交时，给每个 NALU 添加 `00 00 00 01` 再合并；接受长度前缀格式的解码器需要按其要求构造配置和数据，不要混用两种格式。

时间处理规则：

- PTS 单位为 `1 / 90000` 秒，不是毫秒或 Unix 时间。每次连接的首个关键帧归一化为 `2^32`，不能用于计算端到端延迟。
- 消息按解码顺序发送，B 帧的 PTS 可能回退。按到达顺序提交解码器，不要按 PTS 排序，也不要把回退当作网络乱序。
- WebCodecs 时间戳使用微秒，换算为 `round(pts × 1000000 / 90000)`。JavaScript 先用 `getBigUint64()` 读取，避免无符号 64 位值被提前转成 Number 丢失精度。
- 源时间线大幅重置或编码参数改变时，服务端会结束连接；重连后重新接收 CodecInfo、创建解码器，不能沿用旧连接的解码状态。

### 4.3 EOS：`0xff`

单字节结束消息。收到后停止播放并释放资源。断网时可能收不到 EOS，客户端也必须在 `close` / `error` 时清理。

### 4.4 客户端控制：`0x04`

客户端可以发送单字节二进制 `new Uint8Array([0x04])`。当前服务只识别该消息，仍然等待源自然产生关键帧，**不会因此向摄像机请求关键帧，也没有成功应答**。正常观看不必发送。

不要发送 JSON、字符串心跳或其他控制消息，否则连接会结束。服务端每 30 秒发送标准 WebSocket Ping；浏览器自动回复 Pong。非浏览器客户端应使用能处理 Ping / Pong 的 WebSocket 库。

### 4.5 JavaScript 解析示例

下面的解析函数可直接放进播放器项目；视频解码仍由 WebCodecs 或已有 H.264 / H.265 解码器完成。

```javascript
function parseWSVideoMessage(buffer) {
  if (!(buffer instanceof ArrayBuffer) || buffer.byteLength === 0 ||
      buffer.byteLength > 32 * 1024 * 1024) {
    throw new Error('消息大小不合法');
  }
  const view = new DataView(buffer);
  let offset = 0;
  function need(n) {
    if (n > buffer.byteLength - offset) throw new Error('消息不完整');
  }
  function number(bytes) {
    need(bytes);
    const value = bytes === 1 ? view.getUint8(offset) :
      bytes === 2 ? view.getUint16(offset, false) : view.getUint32(offset, false);
    offset += bytes;
    return value;
  }
  function nalu(bytes) {
    if (bytes === 0) throw new Error('NALU 不能为空');
    need(bytes);
    const value = new Uint8Array(buffer, offset, bytes);
    offset += bytes;
    return value;
  }

  const type = number(1);
  let message;
  if (type === 0x01) {
    const codec = number(1), profile = number(1), level = number(1);
    if (codec !== 4 && codec !== 5) throw new Error('不支持的视频编码');
    const sps = nalu(number(2)), pps = nalu(number(2));
    const vps = codec === 5 ? nalu(number(2)) : null;
    message = { type: 'codec', codec, profile, level, sps, pps, vps };
  } else if (type === 0x02) {
    need(8);
    const pts = view.getBigUint64(offset, false);
    offset += 8;
    const key = number(1), count = number(2), nalus = [];
    if (key > 1 || count === 0) throw new Error('视频帧头不合法');
    for (let i = 0; i < count; i++) nalus.push(nalu(number(4)));
    message = { type: 'frame', pts, key: key === 1, nalus };
  } else if (type === 0xff) {
    message = { type: 'eos' };
  } else {
    throw new Error('未知消息类型');
  }
  if (offset !== buffer.byteLength) throw new Error('消息包含多余字节');
  return message;
}

function toAnnexB(nalus) {
  const data = new Uint8Array(nalus.reduce((n, v) => n + 4 + v.length, 0));
  let offset = 0;
  for (const nalu of nalus) {
    data.set([0, 0, 0, 1], offset);
    data.set(nalu, offset + 4);
    offset += 4 + nalu.length;
  }
  return data;
}

function ptsToMicroseconds(pts) {
  const value = (pts * 1000000n + 45000n) / 90000n;
  if (value > BigInt(Number.MAX_SAFE_INTEGER)) {
    throw new Error('时间戳超出客户端精确表示范围，请重新连接');
  }
  return Number(value);
}

// 协议接入时可用以下断言核对基础解析和时间单位。
const sample = parseWSVideoMessage(new Uint8Array([
  2, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 1, 0, 0, 0, 1, 0x65,
]).buffer);
console.assert(sample.type === 'frame' && sample.key && sample.pts === 4294967296n);
console.assert(sample.nalus.length === 1 && toAnnexB(sample.nalus).length === 5);
console.assert(ptsToMicroseconds(90000n) === 1000000);
console.assert(parseWSVideoMessage(new Uint8Array([0xff]).buffer).type === 'eos');
```

## 5. 播放器如何解码和显示

### 5.1 直接使用已有完整示例

仓库提供 [websocket-player.html](websocket-player.html)，包含连接、CodecInfo / VideoFrame 解析、WebCodecs 解码、Canvas 绘制和停止释放逻辑。将页面放到 HTTPS 站点或 localhost HTTP 静态服务，填入第 2 节生成的 WS / WSS 地址后点击播放即可。

该页面是可运行的接入参考，没有附带 H.265 WASM 软件解码器。浏览器使用 WebCodecs 需要安全上下文（HTTPS 或 localhost），并且必须支持视频源对应的编码。WebSocket 可以连接成功而解码不受支持；两者需要分别判断。

### 5.2 嵌入自己的播放器

播放器处理顺序如下：

1. 设置 `binaryType = 'arraybuffer'`，用 `parseWSVideoMessage(event.data)` 解析消息，捕获解析异常并关闭连接。
2. 收到 `type === 'codec'` 时，保存参数集并创建与 `codec` 对应的解码器。H.264 使用 SPS / PPS，H.265 使用 VPS / SPS / PPS。检查解码器能力后再处理帧。
3. 收到 `type === 'frame'` 时，按消息到达顺序提交整个 AU，传入关键帧标志和转换后的时间戳。Annex B 模式使用 `toAnnexB(message.nalus)`。
4. 解码输出的视频帧绘制到 Canvas 或播放器渲染层。WebCodecs 输出的 `VideoFrame` 使用完成后调用 `frame.close()`。
5. 收到 EOS、连接关闭、解码失败或用户停止时，关闭 WS、释放解码器，清理队列。需要继续观看时重新连接并等待新的 CodecInfo。

WebCodecs 中，配置好的 `VideoDecoder` 可这样提交一帧：

```javascript
// message 是解析出的 frame 消息；decoder 已根据此前的 codec 消息完成配置。
decoder.decode(new EncodedVideoChunk({
  type: message.key ? 'key' : 'delta',
  timestamp: ptsToMicroseconds(message.pts),
  data: toAnnexB(message.nalus),
}));
```

上例对应未设置 `description` 的 Annex B 输入配置。完整 codec 字符串生成、参数处理及绘制代码见播放页。H.265 若不受当前浏览器支持，应接入已有软件解码器，或选择已有 WebRTC / HLS 出口；服务端不会自动转成 H.264。

建议对客户端解码队列设置上限；积压时关闭并重连，不要只丢弃任意参考帧后继续解码。自动重连采用有限退避（例如 1、2、4、8 秒，之后保持上限），用户主动停止时取消重连。token 失效时先通过业务登录流程获取新 token，再创建新连接。

## 6. 通过 Nginx 提供 WSS

在已配置 HTTPS 证书的 Nginx `server` 内加入：

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

对外地址为 `wss://video.example.com/ws/camera/001`，转发给 WS 服务的路径为 `/camera/001`。`proxy_pass` 结尾的 `/` 用于去掉 `/ws/` 前缀。当前 WS 监听器提供明文 HTTP / WS，TLS 在代理终止。

## 7. 多人共享、观看回调与连接结束

同一路径的多个 WS 连接各自作为 reader 加入，也可与 WebRTC、HLS 同时观看。读取权限、按需源和最大读取者限制复用现有逻辑。

WS 观看复用现有 `runOnRead` / `runOnUnread`，无需新增另一套回调接口。回调环境中：

| 变量 | 含义 / WS 值 |
| --- | --- |
| `MTX_PATH` | 共享流路径，例如 `camera/001` |
| `MTX_READER_TYPE` | `wsSession` |
| `MTX_READER_ID` | 本次观看连接的 UUID，重连后改变 |
| `MTX_QUERY` | 经 URL QueryEscape 编码的原始查询串；如需读取，先解码这一层，且注意其中可能含凭据 |

`runOnRead` 在连接升级成功并注册媒体读取后触发，早于首帧显示；`runOnUnread` 在会话退出时执行。每个观看连接都有各自的生命周期，不能把某个观看者退出当成整路流已无人观看。若现有回调脚本只处理 `webRTCSession`，需要将 `wsSession` 纳入处理；服务本身不会代替脚本调用摄像机 SDK。

首帧等待受全局 `readTimeout` 控制（默认 10 秒），发送受 `writeTimeout` 和 `writeQueueSize` 控制。队列溢出、写入超时、源结束、编码参数变化、鉴权到期或服务关闭等情况会结束会话。EOS 尽力发送，不保证所有异常断开都有结束包或可读的关闭原因。

## 8. 对接检查表

- RTSP 发布路径与 WS URL 路径一致，代理前缀按配置正确去除。
- 无需发送订阅消息即可按顺序收到 CodecInfo、首个关键帧和后续 VideoFrame。
- 按 90 kHz 换算 PTS，按到达顺序提交 AU，H.264 / H.265 分别验证解码能力。
- 多个观看者和现有 WebRTC / HLS 能同时观看，关闭一个 WS 不影响其他观看者。
- 鉴权、观看加入退出回调及断线重连符合业务预期；用户主动停止后不再重连。

协议实现与运行约束另见 [WebSocket 输出说明](websocket.md)。真实摄像机、目标浏览器的 H.265 能力和现场网络下的长时间播放需要在部署环境验收。

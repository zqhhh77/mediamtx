# tdt-media-stream

[![WebSocket build and tests](https://github.com/zqhhh77/mediamtx/actions/workflows/websocket.yml/badge.svg?branch=feat%2Fwebsocket-output)](https://github.com/zqhhh77/mediamtx/actions/workflows/websocket.yml)

**tdt-media-stream** is TDT's live media server and proxy for publishing, sharing and reading real-time video and audio streams. It is based on [MediaMTX](https://github.com/bluenviron/mediamtx), with an additional H.264/H.265 encoded video output over WebSocket.

Publish through RTSP and let multiple viewers read the same shared path through WebRTC, HLS, RTSP or WebSocket. See the [WebSocket guide](docs/websocket.md) and [browser verification page](docs/websocket-player.html).

中文对接文档：[RTSP 推流、WS 地址生成与客户端接入](docs/websocket-integration.md)。

GitHub Actions builds `tdt-media-stream-linux-amd64` and `tdt-media-stream-windows-amd64.exe`. The existing configuration filename `mediamtx.yml`, `MTX_` environment variables and read hooks are supported. Build and playback validation run in Actions.

Docker images: `ghcr.io/zqhhh77/tdt-media-stream:1.0.0` and `:latest` (Linux AMD64 / ARM64). Download `docker-compose.yml` and `mediamtx.yml` from the [Release](https://github.com/zqhhh77/mediamtx/releases) into the same directory, then run `docker compose up -d`. The image enables WebSocket on port 8894 by default. See the [v1.0.0 release notes](docs/releases/v1.0.0.md).

<div align="center">

| [Install](https://mediamtx.org/docs/kickoff/install) | [Documentation](https://mediamtx.org/docs/kickoff/introduction) |
| ---------------------------------------------------- | --------------------------------------------------------------- |

</div>

<h3>Features</h3>

- [Publish streams](https://mediamtx.org/docs/features/publish-and-read) to the server with Media-over-QUIC, SRT, WebRTC, RTSP, RTMP, HLS, MPEG-TS, RTP, using FFmpeg, GStreamer, OBS Studio, Python , Golang, Unity, Web browsers, Raspberry Pi Cameras and more.
- [Read streams](https://mediamtx.org/docs/features/publish-and-read) from the server with Media-over-QUIC, SRT, WebRTC, RTSP, RTMP, HLS and [WebSocket](docs/websocket.md), using FFmpeg, GStreamer, VLC, OBS Studio, Python , Golang, Unity, Web browsers and more.
- Streams are automatically converted from a protocol to another
- Serve several streams at once in separate paths
- Reload the configuration without disconnecting existing clients (hot reloading)
- [Serve always-available streams](https://mediamtx.org/docs/features/always-available) even when the publisher is offline
- [Record streams](https://mediamtx.org/docs/features/record) to disk in fMP4 or MPEG-TS format
- [Playback recorded streams](https://mediamtx.org/docs/features/playback) from disk
- [Authenticate](https://mediamtx.org/docs/features/authentication) users with internal, HTTP or JWT authentication
- [Forward streams](https://mediamtx.org/docs/features/forward) to other servers
- [Proxy requests](https://mediamtx.org/docs/features/proxy) to other servers
- [Control](https://mediamtx.org/docs/features/control-api) the server through the Control API
- [Extract metrics](https://mediamtx.org/docs/features/metrics) from the server in a Prometheus-compatible format
- [Monitor performance](https://mediamtx.org/docs/features/performance) to investigate CPU and RAM consumption
- [Run hooks](https://mediamtx.org/docs/features/hooks) (external commands) when clients connect, disconnect, read or publish streams
- Compatible with Linux, Windows and macOS, does not require any dependency or interpreter, it's a single executable
- ...and many [others](https://mediamtx.org/docs/kickoff/introduction).

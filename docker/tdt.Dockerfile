FROM --platform=linux/amd64 scratch AS binaries

ADD binaries/tdt-media-stream_*_linux_amd64.tar.gz /linux/amd64
ADD binaries/tdt-media-stream_*_linux_arm64.tar.gz /linux/arm64

FROM alpine:3.24

RUN apk add --no-cache ca-certificates

ARG TARGETPLATFORM
COPY --from=binaries /$TARGETPLATFORM /

ENV MTX_WS=true
EXPOSE 8554/tcp 8894/tcp 8888/tcp 8889/tcp 8189/udp

ENTRYPOINT ["/tdt-media-stream"]
CMD ["/mediamtx.yml"]

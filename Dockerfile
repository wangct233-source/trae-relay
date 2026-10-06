# 多阶段构建：产物为 ~15MB 的运行镜像
FROM golang:1.22-alpine AS build
WORKDIR /src
ARG VERSION=dev
# 国内构建环境可解注下行
# ENV GOPROXY=https://goproxy.cn,direct GOSUMDB=off
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/trae-relay .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -u 10001 relay
# /usr/local/bin 副本用于首次启动播种挂载卷；热更新替换的是 /app/bin 卷内文件
COPY --from=build /out/trae-relay /usr/local/bin/trae-relay
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN mkdir -p /app/bin /app/data && chown -R relay /app/bin /app/data && chmod +x /usr/local/bin/docker-entrypoint.sh
USER relay
WORKDIR /app
ENV DATA_DIR=/app/data BIN_DIR=/app/bin
EXPOSE 8080
VOLUME ["/app/data", "/app/bin"]
HEALTHCHECK --interval=30s --timeout=3s --retries=3 CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]

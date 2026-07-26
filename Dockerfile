# 镜像仓库同步平台 - 多阶段多架构 Dockerfile
# 不使用 `# syntax=` 指令以避免 buildx 从 docker.io 拉取 frontend(内网受限)。
# 默认 Dockerfile frontend 完全支持 --platform=$BUILDPLATFORM 与 TARGETARCH。

# ===== 阶段 1: 构建前端(与 CPU 架构无关,固定 amd64) =====
# --platform=$BUILDPLATFORM 让此阶段始终在 amd64 上执行,加快构建。
FROM --platform=$BUILDPLATFORM registry.cn-hangzhou.aliyuncs.com/lpx03/node:20.11.1-bookworm AS web
WORKDIR /web
# npm registry: 内网默认, CI 中可通过 --build-arg 传入公共代理。
ARG NPM_REGISTRY=http://192.168.0.12/repository/npm-group/
RUN npm config set registry ${NPM_REGISTRY}
# 先拷依赖描述,利用 docker 层缓存。
COPY web/package*.json ./
# 删除 lockfile 确保 npm install 完全走当前 registry 解析,
# 避免 lockfile 中 resolved URL 指向不可达的内网地址。
RUN rm -f package-lock.json && npm install --no-audit --no-fund
COPY web/ ./
RUN npm run build
# 产物在 /web/dist

# ===== 阶段 2: 构建后端(纯 Go,免 CGO) =====
# --platform=$BUILDPLATFORM 让 go 编译器始终在 amd64 上运行,通过 TARGETARCH 交叉编译
# 出对应架构的二进制,从而避免在 QEMU 中跑 go 编译器(慢且偶发工具链崩溃)。
# Go 1.23.12 LTS,规避 Go 1.26 在并发编译 ugorji/codec 与 modernc/sqlite 时的 SIGSEGV 工具链 bug。
FROM --platform=$BUILDPLATFORM registry.cn-hangzhou.aliyuncs.com/lpx03/golang:1.23.12-bookworm AS go
# TARGETARCH 由 buildx 自动注入(build 时为 amd64 / arm64)。
ARG TARGETARCH
WORKDIR /src
# Go proxy: 内网默认, CI 中通过 --build-arg 传入公共 CN 代理。
ARG GOPROXY=http://192.168.0.12/repository/go-group/
ARG GOSUMDB=off
ENV GOPROXY=${GOPROXY} \
    GOSUMDB=${GOSUMDB}
# 先拷依赖描述(go mod 只与模块图有关,与目标架构无关,只跑一次)。
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用阶段 1 的前端产物覆盖占位目录,供 go:embed 打进二进制。
COPY --from=web /web/dist ./web/dist
# CGO_ENABLED=0 纯静态;GOARCH=$TARGETARCH 交叉编译目标架构。
# modernc 纯 Go sqlite 驱动,无需 CGO,交叉编译零障碍。
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH} \
    go build -ldflags="-s -w" -trimpath -o /out/server .

# ===== 阶段 3: 运行镜像(分架构,buildx 会为每个平台分别构建此阶段) =====
FROM registry.cn-hangzhou.aliyuncs.com/lpx03/alpine:3.21.5-cn
# skopeo 用于镜像同步;ca-certificates 用于 HTTPS;tzdata 用于时区。
# alpine 的 skopeo 包同时提供 amd64 和 arm64。
RUN apk add --no-cache skopeo ca-certificates tzdata

COPY --from=go /out/server /app/server

# 数据目录(SQLite 落盘于此)。容器以默认用户运行,以便能写入任意 uid 拥有的挂载卷
# (SQLite 文件 + skopeo 缓存;本工具为单容器部署,不对外提权,root 运行可接受)。
RUN mkdir -p /data
VOLUME ["/data"]

ENV PORT=8080 \
    DB_PATH=/data/images-repo-sync.db \
    TZ=Asia/Shanghai
# 敏感配置(JWT_SECRET / ENCRYPT_KEY / ADMIN_USERNAME / ADMIN_PASSWORD)请在运行时通过
# `docker run -e ...` 或 docker-compose 的 environment 注入,不在镜像中固化。

EXPOSE 8080
WORKDIR /app
ENTRYPOINT ["/app/server"]

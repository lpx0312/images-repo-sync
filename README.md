# 镜像仓库同步平台 (images-repo-sync)

一个带 Web 界面的镜像仓库同步工具：配置源/目标仓库，选择镜像与目标架构，按三种模式同步到目标仓库。
后端 Go + Gin + SQLite(文件数据库)，前端 Vue3 + Element Plus，镜像搬运由 `skopeo` 完成，
最终打包为单个 Docker 镜像，SQLite 文件通过 volume 落盘。

## 功能特性

- 🔐 **登录鉴权**：JWT + bcrypt，首次启动自动创建默认 admin 账号，支持修改密码、记住登录、30 分钟无操作自动登出。
- 🗄️ **仓库管理**：配置多个源/目标仓库（Harbor / Docker Hub / ACR / 华为云 SWR / 通用 Registry），含地址、账号密码、TLS、默认 project；密码 AES-GCM 加密存储。
- 🔀 **三种同步模式**：单一项目（扁平）、保持源项目路径、仅替换仓库地址（见下文）。
- 🏗️ **目标架构**：新建任务可选仅 AMD64 / 仅 ARM64 / 所有架构（默认 AMD64，可在系统设置中修改默认值）。单架构使用 `skopeo --override-arch`；所有架构使用 `skopeo copy --all`。
- 📋 **镜像来源**：支持「粘贴列表」和「浏览目录」（列出 repo/tag 勾选）两种方式。
- 📊 **实时进度**：后台任务 + SSE 实时流，日志窗口逐行刷新。
- ⚙️ **系统设置**：可配置新建同步任务的默认架构。
- 💾 **落盘持久化**：SQLite 单文件挂载到 `/data`，容器重建数据不丢。

## 快速开始

### 方式一：Makefile（推荐）

```bash
# 构建并启动（docker run 本地镜像；本地没有镜像时会先 make build）
make up
```

访问 `http://localhost:8080`，默认账号 `admin / Admin@123`（Makefile 开发默认值；可用 `make up ADMIN_PASSWORD=你的密码` 覆盖。首次登录后请修改）。

`make up` 不会读取 `.env`。需要编排、健康检查或用 `.env` 注入密钥时，请用方式二。

常用命令：

```bash
make build        # 构建 Docker 镜像
make up           # 启动服务 (docker run 本地镜像)
make up-compose   # 用 docker compose 启动（需 .env）
make down         # 停止
make logs         # 查看日志
make status       # 容器状态
make health       # 健康检查
make shell        # 进入容器 shell
make restart      # 重启
make clean        # 清理无用容器/镜像
make clean-all    # 清理所有资源（含数据卷，危险）
```

### 方式二：Docker Compose

```bash
cp .env.example .env  # 编辑其中的密钥
docker compose up -d --build
```

### 方式三：docker run

```bash
docker build -t images-repo-sync .
docker run -d --name irs \
  -p 8080:8080 \
  -v "$PWD/data:/data" \
  -e JWT_SECRET="$(openssl rand -hex 32)" \
  -e ENCRYPT_KEY="$(openssl rand -hex 32)" \
  -e ADMIN_PASSWORD='Admin@ChangeMe' \
  images-repo-sync
```

### 本地开发

```bash
# 后端(终端1) - 本地需 Go 1.23+
make run-dev        # 或: DB_PATH=./data/test.db go run .

# 前端(终端2)
make web-dev        # 或: cd web && npm install && npm run dev
# 前端 dev server 在 :3000，自动代理 /api 到 :8080
```

## 三种同步模式

以目标仓库 `harbor.example.com`、配置的目标 project `mirror` 为例，
对源镜像 `gcr.io/k8s-staging/cluster-api/clusterctl:v1` 的转换结果：

| 模式 | 转换结果 | 规则 |
|---|---|---|
| **① 单一项目（扁平）** `flat` | `harbor.example.com/mirror/clusterctl:v1` | 进入目标 project，**只保留镜像名 + tag**，丢弃中间所有路径 |
| **② 保持源项目路径** `preserve_path` | `harbor.example.com/mirror/k8s-staging/cluster-api/clusterctl:v1` | 进入目标 project，且**源 host 后的完整路径原样保留** |
| **③ 仅替换仓库地址** `replace_host` | `harbor.example.com/k8s-staging/cluster-api/clusterctl:v1` | **不加任何 project 前缀**，只把源 host 换成目标 host |

> 模式①与模式②的差别：① 扁平命名、丢弃路径；② 保留路径。
> 模式②与模式③的差别：② 额外加 `project` 前缀，③ 完全不加。

**边界处理（自动，无需配置）：**
- 隐式 `docker.io` 自动补全：`nginx:1.25` 视作 `docker.io/library/nginx:1.25`
- 模式① 对 `docker.io/library/nginx:1.25` → 取末段 `nginx:1.25` → `harbor/mirror/nginx:1.25`
- 模式③ 对 `bitnami/redis:7.0`（隐式 docker.io）→ `harbor/bitnami/redis:7.0`

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `PORT` | `8080` | 监听端口 |
| `DB_PATH` | `/data/images-repo-sync.db` | SQLite 文件路径 |
| `JWT_SECRET` | （随机生成） | JWT 签名密钥。**生产必须显式设置**，否则重启后所有登录失效 |
| `ADMIN_USERNAME` | `admin` | 首次启动创建的默认管理员用户名 |
| `ADMIN_PASSWORD` | `admin123` | 首次启动创建的默认管理员密码。`make up` 开发默认值为 `Admin@123`；docker compose 使用 `.env`（`.env.example` 为 `Admin@ChangeMe`） |
| `ENCRYPT_KEY` | （空=明文） | 仓库密码加密主密钥。**生产建议设置**，否则仓库密码明文存储 |
| `SKOPEO_BIN` | `skopeo` | skopeo 可执行文件路径 |
| `TZ` | `Asia/Shanghai` | 时区 |

## 数据持久化

- 数据库文件：`/data/images-repo-sync.db`
- 通过 volume 挂载（compose 中为 `./data:/data`）
- 容器删除重建后，用户、仓库配置、任务历史全部保留

## 技术栈

- **后端**：Go 1.23 + Gin + GORM + glebarez/sqlite（modernc 纯 Go 驱动，免 CGO）+ golang-jwt + bcrypt
- **前端**：Vue 3 + Vite + Element Plus + Pinia + Vue Router + axios
- **镜像搬运**：skopeo（按任务架构选择 `--override-arch` 或 `copy --all`；默认加 `--preserve-digests`，目标为华为云 SWR 时不加）
- **打包**：多阶段 Dockerfile（node 构建前端 → go 编译 → alpine 运行），单二进制（前端 `go:embed`）

> 构建环境默认使用内网加速：Go 代理 `http://192.168.0.12/repository/go-group/`、npm 镜像 `http://192.168.0.12/repository/npm-group/`，基础镜像来自阿里云镜像仓库。公网或 CI 可通过 `--build-arg GOPROXY=...`、`--build-arg NPM_REGISTRY=...` 覆盖，无需改 Dockerfile；更换基础镜像仍需修改 `FROM`。

## 多架构构建

本镜像支持 `linux/amd64` + `linux/arm64` 双架构，推送到镜像仓库后会生成一个 manifest list，拉取时自动匹配当前机器架构。

**关键技术点**：Go 后端通过 `TARGETARCH` 在 amd64 构建机上交叉编译出对应架构的二进制（`CGO_ENABLED=0 GOARCH=$TARGETARCH`），无需在 QEMU 中跑 Go 编译器，速度快、稳定性高。只有最终 alpine 运行阶段按平台分别拉取。

### 构建并推送到阿里云

```bash
# 一键多架构构建 + 推送
make build-push ALIYUN_USERNAME=你的账号 ALIYUN_PASSWORD=你的密码
```

镜像地址：`registry.cn-hangzhou.aliyuncs.com/lpx03/images-repo-sync:latest`
同时会打当前 commit 短 sha 标签（如 `:4f5fb2a`）和 `git describe` 版本标签。

推送到其他命名空间：`make build-push ALIYUN_NAMESPACE=你的命名空间 ALIYUN_USERNAME=... ALIYUN_PASSWORD=...`

向 GitHub 推送任意 commit 会触发 GitHub Actions，构建 `linux/amd64` + `linux/arm64` 并推送到阿里云 ACR。默认分支额外打 `:latest`，每次提交打 commit 短 sha 标签（仓库地址由 Actions 变量配置）。

### 仅构建不推送（写入 buildx 缓存）

```bash
make build-multiarch
```

### 在不同架构机器上拉取

```bash
# amd64 机器：自动拉 amd64 镜像
docker pull registry.cn-hangzhou.aliyuncs.com/lpx03/images-repo-sync:latest

# arm64 机器（如树莓派、鲲鹏、AWS Graviton）：自动拉 arm64 镜像
docker pull registry.cn-hangzhou.aliyuncs.com/lpx03/images-repo-sync:latest

# 手动指定架构
docker pull --platform linux/arm64 registry.cn-hangzhou.aliyuncs.com/lpx03/images-repo-sync:latest
```

### 前置条件

- Docker 19.03+（带 buildx）
- buildx builder 已创建（`make` 会自动创建名为 `mybuilder` 的 docker-container 驱动 builder）
- QEMU 已注册（用于 arm64 的 alpine 阶段）：`docker run --privileged --rm tonistiigi/binfmt --install all`

> 注意：单架构本地构建（`make build`）用普通 `docker build`，产出可直接 `docker run` 的镜像；多架构构建（`make build-multiarch` / `make build-push`）用 buildx，产出的 manifest list 无法直接 `docker run`，需推送后在目标机器拉取。

## 项目结构

```
images-repo-sync/
├── main.go                       # 入口,embed 前端
├── internal/
│   ├── config/   config.go       # 环境变量配置
│   ├── model/    model.go        # GORM 模型
│   ├── store/    store.go        # SQLite 初始化 + seed admin
│   ├── auth/     jwt.go, password.go
│   ├── crypto/   crypto.go       # AES-GCM 加解密
│   ├── middleware/auth.go
│   ├── registry/ probe.go        # 仓库连通性探测
│   ├── api/      auth.go, registry.go, catalog.go, task.go, settings.go, router.go
│   ├── skopeo/   ref.go, copy.go, exec.go, authfile.go, catalog.go
│   └── task/     manager.go, runner.go, types.go
├── web/                          # Vue3 前端
│   └── src/
│       ├── styles/design-tokens.css
│       ├── stores/auth.js
│       ├── utils/  ref.js, constants.js
│       ├── api/  router/
│       ├── App.vue, views/, components/
├── .github/workflows/docker-publish.yml
├── Dockerfile, docker-compose.yml
└── README.md
```

## API 概览

| 方法 | 路径 | 鉴权 | 说明 |
|---|---|---|---|
| GET | `/api/healthz` | 公开 | 健康检查 |
| POST | `/api/auth/login` | 公开 | 登录，返回 JWT |
| POST | `/api/auth/logout` | Bearer | 登出 |
| GET | `/api/auth/me` | Bearer | 当前用户 |
| PUT | `/api/auth/password` | Bearer | 修改密码 |
| GET/POST/PUT/DELETE | `/api/registries` | Bearer | 仓库 CRUD |
| POST | `/api/registries/:id/test` | Bearer | 测试连接 |
| GET | `/api/catalog/:id/repos` | Bearer | 列出 repo |
| GET | `/api/catalog/:id/tags?repo=` | Bearer | 列出 tag |
| GET | `/api/catalog/:id/projects` | Bearer | 列出 Harbor project |
| GET/PUT | `/api/settings` | Bearer | 系统设置（如默认同步架构） |
| POST | `/api/tasks` | Bearer | 创建同步任务 |
| GET | `/api/tasks` `/api/tasks/:id` | Bearer | 任务列表/详情 |
| GET | `/api/tasks/:id/stream` | Bearer | SSE 实时事件流 |
| POST | `/api/tasks/:id/cancel` | Bearer | 取消任务 |

## 注意事项

- **默认密码**：未设置 `ADMIN_PASSWORD` 时应用默认 `admin / admin123`；`make up` 使用 Makefile 开发默认值 `Admin@123`；docker compose 使用 `.env`（`.env.example` 为 `Admin@ChangeMe`）。均为首次启动便利，生产环境务必设置强密码并登录后修改。
- **Harbor `_catalog`**：很多 Harbor 部署禁用了 registry 原生 `_catalog`；本工具对 Harbor 类型走 v2 API（`/api/v2.0/projects/.../repositories`），其他类型走 `_catalog`。若浏览目录失败，请改用「粘贴列表」方式。
- **华为云 SWR**：基础版拒收顶层 OCI image index。目标类型选「华为云 SWR」时同步不加 `--preserve-digests`，skopeo 会把 OCI index 转成 Docker manifest list（仅顶层 digest 变化，镜像内容不变）。其他类型若遇到同样拒收，会自动去掉该参数重试一次。
- **insecure 仓库**：自签证书的仓库请在仓库配置中开启「跳过 TLS」（只跳过证书校验，仍走 HTTPS）。
- **并发**：当前任务串行执行（SQLite 写串行 + 同步 IO 密集），避免对目标仓库造成过大压力。

## License

MIT

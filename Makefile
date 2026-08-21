# ============================================================================
# 镜像仓库同步平台 (images-repo-sync) - Makefile
# ============================================================================
#
# 支持单架构(本地)和多架构(buildx amd64+arm64 → 阿里云)两种构建模式。
#
# 常用命令：
#   make help              - 查看全部命令
#   make build             - 本地构建单架构镜像(amd64, 可立即 docker run)
#   make up / down         - 启动/停止服务(docker run 本地镜像)
#   make build-multiarch   - 多架构构建(amd64+arm64, 仅本地 manifest)
#   make build-push        - 多架构构建并推送到阿里云(一键发布)
#
# 推送目标: registry.cn-hangzhou.aliyuncs.com/lpx03/images-repo-sync
# 阿里云账号请在环境变量传入(勿写入仓库):
#   ALIYUN_USERNAME / ALIYUN_PASSWORD
# ============================================================================

.DEFAULT_GOAL := help

# ---------- 项目配置 ----------
PROJECT_NAME := images-repo-sync
# GIT_SHA: commit 短 sha(如 4f5fb2a),用于在镜像仓库里和 commit 一一对应。
# VERSION: git describe 语义版本(如 v1.0.0-1-g4f5fb2a),带 tag 上下文。
GIT_SHA      := $(shell git rev-parse --short HEAD 2>/dev/null || echo dev)
VERSION      := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
BUILD_TIME   := $(shell date -u '+%Y-%m-%d_%H:%M:%S')

# 本地镜像(单架构, make build / make up 用)
IMAGE         := $(PROJECT_NAME):latest
IMAGE_VER     := $(PROJECT_NAME):$(VERSION)
CONTAINER     := $(PROJECT_NAME)
PORT          ?= 8080
DATA_DIR      ?= $(CURDIR)/data

# 多架构镜像仓库(阿里云)。
# 凭据请通过环境变量传入: make build-push ALIYUN_USERNAME=xxx ALIYUN_PASSWORD=xxx
ALIYUN_REGISTRY  := registry.cn-hangzhou.aliyuncs.com
ALIYUN_NAMESPACE ?= lpx03
REMOTE_IMAGE     := $(ALIYUN_REGISTRY)/$(ALIYUN_NAMESPACE)/$(PROJECT_NAME)
REMOTE_LATEST    := $(REMOTE_IMAGE):latest
REMOTE_SHA       := $(REMOTE_IMAGE):$(GIT_SHA)
REMOTE_VERSION   := $(REMOTE_IMAGE):$(VERSION)

# 多架构平台(buildx)。
PLATFORMS    := linux/amd64,linux/arm64
BUILDER      := mybuilder

# ---------- 颜色 ----------
RED    := \033[31m
GREEN  := \033[32m
YELLOW := \033[33m
BLUE   := \033[34m
CYAN   := \033[36m
RESET  := \033[0m

# ============================================================================
# 帮助
# ============================================================================
.PHONY: help
help: ## 显示帮助信息
	@echo "$(CYAN)============================================================================$(RESET)"
	@echo "$(CYAN)镜像仓库同步平台 (images-repo-sync)$(RESET)"
	@echo "$(CYAN)============================================================================$(RESET)"
	@echo ""
	@echo "$(YELLOW)本地构建:$(RESET)"
	@echo "  $(GREEN)build$(RESET)             构建单架构镜像(amd64, 本地 docker run 用)"
	@echo "  $(GREEN)build-no-cache$(RESET)    不使用缓存构建单架构"
	@echo ""
	@echo "$(YELLOW)多架构构建:$(RESET)"
	@echo "  $(GREEN)build-multiarch$(RESET)   多架构构建(amd64+arm64),仅写入本地 buildx 缓存"
	@echo "  $(GREEN)build-push$(RESET)        多架构构建并推送到阿里云(一键发布, 需 ALIYUN_USERNAME/PASSWORD)"
	@echo "  $(GREEN)login$(RESET)             登录阿里云镜像仓库"
	@echo ""
	@echo "$(YELLOW)服务管理:$(RESET)"
	@echo "  $(GREEN)up$(RESET)                启动容器(docker run 本地镜像, 自动 build)"
	@echo "  $(GREEN)down$(RESET)              停止并删除容器"
	@echo "  $(GREEN)restart$(RESET)           重启服务"
	@echo "  $(GREEN)up-compose$(RESET)        [可选] 用 docker compose 启动(需 .env)"
	@echo ""
	@echo "$(YELLOW)监控调试:$(RESET)"
	@echo "  $(GREEN)status$(RESET)            查看容器状态"
	@echo "  $(GREEN)logs$(RESET)              查看日志(实时跟踪)"
	@echo "  $(GREEN)shell$(RESET)             进入容器 shell"
	@echo "  $(GREEN)health$(RESET)            健康检查"
	@echo ""
	@echo "$(YELLOW)本地开发:$(RESET)"
	@echo "  $(GREEN)run-dev$(RESET)           本地直接运行后端"
	@echo "  $(GREEN)web-dev$(RESET)           启动前端 dev server(:3000)"
	@echo "  $(GREEN)web-build$(RESET)         构建前端到 web/dist"
	@echo "  $(GREEN)test$(RESET)              运行单元测试"
	@echo ""
	@echo "$(YELLOW)环境与清理:$(RESET)"
	@echo "  $(GREEN)env-init$(RESET)          生成 .env 配置模板"
	@echo "  $(GREEN)clean$(RESET)             清理停止的容器和无用镜像"
	@echo "  $(GREEN)clean-all$(RESET)         清理所有相关资源(含数据卷, 危险)"
	@echo "$(CYAN)============================================================================$(RESET)"
	@echo "$(YELLOW)示例:$(RESET)"
	@echo "  make build-push ALIYUN_USERNAME=yourname ALIYUN_PASSWORD=yourpass"
	@echo "  make up PORT=9090"
	@echo "$(CYAN)============================================================================$(RESET)"

# ============================================================================
# 本地单架构构建
# ============================================================================
.PHONY: build
build: ## 构建单架构镜像(amd64)
	@echo "$(BLUE)🐳 构建单架构镜像(amd64)...$(RESET)"
	@docker build -t $(IMAGE) -t $(IMAGE_VER) .
	@echo "$(GREEN)✅ 镜像构建完成: $(IMAGE) / $(IMAGE_VER)$(RESET)"

.PHONY: build-no-cache
build-no-cache: ## 不使用缓存构建单架构
	@echo "$(BLUE)🐳 构建单架构镜像(no-cache)...$(RESET)"
	@docker build --no-cache -t $(IMAGE) -t $(IMAGE_VER) .
	@echo "$(GREEN)✅ 镜像构建完成$(RESET)"

# ============================================================================
# 多架构构建(buildx)
# ============================================================================

.PHONY: ensure-builder
ensure-builder: ## 确保 buildx builder 存在
	@docker buildx inspect $(BUILDER) >/dev/null 2>&1 \
	 || (echo "$(BLUE)🔧 创建 buildx builder $(BUILDER)...$(RESET)" \
	     && docker buildx create --name $(BUILDER) --driver docker-container --bootstrap --use)
	@docker buildx inspect --bootstrap $(BUILDER) >/dev/null

.PHONY: login
login: ## 登录阿里云镜像仓库
	@if [ -z "$(ALIYUN_USERNAME)" ] || [ -z "$(ALIYUN_PASSWORD)" ]; then \
		echo "$(RED)❌ 请传入 ALIYUN_USERNAME 和 ALIYUN_PASSWORD$(RESET)"; \
		echo "$(YELLOW)   例: make login ALIYUN_USERNAME=xxx ALIYUN_PASSWORD=xxx$(RESET)"; \
		exit 1; \
	fi
	@echo "$(BLUE)🔐 登录 $(ALIYUN_REGISTRY)...$(RESET)"
	@echo "$(ALIYUN_PASSWORD)" | docker login $(ALIYUN_REGISTRY) -u "$(ALIYUN_USERNAME)" --password-stdin
	@echo "$(GREEN)✅ 登录成功$(RESET)"

.PHONY: build-multiarch
build-multiarch: ensure-builder ## 多架构构建(amd64+arm64, 仅本地 buildx 缓存)
	@echo "$(BLUE)🐳 多架构构建 [$(PLATFORMS)]...$(RESET)"
	@echo "$(YELLOW)    注: 仅写入 buildx 缓存,不产生本地 docker images。如需推送请用 make build-push$(RESET)"
	@docker buildx build \
		--builder $(BUILDER) \
		--platform $(PLATFORMS) \
		-t $(REMOTE_LATEST) -t $(REMOTE_SHA) -t $(REMOTE_VERSION) \
		--load=false \
		.
	@echo "$(GREEN)✅ 多架构构建完成(缓存已生成)$(RESET)"

.PHONY: build-push
build-push: ensure-builder login ## 多架构构建并推送到阿里云(一键发布)
	@echo "$(BLUE)🚀 多架构构建并推送 [$(PLATFORMS)] -> $(REMOTE_IMAGE)...$(RESET)"
	@docker buildx build \
		--builder $(BUILDER) \
		--platform $(PLATFORMS) \
		-t $(REMOTE_LATEST) -t $(REMOTE_SHA) -t $(REMOTE_VERSION) \
		--push \
		.
	@echo "$(GREEN)✅ 多架构镜像已推送:$(RESET)"
	@echo "  $(GREEN)$(REMOTE_LATEST)$(RESET)   <- 最新版"
	@echo "  $(GREEN)$(REMOTE_SHA)$(RESET)       <- 当前 commit 短 sha"
	@echo "  $(GREEN)$(REMOTE_VERSION)$(RESET)   <- 语义版本"
	@echo ""
	@echo "$(CYAN)拉取命令: docker pull $(REMOTE_LATEST)$(RESET)"
	@echo "$(CYAN)运行命令: docker run -d -p 8080:8080 -v \$$PWD/data:/data $(REMOTE_LATEST)$(RESET)"

# ============================================================================
# 服务管理(本地 docker run,不走 docker compose 避免内网 pull 卡死)
# ============================================================================

# 运行时配置可通过环境变量或 make 参数覆盖;未设置时用下面的开发默认值,保证开箱即用。
# 注意: make up 不会自动读取 .env(那是 docker compose / make up-compose 的行为)。
# 这些值仅用于本地测试,生产请显式覆盖: make up JWT_SECRET=xxx ENCRYPT_KEY=xxx
JWT_SECRET   ?= dev-jwt-secret-change-me
ENCRYPT_KEY  ?= dev-encrypt-key-change-me
ADMIN_USERNAME ?= admin
ADMIN_PASSWORD ?= Admin@123

.PHONY: up
up: ## 启动服务(docker run 本地镜像)
	@echo "$(BLUE)🚀 启动容器(本地镜像 $(IMAGE))...$(RESET)"
	@mkdir -p $(DATA_DIR)
	@if ! docker image inspect $(IMAGE) >/dev/null 2>&1; then \
		echo "$(YELLOW)⚠️  本地未找到镜像 $(IMAGE),先执行 make build...$(RESET)"; \
		$(MAKE) build; \
	fi
	@docker rm -f $(CONTAINER) 2>/dev/null || true
	@docker run -d --name $(CONTAINER) \
		-p $(PORT):8080 \
		-v $(DATA_DIR):/data \
		-e TZ=Asia/Shanghai \
		-e JWT_SECRET='$(JWT_SECRET)' \
		-e ENCRYPT_KEY='$(ENCRYPT_KEY)' \
		-e ADMIN_USERNAME='$(ADMIN_USERNAME)' \
		-e ADMIN_PASSWORD='$(ADMIN_PASSWORD)' \
		$(IMAGE)
	@echo "$(GREEN)✅ 容器已启动$(RESET)"
	@$(MAKE) _show_url

.PHONY: down
down: ## 停止并删除容器
	@echo "$(BLUE)🛑 停止容器...$(RESET)"
	@docker rm -f $(CONTAINER) 2>/dev/null || true
	@echo "$(GREEN)✅ 容器已停止$(RESET)"

.PHONY: restart
restart: ## 重启服务
	@$(MAKE) down
	@$(MAKE) up

.PHONY: run
run: up ## 别名,等价于 make up

.PHONY: stop
stop: down ## 别名,等价于 make down

# 可选:用 docker compose 启动(需先 cp .env.example .env 并填好密钥)。
# 一般本地测试用 make up 即可,仅在需要编排/健康检查/自动重启时用 compose。
.PHONY: up-compose
up-compose: ## 用 docker compose 启动(可选,需配置 .env)
	@echo "$(BLUE)🚀 启动服务(docker compose)...$(RESET)"
	@mkdir -p $(DATA_DIR)
	@if [ ! -f .env ]; then echo "$(RED)❌ 缺少 .env,请先 cp .env.example .env 并填写密钥$(RESET)"; exit 1; fi
	@PORT=$(PORT) docker compose --skip-pull up -d --build
	@echo "$(GREEN)✅ 服务已启动$(RESET)"
	@$(MAKE) _show_url

.PHONY: down-compose
down-compose: ## 停止 docker compose 服务(可选)
	@docker compose down 2>/dev/null || true
	@echo "$(GREEN)✅ 服务已停止$(RESET)"

# ============================================================================
# 监控调试
# ============================================================================
.PHONY: status
status: ## 查看容器状态
	@echo "$(BLUE)📊 容器状态:$(RESET)"
	@docker ps -a --filter "name=$(CONTAINER)" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"

.PHONY: logs
logs: ## 查看日志(实时跟踪)
	@docker logs -f $(CONTAINER) 2>/dev/null \
	 || docker compose logs -f 2>/dev/null \
	 || echo "$(RED)❌ 服务未运行$(RESET)"

.PHONY: shell
shell: ## 进入容器 shell
	@docker exec -it $(CONTAINER) /bin/sh || echo "$(RED)❌ 容器未运行$(RESET)"

.PHONY: health
health: ## 健康检查
	@echo "$(BLUE)🏥 健康检查...$(RESET)"
	@curl -s -o /dev/null -w "healthz: HTTP %{http_code}\n" http://localhost:$(PORT)/api/healthz \
	 && echo "$(GREEN)✅ 服务正常$(RESET)" \
	 || echo "$(RED)❌ 服务不可达$(RESET)"

# ============================================================================
# 本地开发
# ============================================================================
.PHONY: run-dev
run-dev: ## 本地直接运行后端
	@echo "$(BLUE)▶ 本地运行后端(:8080)...$(RESET)"
	@mkdir -p $(DATA_DIR)
	@DB_PATH=$(DATA_DIR)/images-repo-sync.db PORT=8080 JWT_SECRET=dev-secret go run .

.PHONY: web-dev
web-dev: ## 启动前端 dev server
	@echo "$(BLUE)▶ 前端 dev server(:3000)...$(RESET)"
	@cd web && npm run dev

.PHONY: web-build
web-build: ## 构建前端到 web/dist
	@echo "$(BLUE)▶ 构建前端...$(RESET)"
	@cd web && npm ci && npm run build

# CLI 客户端(irs):输出到 bin/,按当前平台命名(irs / irs.exe)。
IRS_BIN := bin/irs$(shell go env GOEXE)

.PHONY: cli
cli: ## 编译 irs 命令行客户端到 bin/
	@echo "$(BLUE)▶ 编译 CLI → $(IRS_BIN)...$(RESET)"
	@mkdir -p bin
	@go build -o $(IRS_BIN) ./cmd/irs
	@echo "$(GREEN)✅ 完成: $(IRS_BIN)(放入 PATH 即可全局使用,文档见 docs/cli.md)$(RESET)"

.PHONY: tidy
tidy: ## go mod tidy
	@go mod tidy

.PHONY: test
test: ## 运行单元测试
	@go test ./...

# ============================================================================
# 环境配置
# ============================================================================
.PHONY: env-init
env-init: ## 生成 .env 配置模板
	@if [ ! -f .env ]; then \
		cp .env.example .env; \
		echo "$(GREEN)✅ 已生成 .env,请修改其中的密钥$(RESET)"; \
	else \
		echo "$(YELLOW)⚠️  .env 已存在$(RESET)"; \
	fi

# ============================================================================
# 清理
# ============================================================================
.PHONY: clean
clean: ## 清理停止的容器和无用镜像
	@echo "$(BLUE)🧹 清理...$(RESET)"
	@docker container prune -f
	@docker image prune -f
	@echo "$(GREEN)✅ 清理完成$(RESET)"

.PHONY: clean-all
clean-all: ## 清理所有相关资源(含数据卷, 危险!)
	@echo "$(RED)⚠️  警告: 将删除容器、镜像和数据卷!$(RESET)"
	@read -p "确定继续? (y/N): " confirm && [ "$$confirm" = "y" ] || exit 1
	@$(MAKE) down
	@docker rm -f $(CONTAINER) 2>/dev/null || true
	@docker rmi $(IMAGE) $(IMAGE_VER) 2>/dev/null || true
	@rm -rf $(DATA_DIR) 2>/dev/null || true
	@echo "$(GREEN)✅ 所有资源已清理$(RESET)"

# ============================================================================
# 内部辅助
# ============================================================================
.PHONY: _show_url
_show_url:
	@echo ""
	@echo "$(CYAN)🌐 访问地址:$(RESET)"
	@echo "  $(GREEN)前端/API:$(RESET) http://localhost:$(PORT)"
	@echo "  $(GREEN)健康检查:$(RESET) http://localhost:$(PORT)/api/healthz"
	@echo "  $(GREEN)默认账号:$(RESET) admin / $(ADMIN_PASSWORD)  (make up 开发默认值; compose 请看 .env 的 ADMIN_PASSWORD)"
	@echo ""

# 构建与交付 + 测试基础设施

> 范围:`Dockerfile`、`Makefile`、`docker-compose.yml`、`.github/workflows/`(ci.yml、docker-publish.yml)、`.dockerignore`/`.gitignore`/`.env.example`、`internal/testutil/`(含 fakeskopeo)。基于 main 分支 2026-10-27 代码(阶段 4 修复已合入)。

## 1. 三阶段 Dockerfile 与 ZONE 机制

三阶段多架构构建,构建阶段均 `--platform=$BUILDPLATFORM`(固定在 amd64 跑工具链,QEMU 只跑产物):

- **阶段 1 web**:`node:20.11.1-bookworm`(ACR 镜像 `registry.cn-hangzhou.aliyuncs.com/lpx03/`),`rm package-lock.json` 后 `npm install`(规避 lockfile 内网 resolved URL),产出到 `/web/dist`。
- **阶段 2 go**:`golang:1.23.12-bookworm`(注释明确规避 Go 1.26 并发编译 SIGSEGV 工具链 bug),`go mod download` 后把阶段 1 产物覆盖 `web/dist` 占位目录,`CGO_ENABLED=0 GOARCH=$TARGETARCH` 交叉编译纯静态二进制(modernc 纯 Go sqlite,免 CGO)。
- **阶段 3 runtime**:`alpine:3.21.5-cn` + `skopeo/ca-certificates/tzdata`,`VOLUME /data`,`ENV PORT/DB_PATH/TZ`,敏感配置(JWT_SECRET 等)留给运行时注入,镜像内不固化。

**ZONE 单参数切换源**(三处 `case` 分支):默认 `ZONE=global` 用公共源(npmjs / proxy.golang.org / sum.golang.org / dl-cdn.alpinelinux.org),任何环境(含 GitHub runner)裸 `docker build .` 即成功;`ZONE=cn` 走内网 Nexus(192.168.0.12,npm/go 代理,GOSUMDB=off;apk 沿用基础镜像内置内网源)。`NPM_REGISTRY/GOPROXY/GOSUMDB` 可作为 global 模式默认值单独覆盖。

## 2. Makefile 目标

- 构建:`build` / `build-no-cache`(docker build,`ZONE ?= cn` 本地默认);`build-multiarch` / `build-push`(buildx docker-container builder,amd64+arm64,latest/GIT_SHA/VERSION 三 tag 推 ACR,凭据经 ALIYUN_USERNAME/PASSWORD 环境变量,`login` 目标 --password-stdin)。
- 服务:`up`/`down`/`restart`/`run`/`stop`(docker run 本地镜像,密钥 `-e` 注入,不读 .env);`up-compose`/`down-compose`(需 .env);`status`/`logs`/`shell`/`health`(curl healthz)。
- 开发:`run-dev`(go run)、`web-dev`(:3000,vite 代理 /api→8080)、`web-build`(`npm ci` + build)、`cli`(编译 `bin/irs`)、`tidy`、`test`(`go test -count=1`)、`test-cov`(排除 testutil 跨包 coverpkg 合并,≥90% 验收线)、`env-init`、`clean`/`clean-all`(交互确认后连数据卷一起删)。

## 3. CI 门禁(ci.yml)与手动发布(docker-publish.yml)

- **ci.yml**(push main + PR,三 job 并行,零内网依赖):`backend` go1.23 vet + test;`frontend` node20 删 lockfile + npm install + vitest(29 用例)+ build;`docker` buildx 构建单架构镜像(ZONE 默认 global)→ `docker run` + 30×2s 循环 curl `/api/healthz` 运行冒烟,失败打容器日志,不推送。
- **docker-publish.yml**(仅 workflow_dispatch 手动):QEMU + buildx,多架构(amd64+arm64)构建推送 ACR(registry/namespace 取 vars,凭据取 secrets);metadata-action 生成 latest/sha/branch/tag/semver 标签;provenance/sbom 关闭(ACR 不接受 attestation);显式传公共源 build-args(与默认一致,双保险)。

## 4. docker-compose

单服务编排:`build .`、`${PORT:-8080}:8080`、`./data:/data`;环境变量自 .env——`JWT_SECRET`/`ENCRYPT_KEY` 缺失时 `:?` 报错拦启动,`ADMIN_*` 有默认;`TASK_CONCURRENCY`/两个保留天数/TZ 可调;healthcheck 用 `wget --spider` 探 healthz(30s/5s/3 次,start_period 10s)。compose 仅是可选项,本地默认走 `make up`(docker run,避免内网 pull 卡死)。

## 5. 测试基础设施(internal/testutil)

- **NewTestDB**:临时 SQLite 替换 `config.AppConfig` 各项 + `store.Init()`,seed admin/Admin@123 与 default_arch=amd64;cleanup **先还原 `store.DB=old` 再关闭新连接**,杜绝悬挂句柄导致的 "database is closed";`resetMu` 串行化全局替换。
- **FakeSkopeo/UseFakeSkopeo**:`sync.Once` 首次把 fakeskopeo 编译到 `$TMPDIR/irs-fake-skopeo-<pid>.exe` 并缓存;`UseFakeSkopeo` 替换/恢复 `AppConfig.SkopeoBin`。替身按引用关键字控制行为——copy:`failcopy`(unauthorized)/`noarch`/`preserveconflict`/`conflictonce`(仅带 --preserve-digests 失败,模拟 SWR)/`manifestunknown`/`slowcopy`(30s,测取消)/`dualstream`(双流各 200 行并发,配 -race)/`longline`(2MB 超长行,测 Scanner 缓冲);list-tags:`failtags`/`badjson`;inspect:`failinspect`/`varydigest`(按 ref 哈希可变 digest)。
- **BuildChartTgz/WriteChartTgz**:内存构造最小可解析 chart tgz(可带/不带顶层目录);**WaitFor**:20ms 轮询至超时。
- `testutil_test.go`(契约测试):锁定 8 表迁移、seed 密码正反向校验、WaitFor 三路径、各关键字行为(含双流完整性与 2MB 断言),是其余各包测试的地基。

## 6. 已知限制

- CI 无镜像漏洞扫描;`.env.example` 与 `config.go` 键集合靠人肉同步,无一致性校验。
- ci.yml docker job 与 docker-publish 均未配置 buildx 缓存(cache-from/to),每次全量构建。
- `make up` 密钥经 `-e` 注入,`docker inspect` 可见。
- 默认管理员口令三处不一致(config.go `admin123`、compose `.env.example` `Admin@ChangeMe`、Makefile `Admin@123`),且 `admin123` 不满足自身强度要求。
- Dockerfile/CI 删 lockfile 后 `npm install`,构建不可复现(与 Makefile `npm ci` 策略相反)。
- fakeskopeo 编译产物在 %TEMP% 永不清理;`test-cov` 分母仍含 main 包;运行时基础镜像是自制 ACR 镜像,tag 固定但内容不可由仓库审计。

## 7. 重构方案

- **P1 make up 改 --env-file 注入密钥**:问题——`-e JWT_SECRET=...` 使密钥出现在 `docker inspect` 与进程列表回显。方案——up 前确保 .env 存在(复用 env-init),`docker run --env-file .env`;开发默认值写进 .env 模板。影响面:Makefile up/_show_url、.env.example。工作量 1h。风险:低(需同步提示已有用户迁移)。
- **P2 CI/发布接 buildx 缓存**:问题——无缓存,ci docker job 与多架构发布每次全量拉依赖重编译,时长与配额浪费。方案——两个 workflow 的 build-push-action 加 `cache-from/to: type=gha`;Makefile build-push 可选加 `--cache-to type=local`。影响面:.github/workflows 两个文件。工作量 1.5h。风险:低(注意多架构 manifest 缓存 scope)。
- **P2 默认管理员口令随机化决策落地**:问题——弱口令三处不一致且 users 表清空重启会静默重置。方案——定稿单一策略:首启无 ADMIN_PASSWORD 时随机生成并打印一次性日志(或强制首次改密),三处配置统一删除明文默认。影响面:internal/config、docker-compose.yml、Makefile、.env.example、README。工作量 3h。风险:中(改变开箱体验,需更新文档与 e2e)。
- **P3 CI 增加镜像扫描与 .env 键一致性校验**:问题——无漏洞门禁;env 键漂移只能靠人。方案——docker job 追加 trivy action(失败级别 HIGH 以上阻断);新增小 job 用脚本比对 .env.example 键集合 ⊇ config.go 消费的 env 键。影响面:ci.yml、新增脚本。工作量 2.5h。风险:低(扫描误报需白名单机制)。
- **P3 fakeskopeo 产物清理 + test-cov 分母修正**:问题——%TEMP% 孤儿 exe 按 pid 累积;test-cov 覆盖率分母含 main 包拉低读数。方案——FakeSkopeo 注册 `tb.Cleanup`(进程内最后一个用例后删除,或改 `TestMain` 统一清);COVER_PKGS 再排除根 main 包。影响面:internal/testutil、Makefile。工作量 1h。风险:低。
- **P3 可复现构建:恢复 lockfile**:问题——`rm package-lock.json + npm install` 使镜像内前端依赖不可复现,与 Makefile `npm ci` 不一致。方案——lockfile 以公共 registry resolved URL 入库,Dockerfile/CI 改 `npm ci`;cn 模式用 `npm ci --registry=<内网>` 覆盖(需验证 Nexus 对 resolved URL 的兼容性,不兼容则回退现状)。影响面:web/package-lock.json、Dockerfile 阶段 1、ci.yml frontend job。工作量 2h。风险:中(内网 registry 行为需实测)。

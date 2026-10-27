# images-repo-sync 架构说明

> 生成时间:2026-10-27,基于 main 分支(92a234d)全仓通读。

## 1. 技术栈与依赖

- **后端**(go.mod,Go 1.23):gin v1.10 + gin-contrib/cors(HTTP);gorm v1.25 + glebarez/sqlite(纯 Go SQLite 驱动,免 CGO);golang-jwt/v5(HS256);golang.org/x/crypto(bcrypt);yaml.v3(解析 Chart.yaml)。镜像搬运依赖外部 **skopeo 二进制**(alpine apk 安装)。
- **前端**(web/package.json):Vue 3.5 + vue-router 4 + pinia 2 + element-plus 2.8 + axios + dayjs;构建 Vite 5(`@vitejs/plugin-vue`),dev server :3000 代理 `/api`→:8080(web/vite.config.js)。
- **工具链**:Makefile + Docker 多阶段 + GitHub Actions(buildx 多架构)。

## 2. 整体架构(分层)

```
main.go(server) ── config.Load → store.Init(SQLite) → task.Instance(worker池)
  │                → RecoverStuckTasks/RecoverChartUploads → api.SweepTempUploads → StartRetention → skopeo.BinVersion 自检
  ├─ go:embed web/dist(main.go)→ staticFS → api.SetupRouter(store.DB, staticFS)
  └─ http.Server 优雅退出(main.go)

cmd/irs/main.go(CLI)→ internal/cli.Run → 纯 HTTP 调用 server 的 /api(不共享业务代码)
internal/api(router.go:Gin+CORS+7个Handler注册;前端静态回退)
  → middleware.AuthRequired(Bearer JWT:验签后回查用户——不存在/禁用拒绝,改密后吊销旧 token)
  → handler(task/registry/catalog/chartrepo/chartupload/settings/auth)
  → 业务层:internal/task(调度)+ internal/skopeo(copy/list-tags/inspect)+ internal/registry(HTTP probe)+ internal/chart(tgz 解析/OCI 推送)
  → 存储层:internal/store(gorm 全局 DB 单例)+ internal/model(8张表)+ internal/crypto(AES-GCM 密码加密)
```

前端 dev 期走 Vite 代理,生产 embed 进二进制同源部署;鉴权均 Bearer token。

## 3. 目录级文件清单

- **main.go**:服务入口;embed 前端、初始化/恢复/保留清理/优雅退出。
- **cmd/irs/main.go**:CLI 入口,`os.Exit(cli.Run(os.Args[1:]))`。
- **internal/api**:`router.go` 路由+CORS+静态;`auth.go` 登录(限速+时序拉平+审计日志)/me/改密;`login_limiter.go` 用户名 5 次/IP 10 次失败锁 10 分钟(内存);`registry.go` 仓库 CRUD/test,DTO→model 时加密密码、normalizeHost 保 http:// 前缀;`catalog.go` 浏览 repo(Harbor v2 API / generic `_catalog`)/tags(skopeo list-tags)/projects;`task.go` 任务创建(List/Get/Cancel/Stream SSE);`settings.go` GET/PUT 系统设置(default_arch);`chartrepo.go` chart 仓库 CRUD/test;`chartupload.go` 浏览器文件/服务器路径上传,上限 100 文件×1GB,落 pending 记录后 `go runUploads` 异步推送;`helpers.go` ok/errResp 统一 `{data}`/`{error}` 包装。
- **internal/auth**:`jwt.go` GenerateToken(24h,rememberMe 7d)/ValidateToken(仅 HS256、强制 exp);`password.go` bcrypt 哈希 + IsStrongPassword(按 rune 计数 ≥8 位四类字符,空白不算特殊字符,≤72 字节)。
- **internal/chart**:`meta.go` 从 tgz 解析 Chart.yaml(name/version/appVersion,限 1MB,超限显式报错,name 有防注入正则);`push.go`(679 行)Target 构造、ChartMuseum POST /api/charts、OCI 手写 blob+manifest 推送(bearer challenge 认证,401 自动刷新重试)、Probe 连通测试;产物与 `helm push` 一致。
- **internal/cli**:`run.go` 命令注册表+分发(两段/一段匹配);`app.go` App 全局选项(--server/--token/--json/--config)、Client 构造(flag>env>配置文件)、120s 默认超时;`client.go` 纯标准库 HTTP 客户端:DoJSON 信封解析、401 自动重登并回写配置、SSE 流解析(StreamSSE)、multipart 上传;`config.go` ~/.irs-cli.json(0600)读写;`render.go` 表格/JSON/状态着色输出;`cmd_auth/cmd_registry/cmd_task/cmd_chart/cmd_settings.go` 各子命令实现。
- **internal/config**:环境变量配置(见 §8)。
- **internal/crypto**:AES-256-GCM,ENCRYPT_KEY 经 SHA-256 派生;未设 key 时明文透传;密文带 `enc:v1:` 版本前缀(换 key/清 key 解密显式报错,不再静默错乱)。
- **internal/middleware**:AuthRequired 验签后回查用户(不存在/禁用拒绝),改密后比对 token 签发时间吊销旧 token,注入 userID/username。
- **internal/model**:8 张表+状态/模式/仓库类型常量(见 §4)。
- **internal/registry**:`client.go` 共享 http.Client(insecure 可选)/BasicAuth 头;`probe.go` Registry v2 ping→basic/bearer challenge 探测,网络错误分类中文提示。
- **internal/skopeo**:`copy.go` Copy 拼装 `skopeo [--override-arch=X --override-os=linux] copy [--all] [--preserve-digests] --src/dest-authfile --src/dest-tls-verify docker://S docker://D`,双流回调经 channel 串行化,FriendlyCopyError 翻译常见错误,IsPreserveDigestsConflict 识别 SWR 拒收场景;`exec.go` runWithStream(bufio.Reader 无上限排空,单消费者串行回调)/captureStdout/BinVersion;`authfile.go` 临时 auth.json 写入/清理;`catalog.go` ListTags;`ref.go` ParseSource/StripScheme/ResolveTarget(三种模式目标 ref 计算,Docker Hub 官方域名归一化,空目标 host 拒绝)。
- **internal/store**:`store.go` Init(DSN:foreign_keys+WAL+busy_timeout 10s,连接池 4/2)、AutoMigrate 8 表、seed admin 与 default_arch、RecordLoginLog、RecoverChartUploads;`retention.go` StartRetention 每天批量(500/批)删过期任务与 chart 上传记录。
- **internal/task**:`manager.go` Manager 单例(队列 chan 64、worker×TASK_CONCURRENCY、SSE 订阅/历史 2000 条/取消注册,broadcast 锁内非阻塞发送);`runner.go` runTask 主流程;`recover.go` 启动恢复(running→failed、pending 重新入队);`types.go` 凭证加载/解密。
- **internal/testutil**:`testutil.go` NewTestDB(内存 SQLite+迁移,cleanup 还原全局 store.DB)、FakeSkopeo/UseFakeSkopeo(把 SkopeoBin 指向编译好的替身)、BuildChartTgz/WriteChartTgz、WaitFor 轮询;`fakeskopeo/main.go` skopeo 替身,按引用关键字模拟:成功/failcopy/noarch/preserveconflict/conflictonce/manifestunknown/slowcopy(30s 测取消)/dualstream(双流并发)/longline(2MB 超长行)/failtags/badjson(list-tags)/failinspect/varydigest(按 ref 可变 digest)。
- **web/src**:Vue3+pinia+vue-router+Element Plus。路由:`/login`(public)、`/tasks`、`/tasks/:id`、`/sync/new`、`/registries`、`/chart-repos`、`/chart-upload`、`/settings`,未登录守卫跳转。状态:仅 `stores/auth.js`(token 存 localStorage(记住我)/sessionStorage,30 分钟无操作自动登出)。API 封装:`api/client.js` axios 实例(baseURL=/api,30s 超时,请求注 Bearer,响应统一拆 {data}/{error},401 清态跳登录)、`api/index.js` 按域分组。组件:`CatalogBrowser`、`ChangePasswordDialog`、`ModePreview`;utils/ref.js 前端复刻后端 ref 解析逻辑(需与 skopeo/ref.go 保持一致)。
- **docs/cli.md** CLI 完整文档;**skills/irs-sync/SKILL.md** AI 技能(irs CLI 操作);**.github/workflows/docker-publish.yml** 手动触发的多架构构建推送 ACR。

## 4. 核心数据模型与数据流

**表**(internal/model/model.go):`users`、`registries`(type:generic/harbor/dockerhub/acr/swr,InSourceGroup 源镜像组)、`sync_tasks`、`sync_task_items`(「镜像×目标」展开,SourceHost 表匿名 docker.io)、`chart_repos`(oci/chartmuseum)、`chart_uploads`、`login_logs`、`system_settings`(default_arch)。

**同步任务全链路**:

1. `POST /api/tasks` → task.go:56 Create:校验(refs≤500、目标≤10、明细≤2000)→ ref 规整去重剥 scheme → 源解析:显式源或按主机名匹配 InSourceGroup 仓库,不匹配且 docker.io 则匿名 → `skopeo.ResolveTarget` 按模式算目标 ref → 展开明细落库 → `task.Instance().Enqueue(t.ID)`。
2. Manager worker 消费 → `runTask`(runner.go):事务加载+**仅 pending 可置 running 的乐观锁**(防终态任务重复投递重跑)→ 解析每条 item 生效源/目标 ID → loadRegistryCredsByID 解密凭证 → 预登录所有源仓库 registry.Probe,失败即中止(兜底按明细回填计数)→ WriteAuthFile 生成临时 auth.json → 按目标分组串行逐条 `skopeo.Copy`(SWR 目标自动关 --preserve-digests;失败特征匹配再降级重试一次)→ 成功后 InspectDigest 回填 → item 状态落库 → 任务终态+succeeded/failed/skipped 更新。arch 未指定时取 system_settings 的 default_arch。
3. 取消:`POST /api/tasks/:id/cancel` → Manager.Cancel 调 context cancelFunc,剩余 item 置 skipped。
4. 进度:SSE `GET /api/tasks/:id/stream`:先发 snapshot 事件→已结束任务从 DB 回放 item 结果→运行中先回放历史再实时转发,15s 心跳。
5. 重启恢复:main.go:42-45 → task/recover.go(running→failed+refreshTaskCounts,pending 重新入队)、store.go:125(chart 上传置失败)。

## 5. API 端点清单

- `GET /api/healthz`;`POST /api/auth/login`(公开)、`POST /api/auth/logout`、`GET /api/auth/me`、`PUT /api/auth/password`;其余均需 JWT。
- registries:GET/POST `/api/registries`、PUT/DELETE `/api/registries/:id`、POST `/api/registries/:id/test`。
- catalog:GET `/api/catalog/:id/repos|tags|projects`。
- tasks:POST/GET `/api/tasks`、GET `/api/tasks/:id`、POST `/api/tasks/:id/cancel`、GET `/api/tasks/:id/stream`(SSE)。
- settings:GET/PUT `/api/settings`。
- chart-repos:GET/POST `/api/chart-repos`、PUT/DELETE `/api/chart-repos/:id`、POST `/:id/test`。
- charts:POST `/api/charts/upload-files`(multipart)、POST `/api/charts/upload-paths`、GET `/api/charts/uploads`、POST `/api/charts/uploads/:id/retry`。

## 6. CLI 子命令

全局选项 `--server/--token/--json/--config`。命令:`login/logout/whoami/health/version`;`registries list|create|update|delete|test`;`catalog projects|repos|tags`;`tasks list|get|create(--wait)|cancel|logs(--follow SSE)`;`chart-repos list|create|update|delete|test`;`charts upload|upload-path|uploads|retry`;`settings get|set(--default-arch)`。

## 7. 配置项(config/config.go,.env.example)

`PORT`(8080)、`DB_PATH`(/data/…db,目录不可写回退 ./data)、`JWT_SECRET`(空则随机,重启即失效)、`ADMIN_USERNAME/PASSWORD`(仅首次 seed)、`ENCRYPT_KEY`(空=明文存密码)、`SKOPEO_BIN`、`TASK_CONCURRENCY`(1-8,默认1)、`TASK_RETENTION_DAYS`(30,0=永久)、`LOGIN_LOG_RETENTION_DAYS`(180)。

## 8. 测试基础设施

63 个 Go _test.go(阶段 4 治理前为 46 个),覆盖 handler/任务调度/取消恢复/SSE/CLI 解析/skopeo 参数/错误翻译/chart 推送/认证吊销/加密迁移等;含跨包 e2e(api/e2e_test.go、cli/e2e_test.go)与 testutil 自身契约测试。testutil 提供内存 DB、FakeSkopeo 编译注入、chart tgz 构造、WaitFor;前端另有 vitest 纯函数测试(web/tests,29 例)。Makefile `test-cov` 以跨包合并覆盖率 ≥90% 为验收线(当前 90.0%,分母含 main 包,口径偏保守)。

## 9. 构建与交付

- **Dockerfile** 三阶段:①node20 构建 web/dist;②golang1.23.12 交叉编译 `CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH`,COPY 前端产物供 go:embed;③alpine3.21 + skopeo/ca-certificates/tzdata,VOLUME /data,EXPOSE 8080。
- **Makefile**:help/build/build-no-cache/build-multiarch/build-push(阿里云 ACR,latest+sha+版本三标签)/login/up/down/restart/up-compose/status/logs/shell/health/run-dev/web-dev/web-build/cli/tidy/test/test-cov/env-init/clean/clean-all。
- **.github/workflows/docker-publish.yml**:仅 workflow_dispatch,QEMU+buildx 多架构推 ACR。
- **docker-compose.yml**:单服务,健康检查 wget /api/healthz,密钥强制来自 .env(JWT_SECRET/ENCRYPT_KEY 缺失报错)。

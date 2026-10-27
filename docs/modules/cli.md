# CLI(irs)

> 源码:`internal/cli/`(入口 `cmd/irs/`),纯 Go 标准库实现,零第三方依赖。
> 基于 main 分支 2026-10-27 代码(阶段 4 修复已合入)。

## 1. 职责与架构

`irs` 是平台的命令行客户端,把 Web 端全部核心能力(登录、镜像仓库、同步任务、chart 上传、系统设置)封装为可脚本化命令,面向人工终端与 AI/CI 自动化(`--json` + 退出码契约)。

- **命令注册表 + 分发**:`run.go:18-27` 把 6 组命令(auth/registry/catalog/task/chart/settings)聚合进 `allCommands`;`Run`(run.go:30-70)先 `parseGlobals` 剥离头部全局选项,再按"两段(组+子命令)优先、一段兜底"匹配(run.go:42-57),命令参数从名称段之后切出(run.go:64)。退出码:0 成功 / 1 命令失败 / 2 用法或全局参数错误。
- **纯 HTTP 契约**:CLI 不 import 任何 server 业务包(api/task/skopeo 等),只依赖 REST API 的 JSON 形状(各 `*VO` 结构体手工镜像服务端视图)。好处是 CLI 与服务端可独立发布、e2e 用 httptest 即可验证;代价是字段漂移只能靠 e2e 测试兜底,且服务端无单查接口时 CLI 被迫 list 后过滤(cmd_registry.go:141-154、cmd_chart.go:246-259)。
- **状态**:`App`(app.go:23-31)承载一次调用的全局选项与登录态;凭据优先级 flag > 环境变量(IRS_SERVER/IRS_TOKEN/IRS_USER/IRS_PASSWORD)> `~/.irs-cli.json`(app.go:61-88)。

## 2. 命令与全局选项清单

全局选项(须置于命令之前,精确匹配,run.go:94-122):`--server`、`--token`、`--json(-j)`、`--config`、`--version(-v)`、`--help(-h)`。共 29 个命令:

| 组 | 命令 | 关键 flag | 位置 |
|---|---|---|---|
| auth | login | `--server --username --password --token`(密码可走 IRS_PASSWORD) | cmd_auth.go:11-18 |
| | logout / whoami / version | — | |
| | health | `--server`(公开接口,免登录;先于 confirmClient 解析,cmd_auth.go:148) | |
| registries | list / create / update / delete / test | create/update 共享表单 `--name --host --type --username --password --insecure --default-project`;update 预填当前值;delete 需 `--yes` | cmd_registry.go:11-17 |
| catalog | projects / repos / tags | repos:`--project --q`;tags:`--repo` 必填 | cmd_registry.go:20-24 |
| tasks | list | `--status --page --size` | cmd_task.go:17-23 |
| | get / cancel | `<id>` | |
| | create | `--source --target --mode --project --arch --refs(可多次)/--refs-file/--wait` | |
| | logs | `<id> [--follow]`(SSE 实时跟踪) | |
| chart | chart-repos list/create/update/delete/test | 表单同 registries + `--project`(OCI 必填) | cmd_chart.go:16-26 |
| | charts upload | `<repo-id> <文件/目录>... [--no-wait]`(目录展一层 *.tgz,≤100 个) | |
| | charts upload-path / uploads / retry | upload-path 提交服务端路径;retry `--wait`(默认 true) | |
| settings | get / set | set:`--default-arch` | cmd_settings.go:8-11 |

## 3. 客户端机制

`Client`(client.go:23-35)是 REST API 的极薄封装:

- **DoJSON 信封**:统一解包 `{data,total,page,size}`(client.go:67-94),响应读取上限 64MB(client.go:149);请求体 `[]byte` 直传、其余 JSON 序列化(client.go:119-131)。
- **401 自动重登**:`request`(client.go:98-112)遇 401 且配置了 IRS_USER/IRS_PASSWORD 时静默重登一次并重试;新 token 经 `OnTokenRefresh` 回调持久化回配置文件(app.go:79-86)。登录接口自身不重试防递归。
- **SSE 流解析**:`StreamSSE`(client.go:282-359)用独立无超时 `http.Client`(client.go:293,生命周期由 ctx 控制);逐行解析 `event:`/`data:`/空行,心跳注释行跳过,多行 data 按 `\n` 拼接(client.go:326-329),EOF 尾帧无空行时补发(client.go:350-354)。`followTask`(cmd_task.go:332-409)据此驱动任务跟踪,退出判定以 `task_finished` 携带的服务端统计为准(迟到订阅者不漏判失败,cmd_task.go:402-404)。
- **multipart 上传**:`UploadFiles`(client.go:212-271)把全部文件读进内存 `bytes.Buffer` 再一次性 POST;`charts upload` 用 30 分钟 ctx(cmd_chart.go:368),随后 `waitUploads` 每秒分页轮询直到全部终态(cmd_chart.go:440-501)。
- **本地配置**:`~/.irs-cli.json` 保存 server/token/username,0600 权限、非原子写(config.go:53-68);logout 直接删文件(服务端 JWT 无状态)。

## 4. 测试现状

约 2700 行、85+ 测试函数,层次完整:单元层(httptest 假后端覆盖 DoJSON/401 重登/SSE/multipart,client_test.go、client_extra_test.go)、命令层(全部 29 命令的成功/参数错误/API 错误路径,cmd_*_test.go、cmd_errors_test.go)、回归层(阶段 4 修复逐项锁死:a_regressions_test.go)、边界层(b_edge_test.go:SSE 非 200/读错误、含 \t 单元格、翻页找记录)、e2e 层(真实 api.SetupRouter + 真 DB + fake skopeo 走通 health→login→tasks --wait 全链路,e2e_test.go:26-42)。盲区:UploadFiles 401 重试、多文件目录展开失败中途中断、GBK 终端输出。

## 5. 已知限制与技术债

阶段 4 已修复 health --server 顺序、--help/-h、全局 flag 精确匹配、refs 吞入/多次覆盖、followTask 退出码、size 除零、BOM、SSE 尾帧/多行 data、--project 文案。仍存:

1. `UploadFiles`/`StreamSSE` 不走 401 自动重登;非 JSON 错误体消息为空(client.go:254-263、299-303)。
2. multipart 全内存缓冲:100 个 tgz 峰值内存=文件总量(client.go:213-236)。
3. 超时口径不一致:`http.Client` 整体 10min(client.go:49)小于上传 ctx 30min(cmd_chart.go:368),大上传先被 client 掐断。
4. 配置写非原子、Windows 下 0600 无效(token 明文可读,config.go:68)。
5. flag 错误英文原文输出两遍:`newFlagSet` 输出到 stderr(cmd_auth.go:179-187)+ `Run` 再打印(run.go:65-67)。
6. `--no-wait` 与 `--wait=false` 语义重复(cmd_chart.go:344、399、562)。
7. `irs health --json` 不识别(cmd_auth.go:139 用裸 FlagSet,未注册 --json)。
8. `login --token` 残留上次的 TokenExpiresAt(cmd_auth.go:59-71)。
9. GBK 终端 `✓/✗` 乱码(render.go:67-76);`sortedKeys` 手写插入排序(cmd_settings.go:70-81)。
10. cmd_*.go 五文件重复同一八行模式(confirmClient→ctx→DoJSON→JSONOut 分支,约 20 处);followTask 6h 超时硬编码(cmd_task.go:335);CLI 仅暴露单 --target(cmd_task.go:242)而服务端已支持多数组。

## 6. 重构方案

### P1(功能正确性,优先)

| # | 问题(file:line) | 方案 | 影响面 | 工时 | 风险 |
|---|---|---|---|---|---|
| 1 | UploadFiles/SSE 无 401 重登、非 JSON 错误体空消息(client.go:254-263、282-303) | 抽公共 `sendWithAuth(ctx, reqFn)`:统一 401 判定→login→重试,并复用 doOnce 的错误体解析(含非 JSON 截断) | 长会话上传/跟踪遇 token 过期即失败;需新增 UploadFiles 401 测试 | 3h | 低 |
| 2 | client 10min 超时 < 上传 ctx 30min,大上传被掐断(client.go:49、cmd_chart.go:368) | `http.Client` 去掉整体 Timeout,超时一律由 ctx 控制(SSE 已是此模式);常规调用本就带 120s ctx | 全部请求路径;等待型调用改显式 ctx | 1h | 低 |
| 3 | multipart 全内存缓冲,OOM 风险(client.go:213-236) | `io.Pipe` + 后台 goroutine 写 multipart writer,流式上传(chunked);Content-Length 由 pipe 侧放弃 | 仅 charts upload;服务端为 gin 标准解析,兼容 | 2h | 中:需处理管道写错误传播与 ctx 取消 |
| 4 | 配置非原子写、Windows 0600 失效(config.go:53-68) | 写同目录临时文件 + rename;Windows 下用 `os.Chmod` 之外补 ACL(或仅提示);token 写入前可选仅存内存 | 登录态持久化;logout/clear 不变 | 1.5h | 低 |

### P2(健壮性/设计)

| # | 问题(file:line) | 方案 | 影响面 | 工时 | 风险 |
|---|---|---|---|---|---|
| 5 | flag 错误英文原文重复两遍(cmd_auth.go:179-187、run.go:65-67) | FlagSet `SetOutput(io.Discard)` + 自定义 Usage 返回中文错误,只打印一次 | 所有子命令参数错误提示 | 1h | 低 |
| 6 | cmd_*.go 五文件重复"confirmClient→ctx→DoJSON→JSONOut"八行模式(约 20 处) | App 上抽 `a.call(method, path, body, out)`(含 confirm/ctx/错误包装)与 `a.emit(vo, humanFn)`(JSON/表格双分支),逐命令替换 | 全部命令文件,等价重构;85+ 测试兜底 | 6h | 中:触碰面广 |
| 7 | waitUploads 每秒全量翻页扫描,无退避(cmd_chart.go:440-501) | CLI 侧加指数退避(1s→5s 封顶)+ 失败重试;中期推动服务端提供按 ID 批查或上传事件流 | charts upload/retry 等待路径 | 2h | 低 |
| 8 | `--no-wait` 与 `--wait=false` 重复(cmd_chart.go:344、399、562) | 统一为 `--wait`(默认 true),`--no-wait` 保留一个版本作别名并标注废弃 | charts upload/upload-path/retry;脚本兼容 | 1h | 低 |
| 9 | `health --json` 不识别、`login --token` 残留 TokenExpiresAt(cmd_auth.go:139、59-71) | health 换 `a.newFlagSet` 并注册 --server;token 分支显式清空 `cfg.TokenExpiresAt` | health/login 输出与排障 | 0.5h | 低 |
| 10 | GBK 终端 ✓/✗ 乱码(render.go:67-76) | StatusMark 改纯 ASCII(`OK`/`FAIL`),或检测 `windows` 代码页/`IRS_ASCII` 开关;同时覆盖 `[成功]/[失败]` 前缀评估 | 表格与进度输出 | 1h | 低 |

### P3(风格/增强)

| # | 问题(file:line) | 方案 | 影响面 | 工时 | 风险 |
|---|---|---|---|---|---|
| 11 | sortedKeys 手写插入排序、parseIDArg 手写数字解析(cmd_settings.go:70-81、cmd_auth.go:190-205) | 换 `slices.Sorted(maps.Keys(m))` 与 `strconv.ParseUint` | 纯内部等价替换 | 0.5h | 低 |
| 12 | update 前需 list 全量过滤取单条(cmd_registry.go:141-154、cmd_chart.go:246-259) | 推动服务端补 `GET /api/registries/:id`、`/api/chart-repos/:id`,CLI 消费(跨模块) | registries/chart-repos update | 1h(服务端另计) | 低 |
| 13 | CLI 仅单 --target、followTask 6h 硬编码(cmd_task.go:242、335) | `--target` 改可重复/逗号分隔,展开多数组;超时提为 `--timeout` flag(默认 6h) | tasks create/logs | 1.5h | 低 |
| 14 | SSE 非 200 不解析 `{error}`、UploadFiles 忽略读体错误(client.go:253、299-303) | 抽 `parseErrBody(raw)` 统一复用(与 #1 合并实施) | 错误提示质量 | 0.5h | 低 |

合计 14 项:P1 4 项(7.5h)、P2 6 项(11.5h)、P3 4 项(3.5h)。建议顺序:#2/#5/#9/#11 先行(低风险速赢),#1+#14、#6 为核心改造,#3/#4 随后,#7/#12 需服务端配合排期。

# API 层(api)

> 范围:`internal/api`(7 个 handler + router/helpers/login_limiter)与 `internal/middleware`(鉴权中间件)。基于 2026-10-27 代码(阶段 4 修复后)。

## 1. 职责与边界

`SetupRouter`(router.go:18)统一装配:CORS 白名单(router.go:27-45,仅本地开发 origin)、`/api/healthz`(router.go:49)、7 个 handler 的路由注册(router.go:53-59)、前端静态资源与 SPA 回退(router.go:62-103,assets 长缓存/index.html no-cache)。

Handler 清单与调用关系:
- **auth.go** 登录/登出/改密,依赖 internal/auth(JWT+bcrypt)、internal/store(登录日志)、包内 login_limiter(login_limiter.go:89 全局 loginGuard,5 次/用户、10 次/IP、锁 10 分钟);
- **registry.go / chartrepo.go** 仓库配置 CRUD,依赖 internal/crypto 加密密码、internal/registry.Probe(registry.go:200)/internal/chart.Probe(chartrepo.go:210)做连通测试;
- **catalog.go** 浏览仓库 repo/tag/project:Harbor 走 v2 API 自拼 HTTP(catalog.go:212-351),tag 走 internal/skopeo.ListTags 子进程(catalog.go:118,30s 超时 catalog.go:25);
- **task.go** 同步任务:创建后经 internal/task.Instance().Enqueue 入队(task.go:235)、Cancel(task.go:339)、Subscribe/Unsubscribe 驱动 SSE(task.go:361-362),解析引用用 internal/skopeo;
- **chartupload.go** chart 上传:internal/chart.ParseFile/Push,浏览器文件落临时目录异步裸 goroutine 推送(chartupload.go:125/208/279),启动清扫 `irs-chart-*`(chartupload.go:415,main.go:43 调用)。

鉴权链路:除 login/healthz 外全部挂 `middleware.AuthRequired()`(各 handler RegisterRoutes 内,如 task.go:524)。中间件验 JWT 签名后回查用户(middleware/auth.go:40-49 拒绝已删/禁用),并以 `pwd_changed_at` 对比 token `iat` 吊销改密前签发的旧 token(middleware/auth.go:51-55);改密 handler 写入该时间戳(auth.go:162-168)。登录失败有等耗时 bcrypt 拉平时序(auth.go:61,login_limiter.go:94)。

## 2. 端点清单

| 方法+路径 | 鉴权 | 实现 |
|---|---|---|
| GET /api/healthz | 公开 | router.go:49 |
| POST /api/auth/login | 公开+限速 | auth.go:37 |
| POST /api/auth/logout / GET /api/auth/me / PUT /api/auth/password | Bearer | auth.go:102/115/136 |
| GET/POST /api/registries;PUT/DELETE /api/registries/:id;POST /api/registries/:id/test | Bearer | registry.go:103-202 |
| GET /api/catalog/:id/repos、/tags、/projects | Bearer | catalog.go:56/94/156 |
| POST/GET /api/tasks;GET /api/tasks/:id;POST /api/tasks/:id/cancel;GET /api/tasks/:id/stream(SSE) | Bearer | task.go:56/280/313/328/357 |
| GET/PUT /api/settings | Bearer | settings.go:34/58 |
| GET/POST /api/chart-repos;PUT/DELETE /:id;POST /:id/test | Bearer | chartrepo.go:117-212 |
| POST /api/charts/upload-files、/upload-paths;GET /api/charts/uploads;POST /api/charts/uploads/:id/retry | Bearer | chartupload.go:62/132/229/263 |

## 3. 请求/响应约定

- 信封:成功 `{data}`(helpers.go:10);错误 `{error}` + 400/401/403/404/429/500(helpers.go:15-23);分页额外附 `total/page/size`(task.go:309)。业务级失败(连接测试、catalog 上游错误)恒 200,以 `{data:{ok:false,message}}` 表达(registry.go:201、catalog.go:74/120/173),前端按 ok 字段渲染红绿。
- 校验上限:refs≤500(task.go:44)、目标仓库≤10(task.go:48)、展开明细≤2000(task.go:49)、分页 size≤100(task.go:286)、chart 单文件≤1GB(chartupload.go:32)、单次≤100 个(chartupload.go:26)、multipart 内存缓冲 64MB(router.go:23)。
- SSE(task.go:357-472):`event:`+`data:` 帧,事件类型 snapshot/task_started/item_success/item_failed/task_finished;先订阅再查快照消竞态,运行中回放历史、已结束从 DB 回放明细(task.go:396-437);15s 心跳注释帧(task.go:464)防代理断连;写响应单 goroutine 防帧损坏(task.go:446-448)。

## 4. 测试现状

api 包 18 个测试文件约 64 例,middleware 2 个文件 7 例。代表性:阶段 4 回归 review_red_test.go(HTTP 仓库三接口、取消终态保序、multipart 清理)、boundary_b_test.go(锁定到期/明细 2000 边界/Stream 客户端断开/终态回放)、auth_revocation_test.go(改密吊销旧 token)、chartupload_size_test.go(超限拦截)、task_handler_test.go 10 例(创建校验/自动匹配/SSE)、middleware 边缘用例(小写 bearer/注入身份)。盲区:XFF 伪造下的限速、login_limiter 内存增长、SSRF、SSE 阻塞 Shutdown、chart 并发上传。

## 5. 已知限制与技术债

1. **XFF 伪造**:未调 `SetTrustedProxies`(全仓无),gin 默认信任所有代理,`c.ClientIP()`(auth.go:43)可伪造,login_limiter 的 IP 维度(auth.go:48)形同虚设。
2. **login_limiter 内存无界**:records 仅成功登录 reset(login_limiter.go:80-86),失败用户名/伪造 IP 永不清理。
3. **上传无总量上限**:无 `http.MaxBytesReader`,upload-files 仅靠单文件 1GB 与件数 100 限制(chartupload.go:94),单请求仍可传 100GB 落盘。
4. **SSRF 面**:test/catalog 以服务端身份探测任意 host(registry.go:200、catalog.go:212-351),未禁环回/链路本地地址;`io.ReadAll` 无上限(catalog.go:207)。
5. **密码无法清空**:registryDTO/chartRepoDTO 空密码=保留旧值(registry.go:54-61、chartrepo.go:54-61)。
6. **task.go Create 184 行**(task.go:56-239),绑定/校验/解析/落库四段揉在一起;分页解析在 task.go:281-288 与 chartupload.go:230-237 重复;`okWith` 重复 `ok`(chartupload.go:409)。
7. **harborRepos 吞单 project 错误**(catalog.go:253-257 `continue`),整仓列表静默缺项目。
8. ~~残余小风险:Cancel 竞态~~(已修复:取消落库为条件更新 WHERE status IN pending/running,RowsAffected==0 返回"任务已结束");registry.Test/Probe 无显式超时(registry.go:200);chartRepoTarget 吞解密错(chartrepo.go:111 `pass, _`),与 registry.go:192-199 显式报错口径不一;registry Delete 无任务引用守卫(registry.go:170-177);chart 推送裸 goroutine 无并发上限且整文件读内存(chartupload.go:125/351);SSE 长连接阻塞优雅退出(task.go:451-471 无服务级关闭信号)。

## 6. 重构方案

**P1(安全/资源防打穿)**
1. **信任代理策略**:问题见债 1。方案:`r.SetTrustedProxies(nil)`(直连部署)或按 TRUSTED_PROXIES 配置白名单,router.go:21 处。影响:仅 ClientIP 来源,限速/日志更准。工作量 1h。风险:反代后真实 IP 取不到,需文档说明配置。
2. **上传总量 MaxBytesReader**:问题见债 3。方案:对 upload-files 包 `http.MaxBytesReader`(上限=100×1GB 或独立配置),超额 413。影响:仅该端点。工作量 1h。风险:与单文件校验叠加需测边界文案。
3. **login_limiter 清扫**:问题见债 2。方案:locked()/recordFailure() 时惰性删除过期记录,或定时器每 10min 全表扫描。影响:内存有界。工作量 1h。风险:低。
4. **SSRF 收敛**:问题见债 4。方案:公共 `validateEgressHost()` 禁环回/链路本地/私有段(可配置放行),用于 registry.Test、chartrepo.Test、catalog 三个出网点;doRequest 加 ReadAll 上限。影响:内网用户配 127.0.0.1 仓库会被拒,需白名单。工作量 3h。风险:误伤合法内网地址,需配置开关。

**P2(正确性/可维护性)**
5. **密码可清空**:问题见债 5。方案:DTO 增加 `clear_password bool` 字段,置真时清空 PasswordEnc;前端配合。影响:registry/chartrepo 两 handler 与 web 表单。工作量 2h。风险:旧前端不传字段,行为不变,兼容。
6. **Create 拆分**:问题见债 6。方案:拆为 `bindAndValidate`(参数+上限)、`resolveSources`(去重+源匹配)、`expandItems`(×目标展开)、`persistAndEnqueue` 四个方法,Create 只留编排;`matchSourceGroup` 已是范本(task.go:254)。影响:task.go 单文件,测试不变。工作量 3h。风险:纯重构,回归靠现有 10 例。
7. **harborRepos 错误上抛**:问题见债 7。方案:单 project 失败时返回 `partial:true`+缺失项目列表,而非静默 continue。影响:catalog.go Repos 响应结构加字段,前端提示。工作量 2h。风险:低。
8. **chart 上传并发上限**:裸 goroutine+1GB 整读可叠加打满内存(chartupload.go:125/351)。方案:带缓冲 channel 信号量限并发(如 2),runOne 改 `io.Reader` 流式喂 chart.Push(需 chart 包配合)。影响:chartupload.go 与 internal/chart 接口。工作量 4h(信号量 1h,流式 3h)。风险:流式改造动 chart.Push 签名。
9. **Cancel 条件更新(已实现)**:Updates 已带 `WHERE status IN ('pending','running')`,受影响行数为 0 时返回"任务已结束",终态不被覆盖(task.go Cancel);债 8 第一条随之关闭。

**P3(整洁/一致)**
10. **公共校验/工具抽离**:分页解析抽 `parsePage(c)`;registryDTO 与 chartRepoDTO 的 trim/加密逻辑抽 `applyPassword(m, dto)`;合并 okWith 入 helpers.go。工作量 2h,风险低。
11. **SSE 优雅退出**:Stream 的 select 增加 server 关闭 channel(task 包提供),Shutdown 不再等满 10s。工作量 2h。风险:需 task 包加通知机制,跨模块。
12. **出网超时统一**:registry.Test(chartRepo.Test 同)套 30s 超时,与 catalog.go:25 的 listTagsTimeout 对齐;抽公共常量。工作量 1h。风险:低。
13. **解密口径统一**:chartRepoTarget(chartrepo.go:111)改为返回 error,Test/push 路径显式报"密码解密失败,请检查 ENCRYPT_KEY",对齐 registry.go:195。工作量 1h。风险:需处理 push 路径错误传播。
14. **Delete 引用守卫**:registries/chart-repos Delete 前统计关联任务/上传记录,存在时 409 提示(或级联确认)。工作量 2h。风险:历史数据悬空引用的存量需兼容提示。

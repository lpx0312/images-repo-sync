# 任务执行与持久化核心(task/store/config/model)

> 覆盖 `internal/task`、`internal/store`、`internal/config`、`internal/model` 与服务端入口 `main.go`。基线:main 分支,阶段 4 修复已合入。

## 1. 职责与架构

- **Manager 单例**(`internal/task/manager.go:66-86`):`sync.Once` 懒初始化,持有 64 缓冲的任务 ID 队列(`manager.go:69`)、按 taskID 组织的 SSE 订阅表、运行中任务的 cancel 注册表(`manager.go:53-55`)与事件历史(上限 2000 条,`manager.go:38`)。worker 数取 `TASK_CONCURRENCY`,下限 1 防 0 worker(`manager.go:78-80`)。
- **runTask 全流程**(`internal/task/runner.go:41-371`):事务内加载任务+明细并以乐观锁置 running → 解析每条明细实际生效的源/目标仓库 ID(0 回退任务级,旧数据兼容,`runner.go:136-161`)→ 逐仓库解密凭证(`types.go:46-65`)→ 预登录全部源仓库(probe 提前暴露凭证问题,`runner.go:182-194`)→ 生成 skopeo authfile(`runner.go:197-209`,结束统一清理)→ 按目标分组串行 skopeo copy(华为 SWR 目标自动关 `--preserve-digests`,拒收时特征识别降级重试,`runner.go:231,281-288`)→ 统计并写终态。任何错误路径由 defer 兜底:剩余明细置 failed/skipped + `refreshTaskCounts` 回填计数 + 任务置 failed/canceled(`runner.go:53-88`)。
- **启动恢复**(`internal/task/recover.go:16-52`):running 任务置 failed(明细标"服务重启,任务中断"),pending 任务重新入队;`store.RecoverChartUploads`(`store/store.go:125-139`)把中断的 chart 上传直接置失败;`api.SweepTempUploads` 清扫临时目录(`main.go:42-43`)。
- **保留清理**(`internal/store/retention.go:19-31`):启动即清一次,之后每 24h;按终态+创建时间分批(500/批)删除,任务+明细两步删除包在同一事务(`retention.go:75-89`)。
- **配置加载**(`internal/config/config.go:44-104`):环境变量覆盖默认值;DB 目录写探测失败回退 `./data`;JWT_SECRET 空则随机(熵源故障 panic,`config.go:131-139`)。
- **持久化**(`store/store.go:24-68`):glebarez/sqlite 纯 Go 驱动,WAL+busy_timeout 10s+外键,MaxOpenConns=4;AutoMigrate 8 表;首次启动 seed 默认 admin 与 `default_arch` 设置。

## 2. 任务状态机

任务:`pending → running → success / failed / canceled`;明细:`pending → running → success / failed / skipped`。

- **pending→running**:事务内 `UPDATE ... WHERE status='pending'`,RowsAffected=0 即任务已非 pending(终态/已取消),跳过执行且不触发兜底改写(`runner.go:108-134`)——防重复投递重跑终态任务;已被取消则经 `errAlreadyCanceled` 直接退出(`runner.go:99-101,types.go:12`)。
- **取消**:队列中任务由 API 直接置 canceled(`api/task.go:339-343`);运行中任务经 `Manager.Cancel` 调 cancel func(`manager.go:176-185`),ctx 取消后当前明细置 skipped、剩余 pending/running 统一 skipped,终态 canceled(`runner.go:304-339,344-345`)。
- **兜底**:预执行失败(probe/凭证/authfile)时明细全置终态,defer 内 `refreshTaskCounts` 按明细实际状态回填 succeeded/failed/skipped/total(`recover.go:56-84`),消除"任务 failed 但 Failed=0"的不一致。
- **取消竞态(已修复)**:取消落库为条件更新(WHERE status IN pending/running,RowsAffected==0 不改写终态并返回"任务已结束",api/task.go Cancel);runTask 侧另有 pending 乐观锁双重防护。

## 3. 并发模型

- worker 池默认 1 串行(上限 8,`config.go:88-93`),并发只作用于不同任务;单任务内镜像按目标分组串行复制(`runner.go:216-325`)。`Instance()` 必须在 `config.Load()` 之后调用,否则读不到配置(下限 1 兜底)。
- 广播在锁内非阻塞发送,与 `Unsubscribe` 的 close 互斥,杜绝 send-on-closed panic;慢订阅者(64 缓冲满)静默丢事件(`manager.go:137-156`)。
- SQLite 侧:WAL 多读单写 + busy_timeout 化解 worker 写与 SSE 读争用(`store.go:26-44`)。
- 与 HTTP 层关系:Create 成功后 `Enqueue`(`api/task.go:235`);Stream 先订阅再查库消除竞态,运行中任务回放内存历史,已结束任务从 DB 回放明细(`api/task.go:357-472`)。chart 上传不走本队列,由 api 层裸 goroutine 执行。

## 4. 数据模型(8 表)

| 表 | 要点 | 索引 |
|---|---|---|
| users | 单 admin;`pwd_changed_at` 供改密吊销旧 token | username 唯一 |
| registries | host+凭证(密码加密存 `password_enc`);`in_source_group` 支撑自动匹配源 | name 唯一 |
| sync_tasks | 模式/项目/架构 + 四计数 + 状态机字段;`items` 外键关联 | `(status,created_at)` 复合、source/target_registry_id 单列(`model.go:105,108`) |
| sync_task_items | 每条镜像×目标展开,自带源/目标仓库 ID 与匿名源 host 快照 | task_id |
| chart_repos / chart_uploads | chart 仓库与上传记录;上传复用 TaskStatus 常量 | status、chart_repo_id |
| login_logs | 登录审计 | user_id/username/status/created_at(`model.go:190`) |
| system_settings | 键值配置,目前仅 `default_arch` | key 主键 |

## 5. 配置项

| 变量 | 默认 | 说明 |
|---|---|---|
| PORT | 8080 | 监听端口,非数字未校验(`config.go:47-49`) |
| DB_PATH | /data/images-repo-sync.db | 目录不可写回退 ./data(`config.go:54-64`) |
| JWT_SECRET | 空→每次启动随机 | 随机则重启全员掉线;无强度校验、多副本不共享 |
| ENCRYPT_KEY | 空=明文 | registry 密码加密密钥 |
| ADMIN_USERNAME / ADMIN_PASSWORD | admin / admin123 | 仅 users 空时 seed,弱默认无强制修改 |
| SKOPEO_BIN | skopeo | skopeo 可执行路径 |
| TASK_CONCURRENCY | 1 | worker 数,clamp 1..8 |
| TASK_RETENTION_DAYS | 30 | 终态任务保留天数,0=永久 |
| LOGIN_LOG_RETENTION_DAYS | 180 | 登录日志保留天数,0=永久 |

## 6. 测试现状

task 包 12 个用例:manager 覆盖历史回放/上限/清除、并发 emit-subscribe、慢订阅者丢弃、Cancel 未注册分支;runner 覆盖 probe 失败后计数一致、终态防重跑、probe 阶段取消、updateItem DB 失败注入;recover 覆盖 running/pending 两分支。store 覆盖 seed/掩码/审计日志/上传恢复/初始化失败;retention 8 例含两步删除原子性与分批;config 13 例含目录回退与边界值;model 断言表名、常量与索引存在性。盲区:优雅退出(信号级)、Enqueue 满队列路径、runTask 正常成功全链路(依赖 fake skopeo 的单点分支)、Cancel API 竞态时序、多 worker 并发。

## 7. 已知限制与技术债

1. Manager 无 Stop/drain:SIGTERM 只停 retention 与 HTTP,worker 与运行中 skopeo 被硬杀,目标仓库残留半推层(`main.go:85-94`);SSE 长连接仅监听客户端断开,`srv.Shutdown` 必然等满 10s(`api/task.go:451-470`)。
2. Enqueue 队列满时起无界兜底 goroutine 阻塞投递(`manager.go:92-95`)。
3. runTask 单函数 371 行;updateItem 吞错仅日志,DB 失败后内存计数与库状态脱节(`runner.go:374-378`)。
4. 慢订阅者丢事件无标记,前端无从感知降级。
5. task 包硬编码表名字符串;ChartUpload 复用 TaskStatus 常量(canceled 语义不符)。
6. JWT_SECRET 无强度校验/多副本告警;admin123 弱默认;PORT 非数字;`Load()` 恒返回 nil 且改写全局单例,可测试性差。
7. Registry 删除无引用守卫且模型无外键,历史任务悬空引用。
8. API Cancel 终态覆盖竞态(已修复:取消落库改为条件更新 WHERE status IN pending/running,api/task.go Cancel);loadRegistryCredsByID` 的 Row().Scan 对 NULL 脆弱。

## 8. 重构方案

### P1

**P1-1 优雅退出链路(Manager.Stop + BaseContext + main 编排)**
问题:见 §7.1,SSE 与 worker 两路都拖死 Shutdown。方案:Manager 增加 `BaseContext`(context.WithCancel),runTask 的 ctx 由其派生而非 `context.Background()`(`runner.go:44`);`Stop()` 三步——停投递、Cancel 全部注册的 cancelFunc、WaitGroup 等 worker 收尾(drain 超时则按重启中断语义置 failed);main 编排改为:收到信号 → 先触发 Manager.Stop(等待 N 秒)→ 关 SSE(api 包暴露 `Shutdown()`,向所有活跃 Stream 发结束帧)→ `srv.Shutdown` → `sqlDB.Close`。影响面:`task/manager.go`、`runner.go`、`main.go`、`api/task.go`(Stream select 增加关闭 case)、`api/router.go`。工作量:8-10h。风险:中——需定义"关闭中被取消任务"的终态(canceled+注明),drain 超时兜底,进程级测试需构造信号。

**P1-2 runTask 拆分 + updateItem 错误传播**
问题:runTask 371 行承担 7 段职责(`runner.go:41-371`);updateItem 吞错。方案:拆 `prepareTask`(加载+乐观锁)、`probeSources`、`buildAuthFiles`、`runItemGroup`(单目标循环,含 SWR 降级重试)、`finalizeTask`;updateItem 返回 error,由调用方累计并在终态 error 注明"部分明细状态未落库";先补 runTask 正常成功路径的 golden 测试再动刀。影响面:仅 task 包及测试。工作量:6-8h。风险:低——纯结构重构,行为由现有回归测试锁定。

### P2

**P2-1 Enqueue 兜底 goroutine 无界**:队列满即 `go func` 阻塞投递,可无限堆积。方案:改为带超时的 select 返回 error,满时任务留 pending 由恢复扫描/短周期轮询补投;或队列改无界 slice+cond。影响面:manager.go、api/task.go:235。工作量:2h。风险:低。
**P2-2 API Cancel 乐观锁(已实现)**:取消落库已改为条件更新(WHERE status IN pending/running + RowsAffected==0 时返回"任务已结束",api/task.go Cancel),R1 复审与 R2 终审核验落地;测试 TestCancelFinishedTaskKeepsTerminalState 固化主线。
**P2-3 慢订阅者丢事件标记**:丢弃分支静默。方案:订阅结构加 dropped 计数,Stream 转发前检查并向客户端发 `event: lag`,前端提示降级轮询。影响面:manager.go、api/task.go、TaskDetailView.vue。工作量:4h。风险:低。
**P2-4 JWT_SECRET 强度校验与弱默认口令**:<16 字节告警、多副本场景提示;ADMIN_PASSWORD 为默认值时首登强制改密。影响面:config.go、auth、api。工作量:5h。风险:低。
**P2-5 Registry 删除引用守卫**:删除前检查 sync_tasks 两列引用,有则 409 提示。影响面:api/registry.go。工作量:2h。风险:低。

### P3

**P3-1 配置可测试性 + PORT 校验**:`Load()` 返回实例供注入(全局单例渐进收编);PORT 用 strconv.Atoi 校验。工作量:3h。风险:低。
**P3-2 表名常量化 + ChartUpload 独立状态常量**:task 包改用 `model.Xxx{}.TableName()`;ChartUpload 定义独立 pending/running/success/failed 常量(值不变,数据零迁移)。工作量:2h。风险:低。

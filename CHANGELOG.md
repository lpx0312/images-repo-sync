# Changelog

本文件记录项目的显著变更。格式参考 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/)。

## [Unreleased] — 2026-10-27 全仓治理(架构梳理 · 11 路并行审查 · 回归测试锁定修复 · 模块文档 · 多角色复审)

> 本次由「通读全仓 → 10+1 路并行代码审查(99 项发现:0 P0 / 14 P1 / 47 P2 / 38 P3)→ 38 项回归测试锁定缺陷 → 修复 → 6 份模块文档与 55 条重构方案 → 安全/可靠性/测试/风格四角色两轮复审」完成。审查明细见 `docs/review-2026-10-27.md`,模块文档见 `docs/modules/`,升级注意见 `docs/migration-guide-2026-10-27.md`。

### 安全(Security)

- **改密即吊销旧 token**:`users` 表新增 `pwd_changed_at`,中间件验签后回查用户——账号被删除/禁用同样拒绝;修改密码后所有旧 token(网页会话与 CLI `--token`)立即失效。
- **JWT 收紧**:仅接受 HS256 签名、强制携带 `exp`(自签 token 均满足,存量无感)。
- **加密密文版本化**:仓库密码密文统一加 `enc:v1:` 前缀;更换/清空 `ENCRYPT_KEY` 后旧密文显式报错,不再静默把密文当明文使用;`registry` 接口不再吞解密错误。
- **Chart 防注入**:Chart.yaml 的 `name` 增加白名单正则(拒绝 `/`、`..`、空格、大写等),杜绝把 chart 推到任意仓库路径;Chart.yaml 超 1MB 由"静默截断+误导性解析错误"改为显式报错。
- **fail-fast**:`JWT_SECRET` 随机生成失败时 panic,不再静默回退到源码公开常量。

### 修复(Fixed)— 后端

- catalog 对 `http://` 前缀仓库的 Repos/Tags/Projects 三个接口全部失效(硬编码 https)。
- chart 上传:multipart 表单临时文件泄漏(大文件每传一次泄一份);upload-paths 无单文件大小上限可单请求 OOM;服务启动清扫 `irs-chart-*` 残留临时目录。
- skopeo:stdout/stderr 双流回调数据竞争(channel 单消费者串行化,`-race` 验证);单行超 1MB 后管道不再挂死(bufio.Reader 无上限排空);空目标 host、scheme/官方域名大小写等 ref 解析边界。
- 任务执行:预执行失败(probe/凭证/authfile)时任务计数与明细不一致;终态任务被重复投递会重跑(pending 乐观锁);取消竞态把已完结任务覆盖为 canceled(条件更新);retention 两步删除事务化;worker 数下限保护;DB 目录不可写回退真正生效(写探测);**后台 worker 晚于 DB 生命周期消费队列时 runTask 空指针崩溃**(GitHub Actions Linux 实测;优雅退出/测试收尾窗口,现跳过并记日志,任务保持 pending 由下次启动恢复)。
- chart 推送:Harbor 探测分支连接泄漏;OCI bearer token 过期自动重认证(不再误报"凭证无效");ChartMuseum `{"error":...}` 错误体解析;registry Probe 兼容仅返回 `access_token` 的 token 端点。
- api 杂项:settings 保存失败仍报成功、任务列表 Count 错误未查等错误处理;catalog 直调 skopeo 无超时(30s);创建任务未指定 arch 时改读 `default_arch` 设置而非硬编码。

### 修复(Fixed)— CLI(irs)

- `irs health --server URL` 无全局配置时不再误报"未配置服务端地址"。
- `irs --help` / `-h` 不再报"无法识别的全局选项"退出 2。
- 全局 flag 精确匹配:`--servername` 等前缀相似项不再被误吞;`--server=URL` 形式可用。
- `tasks create`:位置参数之后的 flag 不再被吞进镜像列表;多次 `--refs` 累加而非静默覆盖。
- `tasks logs --follow` 迟到订阅者退出码修正(以服务端最终失败数判定)。
- `tasks list` 服务端返回 size=0 不再除零 panic;`--refs-file` 兼容 UTF-8 BOM;SSE 解析兼容多行 `data:` 与无空行结尾。

### 修复(Fixed)— 前端(web)

- 任务列表状态筛选不生效(需手动刷新)。
- 任务详情 SSE 被代理/服务端"干净关闭"后页面冻结:自动重连(3s×5 次)→降级轮询,组件卸载清理重连定时器。
- 非法任务 id(非数字)不再进入 3s 循环报错,直接回列表。
- 统计卡片改用全量口径(此前按当前页统计,翻页后偏小);轮询静默化(表格不再周期性闪烁)+ 过期响应丢弃。
- 网络错误/超时映射为中文提示;删除确认弹窗取消不再产生 unhandled rejection。
- `utils/ref.js` 与后端 `ref.go` 对齐:剥 `http(s)://` scheme(大小写不敏感)、Docker Hub 官方域名归一化(`registry-1.docker.io` 等)、空目标 host 拒绝——预览与实际落库一致。

### 构建 / CI(Build & CI)

- **Dockerfile 默认改公共源,单参数 `ZONE` 切换**:默认 `global`(npmjs/golang/dl-cdn,GitHub Actions 可直接构建);内网 `--build-arg ZONE=cn` 走内网 Nexus。Makefile 所有 build 目标与 docker-compose 默认 `ZONE=cn`(compose 可 `.env` 覆盖)。**内网裸 `docker build .` 需显式加 `--build-arg ZONE=cn`**。
- 新增 `.github/workflows/ci.yml`:push/PR 门禁 = go vet + go test + 并发热点 `-race` + 前端 vitest/构建 + 单架构镜像构建与 healthz 运行冒烟。
- `.gitignore` 补 `gui-test-screenshots/`;`.dockerignore` 补 `.env`、`bin/` 等;`.env.example` 补 `DB_PATH`/`SKOPEO_BIN` 并修正并发说明;README 修正任务串行表述与 ZONE 说明。

### 测试(Tests)

- Go 测试文件 46 → 63 个,新增约 130 个用例:38 项"回归锁定"用例精确对应审查缺陷,其余为边界/契约测试;全部绿色。
- fakeskopeo 新增 6 个行为关键字:`dualstream`(双流并发,配 -race)、`longline`(2MB 超长行)、`failtags`/`badjson`/`failinspect`/`varydigest`(list-tags/inspect 失败与可变 digest)。
- testutil:修复 `NewTestDB` cleanup 后全局 `store.DB` 悬挂指向已关闭连接;新增 testutil 自身契约测试。
- 前端引入 vitest 2.1.9(与 vite 5 兼容),`web/tests/ref.test.js` 29 例与后端 ref 解析逐条对齐。
- 跨包合并语句覆盖率 90.0%(验收线 90%)。
- CI 实测修复:TestRecoverStuckPendingTaskRequeued 时序脆弱(-race 慢调度下"离开 pending"等待条件会命中 running→failed 中间态窗口,改为直接等待终态)。

### 文档(Docs)

- 新增 `docs/architecture.md`(全仓架构与文件清单)、`docs/review-2026-10-27.md`(11 路审查汇总与修复对照)、`docs/modules/`(api/cli/core/integration/web/delivery 六份模块文档,含 55 条带工时与风险的重构方案)、`docs/migration-guide-2026-10-27.md`(迁移指南)、`docs/pr-description.md`(PR 描述)。

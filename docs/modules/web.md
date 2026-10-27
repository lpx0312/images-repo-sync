# Web 前端(web/)

> 源码:`web/`。技术栈 Vue 3.5 + Vite 5 + Element Plus 2.8(zh-cn)+ Pinia 2 + vue-router 4 + axios + dayjs;构建产物 `web/dist` 经 `go:embed all:web/dist` 打进服务端二进制,前端无独立部署单元。基于 main 分支 2026-10-27 代码(阶段 4 修复已合入)。

## 1. 架构

- **路由**(`router/index.js`):8 条业务路由全部懒加载,`/login` 标 `meta.public`;全局守卫:未登录访问受保护页跳 `/login?redirect=<fullPath>`,已登录访问登录页回 `/tasks`;未匹配路径与 `/` 均重定向 `/tasks`。
- **状态**:仅一个 Pinia store(`stores/auth.js`)——token/user 按"记住我"二选一落 localStorage 或 sessionStorage(写入前互斥清理);登录/登出/改密三个动作;30 分钟无操作自动登出(60s tick,App.vue 监听全局 mousemove/keydown/click 刷新活跃时间)。业务页状态全部视图内局部 ref,无跨页 store。
- **API 封装**(`api/client.js` + `api/index.js`):单 axios 实例(baseURL `/api`,超时 30s,大文件上传放宽 600s)。请求拦截附 `Bearer`;响应拦截直接返回 `response.data` 完成信封拆包,网络层失败(无响应体/ECONNABORTED)映射中文提示;401 区分登录接口(仅提示)与其他接口(清登录态跳 `/login?expired=1&redirect=`);5xx 统一兜底文案。`index.js` 按 7 组资源聚合端点:auth / registry / catalog / task / settings / chartRepo / chartUpload。
- **SSE 处理**(TaskDetailView):`fetch` + `ReadableStream` 手动解析 `text/event-stream`(EventSource 无法带 Authorization 头)。按 `\n\n` 切帧,`event:`/`data:` 行拼接、`:` 心跳跳过;消费 snapshot / task_started / item_started / item_progress / item_success / item_failed / task_finished 七类事件;任务计数由明细重算(`recountFromItems`),SSE 历史回放天然幂等;日志上限 2000 行、可暂停自动滚动。断流自愈:反向代理"干净关闭"(done)与网络错误统一进 `scheduleStreamRetry`——任务未到终态则 3s 延时重连,连续 5 次失败降级为 3s 轮询(轮询静默 catch、终态自停);snapshot 送达即清零重连计数。非法任务 id(Number.isInteger 守卫)直接 replace 回列表,不再进入请求。

## 2. 组件清单

| 类别 | 文件 | 职责 |
|---|---|---|
| 布局 | App.vue | 顶栏导航(桌面)+ 移动抽屉 + 改密入口 + 活跃时间采集 |
| 视图 | LoginView | 登录(记住我、redirect 回跳) |
| | TaskListView | 统计卡片(全量口径,5 个 status count 并行查询)+ 列表 + 状态筛选(watch → 回第 1 页重载)+ 4s 静默轮询(loadSeq 丢弃过期响应、不置 loading) |
| | TaskDetailView | 任务详情:SSE 实时日志/明细(按目标仓库分组)+ 重连降级轮询 + 取消任务 |
| | SyncNewView | 三步向导:选镜像(粘贴列表 / CatalogBrowser 目录浏览)→ 选目标/模式/架构/project → 映射预览(mappingRows = 引用 × 目标)并提交 |
| | ChartUploadView | chart 上传(文件拖拽带进度 / 服务端路径)+ 记录列表(筛选、3s 静默轮询、重试) |
| | RegistryView / ChartRepoView | 仓库 CRUD + 测试连接 |
| | SettingsView | default_arch |
| 组件 | CatalogBrowser | Harbor project/repo 与通用 `_catalog` 浏览,tag 多选,产出带 registryHost 前缀的完整引用 |
| | ModePreview | 三种同步模式单选 + 实时示例预览(调 `resolveTarget`) |
| | ChangePasswordDialog | 改密表单(前端正则强度校验) |

## 3. 与后端的契约点

- **信封**:成功 `{data, total, page, size}`(列表)或 `{data}`(单对象),失败 `{error}` + HTTP 状态码;视图统一 `res.data ?? res` 兜底。
- **ref 解析同步**(最核心契约):`utils/ref.js` 的 `parseSource`/`resolveTarget` 逐条镜像 `internal/skopeo/ref.go`——`docker://` 与 `http(s)://` 剥离、官方域名(index.docker.com / registry-1.docker.io)归一化为 docker.io(均大小写不敏感)、library/ 补全、非 digest 引用补 `:latest`、flat/preserve_path 缺 project 及空/纯斜杠 targetHost 返回 `''`、replace_host 忽略 project。保证前端映射预览表与后端落库 target_ref 一致。`utils/constants.js` 的 ModeXxx 与 model.go 对齐。
- 其余:状态机字符串(pending/running/success/failed/canceled/skipped)、SSE 事件名与 payload 字段、`remember_me`、`default_arch`。

## 4. 测试现状

仅 `web/tests/ref.test.js`(vitest,29 用例全绿,CI frontend job 执行):表驱动逐条对齐后端 `ref_test.go`/`regression_test.go`——A 类回归(scheme 剥离、preserve_path 无双斜杠、空 host 拦截)+ B 类边界(14 组 parseSource、隐式 docker.io、端口、digest、尾斜杠等)。注意:文件内"RED(待修复)"注释已过时(实现已修复),读时勿被误导。组件、视图、store、拦截器零测试。

## 5. 已知限制

- 新建任务限额(refs≤500、目标≤10、源≠目标)仅后端校验,前端提交后才报错;500×10 行 mappingRows 无虚拟滚动。
- modeLabel/statusLabel/archLabel 等字典函数在 TaskListView / TaskDetailView / SyncNewView / ChartUploadView 四处重复实现,文案已漂移(如"① 扁平" vs "① 单一项目(扁平)")。
- ChangePasswordDialog 正则(`[^A-Za-z0-9]` 把空格当特殊字符、无 72 字节上限)与后端 `auth.IsStrongPassword`(rune 计数、空白不算特殊、≤72 字节)不一致。
- element-plus 全量引入 + 全部图标全局注册,打进单一 index chunk,首屏体积大。
- 统计卡片用 5 个并发 count 请求拼全量口径,后端无聚合接口。

## 6. 重构方案

- **P1 SyncNewView 前端限额校验 + mappingRows 大表虚拟滚动**:问题——限额仅后端拒单,大表全量渲染卡顿。方案——步骤 3 提交前校验 refs≤500/目标≤10/源≠目标并就地提示;预览表改 el-table-v2 或分页(首屏只渲染 100 行)。影响面:SyncNewView.vue。工作量 3h。风险:低。
- **P2 抽取 utils/dict.js 统一字典**:问题——4 个视图重复 label/Type 函数且文案漂移。方案——新建 `utils/dict.js` 导出 modeLabel/statusLabel/itemStatusLabel/archLabel 等单一实现,各视图改引用。影响面:4 个 view,纯展示层。工作量 1.5h。风险:低(注意 TaskListView 与 Detail 的 archLabel 文案差异需人工定稿)。
- **P2 ChangePasswordDialog 强度规则对齐后端**:问题——空格误判为特殊字符、超 72 字节后端 500。方案——校验改为"rune≥8 且四类字符(空白除外)+ 字节数≤72",文案同步。影响面:ChangePasswordDialog.vue(可加 vitest 用例)。工作量 1h。风险:低。
- **P3 bundle chunk 拆分**:问题——element-plus 与全量图标在单一 index chunk,首屏慢。方案——vite `build.rollupOptions.manualChunks` 拆 element-plus/dayjs/axios 为 vendor chunk,图标改按需引入(unplugin-icons 或局部注册)。影响面:vite.config.js、main.js;go:embed 流程不变。工作量 2h。风险:低。
- **P3 ref.js 与后端 ref_test 用例表共享**:问题——29 用例靠人工双向对齐,后端改用例前端不知情。方案——用例表抽为 JSON(go:embed 进 ref_test.go,web 测试经构建脚本拷贝或生成),或 CI 加"两份用例表 diff"步骤。影响面:internal/skopeo 测试 + web/tests + 构建链。工作量 4h。风险:中(动构建链,需同时保 go/vitest 两侧可运行)。

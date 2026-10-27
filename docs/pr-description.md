# PR 描述(可直接粘贴)

## Title

refactor: 全仓治理 — 11 路并行审查、38 项回归锁定修复、模块文档与 CI 门禁

## Summary

对 images-repo-sync 做了一轮完整治理:**通读全仓产出架构文档 → 10+1 路并行代码审查(99 项发现:0 P0 / 14 P1 / 47 P2 / 38 P3)→ 为每目录补单元/边界测试并以 38 个"回归锁定"用例精确复现缺陷 → 修复全部 14 个 P1 及大部分 P2 → 6 份模块文档(55 条带工时与风险的重构方案)→ 安全/可靠性/测试/风格四角色两轮复审 → 终审"可提交"**。

修复不是"照单全收":每项缺陷先写断言正确行为的测试(RED),再修代码转绿(GREEN),测试留在仓库里防止回归。Go 13 包 + 前端 vitest 29 例全绿;跨包合并覆盖率 90.0%(验收线 90%);`-race` 下双流竞争用例通过(CI 已加 race job)。

## 行为变更(用户可感知)

| 变更 | 说明 |
|---|---|
| 🔒 改密即吊销旧 token | 修改密码后所有旧会话(网页+CLI)失效;禁用/删除账号立即拒绝 |
| 🔒 JWT 收紧 | 仅 HS256、强制 exp(自签 token 兼容,无感) |
| 🔒 密文版本化 | 仓库密码密文加 `enc:v1:` 前缀;ENCRYPT_KEY 切换不再静默错乱 |
| 🐳 Dockerfile 默认公共源 | 内网构建需 `--build-arg ZONE=cn`(make/compose 已默认 cn);GitHub 可裸构建 |
| ✅ 任务取消条件更新 | 已完结任务不再被覆盖为 canceled;终态任务不被重复投递重跑 |
| ✅ http:// 仓库可用 | 明文仓库的浏览/Tags/项目列表修复 |
| ✅ 前端体验 | 状态筛选生效、SSE 断流自愈重连、统计全量口径、网络错误中文化 |
| ✅ CLI | `--help`、多次 `--refs`、`health --server`、退出码、BOM 等修复 |

完整清单见 `CHANGELOG.md`;升级操作见 `docs/migration-guide-2026-10-27.md`。

## 变更构成

- **后端修复**(internal/api、task、skopeo、chart、registry、auth/middleware/crypto、config/model/store):14 P1 + 约 25 项 P2,含 2 个数据竞争与 3 类资源泄漏。
- **前端修复**(web/src):2 P1(筛选失效、SSE 冻结)+ 5 项 P2;`utils/ref.js` 与后端解析逐条对齐。
- **测试**:`*_test.go` 46→63 个(新增约 130 用例);fakeskopeo 扩 6 个失败注入关键字;testutil 修全局悬挂并新增契约测试;前端引入 vitest。
- **构建/CI**:Dockerfile ZONE 机制、Makefile/compose 联动、新增 `ci.yml`(vet+test+race+前端+镜像构建与 healthz 冒烟)、ignore/env/README 修正。
- **文档**:`docs/architecture.md`、`docs/review-2026-10-27.md`(审查全记录+修复对照)、`docs/modules/` 六份模块文档与重构路线(55 条)。

## 审查与验证过程

1. **审查**:11 个并行审查任务(api/cli/skopeo/task+store/chart+registry/认证链/config+model/web/testutil/构建交付/横切面),全部发现附 file:line 与代码证据。
2. **测试先行**:38 项缺陷各有一个"回归锁定"用例(RED→修复→GREEN),另有约 90 个边界/契约用例直接 GREEN。
3. **两轮多角色复审**:R1(安全/可靠性/测试质量/风格)+ R2(风格终审/首席架构师终审)。R1 发现 2 个 P1(前端重连定时器泄漏、取消竞态未真正修复)与 5 项 P2,全部修复并经 R2 逐项核验落地。
4. **实测**:`go test ./... -count=1` 13 包全绿;`npm test` 29/29;`go test -race`(skopeo/task)通过;`docker build --build-arg ZONE=cn` 本机构建成功。

## 遗留(非本 PR 范围,已列入 docs/modules/ 重构方案)

55 条重构条目按优先级与工时排序,重点:Manager 优雅退出链路(Stop/drain+SSE 关闭信号)、runTask 拆分、push.go 拆分与 chart/registry 重复实现收敛、前端限额校验与虚拟滚动、SSRF 收敛等。

## 建议的 commit 拆分

1. `fix:` 后端+前端缺陷修复(含行为变更,引用回归用例编号)
2. `test:` 回归/边界测试 + fakeskopeo/testutil 扩展 + vitest
3. `docs:` 架构/审查/模块文档 + changelog/迁移指南
4. `build:` Dockerfile ZONE + ci.yml + Makefile/compose/ignore/env

## 迁移提示

内网构建命令变更与加密迁移语义见 `docs/migration-guide-2026-10-27.md`(最重要的一条:内网裸 `docker build` 需加 `--build-arg ZONE=cn`)。

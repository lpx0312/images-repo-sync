# 迁移指南(2026-10-27 全仓治理版本)

本文档说明本次升级带来的行为变化与操作步骤。按影响从大到小排列。

## 1. Docker 构建源默认改为公共源(⚠ 内网用户必读)

Dockerfile 现在默认使用公共源(registry.npmjs.org / proxy.golang.org / dl-cdn.alpinelinux.org),以满足"任何环境(含 GitHub Actions 托管 runner)直接 `docker build .` 即可成功"的规范。**内网环境必须显式切换 ZONE**:

| 构建入口 | 内网(默认 cn) | 公网/CI |
|---|---|---|
| 裸 docker build | `docker build --build-arg ZONE=cn -t images-repo-sync:latest .` | `docker build ... .`(默认 global) |
| Makefile | `make build` / `make build-push`(已默认 `ZONE=cn`) | `make build ZONE=global` |
| docker compose | 默认 `ZONE=cn` | `.env` 中加 `ZONE=global` 后 `docker compose up -d --build` |

- 切换只需 `ZONE` 一个参数;`NPM_REGISTRY` / `GOPROXY` / `GOSUMDB` 仍可单独传 build-arg 细粒度覆盖(对 global 模式生效)。
- 本机(内网)常用命令变更:**原先的 `docker build -t images-repo-sync:latest .` 需改为加 `--build-arg ZONE=cn`**,否则 npm/go 下载会因访问公共源失败。已实测 `ZONE=cn` 构建成品镜像与旧版一致。
- GitHub Actions 手动发布 workflow 无需任何修改(默认 global 即公共源)。

## 2. 认证行为变化

- **改密即吊销**:修改密码后,该用户此前签发的所有 token(网页会话、CLI 保存的 `--token`)立即失效,需重新登录。CLI 侧:`irs login` 重新登录即可。
- **JWT 校验收紧**:仅接受 HS256 + 强制 `exp`。本系统自签的 token 均满足,升级无感;仅当外部程序自行签发无 exp 的 token 时会开始被拒绝。
- 升级后可选的一次性安全动作:管理员改一次密码,使历史上所有可能泄漏的旧 token 全部失效。

## 3. 仓库密码加密(enc:v1)迁移语义

`ENCRYPT_KEY` 相关行为变化,按部署现状对号入座:

| 你的现状 | 升级后行为 | 需要操作 |
|---|---|---|
| 一直配置 ENCRYPT_KEY | 旧密文(无前缀)自动兼容解密,新写入密文带 `enc:v1:` 前缀 | 无 |
| 从未配置 ENCRYPT_KEY(明文存储) | 继续明文透传,与从前一致 | 无(建议尽早配置 key) |
| 明文运行一段时间后**新配** ENCRYPT_KEY | 存量明文密码解密会报错(接口显式失败,不再静默) | **重新录入各仓库密码一次** |
| 更换 ENCRYPT_KEY(旧→新) | 旧 `enc:v1:` 密文解密显式报错 | 用旧 key 导出/重录,或接受重录 |

## 4. 数据库

无需手动迁移:启动时 AutoMigrate 自动完成——
- `users` 表新增 `pwd_changed_at` 列(全部存量行零值,兼容放行);
- `login_logs.created_at` 单列索引、`sync_tasks(status, created_at)` 复合索引(首次启动建索引,库大时启动稍慢,属一次性)。

## 5. 行为变化清单(可能影响使用习惯)

- 取消一个已结束的任务,现在返回"任务已结束"且**不再改写其真实终态**(此前可能把 success 覆盖为 canceled)。
- 终态(success/failed/canceled)任务即便被重复投递也**不会重新执行**。
- `http://` 前缀的明文仓库,浏览目录/Tags/项目列表现在真正可用(此前必然失败)。
- 任务未指定架构时,取"系统设置"中的 `default_arch`(此前硬编码 amd64;默认设置值不变,行为无感的场景居多)。
- Chart 上传:单文件 >1GB 在入口即拒绝;Chart.yaml 的 name 含大写/空格/`/` 等会被拒绝(helm 同款规范)。
- 前端:任务列表状态筛选即时生效;任务详情页 SSE 断流自动重连;统计卡片为全量口径。
- CLI:`--refs` 可多次提供;`--help` 可用;错误提示更准确。

## 6. CI

新增 `.github/workflows/ci.yml`(push/PR 自动运行 vet+test+race+前端+镜像冒烟)。GitHub Actions 使用公共源,不依赖任何内网地址;若仓库 fork 到内网 Git,仅需照常 `ZONE=cn` 构建发布镜像。

## 7. 回滚

本版本无破坏性数据迁移(新增列/索引可保留)。回滚到旧版本二进制/镜像即可;唯一注意:若升级期间已用 `enc:v1:` 写入新密文,旧版本无法解密(重新录入密码即可)。

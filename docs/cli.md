# irs CLI 使用文档

`irs` 是 images-repo-sync 平台的命令行客户端：把 Web 端的全部核心能力
（登录、镜像仓库管理、同步任务、chart 仓库与上传、系统设置）封装为可脚本化
的命令，适合人工终端使用，也适合 AI / CI 自动化调用（配合 `--json` 与退出码）。

- 源码：`cmd/irs/`（入口）+ `internal/cli/`（实现，纯标准库）
- 配置文件：`~/.irs-cli.json`（保存服务端地址与登录 token，0600 权限）

## 安装

```bash
# 本地编译(生成 bin/irs 或 bin/irs.exe)
make cli
# 或直接
go build -o bin/irs ./cmd/irs
```

把 `bin/` 下的可执行文件放入 PATH 即可全局使用（Windows 示例：复制为
`C:\Users\<你>\bin\irs.exe`）。

## 登录

```bash
# 方式一:账号密码(密码也可用 IRS_PASSWORD 环境变量传入)
irs login --server http://localhost:8080 --username admin --password 'Admin@123'

# 方式二:直接使用已有 JWT token(如从浏览器会话取得)
irs login --server http://localhost:8080 --token <jwt>

# 检查登录态
irs whoami
```

登录成功后 server/token 写入 `~/.irs-cli.json`，后续命令免传。
`irs logout` 清除本地凭据。

### 凭据优先级

| 来源 | 优先级 |
|---|---|
| `--server` / `--token` 参数 | 高 |
| `IRS_SERVER` / `IRS_TOKEN` 环境变量 | 中 |
| `~/.irs-cli.json` 配置文件 | 低 |

设置了 `IRS_USER` + `IRS_PASSWORD` 环境变量时，token 过期（HTTP 401）会
自动用账号密码重登一次并持久化新 token，适合 CI 长期运行。

> 环境变量别名：`IRS_USERNAME`/`IRS_PASS` 同 `IRS_USER`/`IRS_PASSWORD`。

## 全局选项与退出码

```
irs [全局选项] <命令> [参数]

--server URL    平台地址(全局选项须放在命令之前)
--token TOKEN   Bearer token
--json          以 JSON 输出结果(也可放在命令之后)
--config PATH   配置文件路径(默认 ~/.irs-cli.json)
--version/-v    版本
```

| 退出码 | 含义 |
|---|---|
| 0 | 成功 |
| 1 | 业务失败(上传失败、同步任务有失败项、API 报错等) |
| 2 | 命令/参数错误(未知命令、缺参数) |

## 命令总览

```
irs version / irs health / irs login / irs logout / irs whoami
irs registries list|create|update|delete|test
irs catalog projects|repos|tags
irs tasks list|get|create|cancel|logs
irs chart-repos list|create|update|delete|test
irs charts upload|upload-path|uploads|retry
irs settings get|set
```

`irs` 或 `irs help` 查看全部命令与一行说明。

## 镜像仓库(registries)

```bash
irs registries list                          # 查看 ID/类型/地址/默认项目
irs registries create --name my-harbor --host harbor.example.com \
  --type harbor --username admin --password *** --insecure
irs registries update 2 --default-project mirror   # 未指定的项保留原值
irs registries test 1                        # 连通性与凭证探测(/v2/)
irs registries delete 2 --yes                # 删除需显式确认
```

`--type` 可选 `generic / harbor / dockerhub / acr / swr`；`--insecure`
跳过 TLS 证书校验（自签证书）。

## 浏览目录(catalog)

```bash
irs catalog projects 1                       # Harbor 列出全部项目
irs catalog repos 1 --project datacenter-test-chart
irs catalog repos 1 --q nginx                # 名称关键字过滤
irs catalog tags 1 --repo library/nginx      # 列出某镜像的 tag
```

注意：
- `catalog tags` 依赖平台容器内的 skopeo，对 chart OCI artifact 仓库可能失败；
  查 chart 版本请用 Harbor REST `/v2/<project>/<chart>/tags/list`（只读）。
- ACR 等禁用 `_catalog` 的仓库 `catalog repos` 会报认证失败，属预期，
  请改用同步任务的「粘贴 refs」方式。

## 同步任务(tasks)

```bash
irs tasks list --size 10 --page 2            # 任务历史(可 --status failed 过滤)
irs tasks get 31                             # 任务详情含逐镜像明细

# 创建并实时跟踪(推荐):
irs tasks create --source 2 --target 1 --mode flat \
  --project datacenter-test-chart --arch amd64 \
  --refs registry.cn-hangzhou.aliyuncs.com/lpx03/images-repo-sync:c8334db --wait

# 镜像列表三种给法:
#   --refs "a:1,b:2"        逗号分隔
#   --refs-file refs.txt    每行一条,# 注释
#   位置参数直接跟在最后
```

| 参数 | 说明 |
|---|---|
| `--source` / `--target` | 源/目标仓库 ID(`irs registries list` 查看) |
| `--mode` | `flat`(进项目扁平) / `preserve_path`(保留路径) / `replace_host`(仅换 host) |
| `--project` | 目标 project(缺省用目标仓库的默认项目) |
| `--arch` | `amd64` / `arm64` / `all`,默认 amd64 |
| `--wait` | 创建后实时跟随 SSE 日志直到结束;有失败项时退出码 1 |

```bash
irs tasks logs 31 --follow     # 跟踪运行中的任务(Ctrl+C 只中断查看,任务继续)
irs tasks cancel 31            # 取消排队中/运行中的任务
```

## Chart 仓库与上传(chart-repos / charts)

```bash
irs chart-repos list                          # 查看 ID(kubekey-chart → datacenter-test-chart)
irs chart-repos create --name my-chart --type oci --host harbor.example.com \
  --project datacenter-test-chart --username admin --password *** --insecure
irs chart-repos test 1                        # OCI ping /v2/;ChartMuseum /health
```

- `--type oci`：推送到 `oci://<host>/<project>/<chart>:<version>`，产物与
  `helm push` 一致，可用 `helm pull oci://...` 拉回；`--project` 必填。
- `--type chartmuseum`：走 `POST /api/charts`，地址可带子路径。
- `--host` 带 `http://` 前缀表示明文 HTTP，默认 HTTPS。

```bash
# 上传本地文件/目录(目录取一层 *.tgz,不递归),默认等待全部结果:
irs charts upload 1 ./mychart-0.1.0.tgz ./charts-dir/

# 上传平台服务器上的路径(目录同样扫一层 *.tgz):
irs charts upload-path 1 /charts

# 查询记录与重试:
irs charts uploads --size 20 --page 2
irs charts uploads --status failed
irs charts retry 16
```

- 非法 tgz（缺 Chart.yaml、损坏等）会被服务端校验跳过并打印 `[跳过] 原因`。
- 重复上传同一包会覆盖同名 tag（内容相同 digest 不变，幂等）。
- `--no-wait` 只提交不等待；等待模式下任一失败退出码 1。

## 系统设置(settings)

```bash
irs settings get
irs settings set --default-arch amd64     # amd64 / arm64 / all(新建任务默认架构)
```

## JSON 输出与自动化

任意命令追加 `--json`（位置随意）输出结构化 JSON，例如：

```bash
irs chart-repos list --json
irs charts uploads --status failed --json
```

脚本/AI 可靠的做法：`--json` + 检查退出码。AI 场景可配合本仓库提供的
`irs-sync` skill（ZCode 技能，位于用户技能目录），其中内置了
「写操作仅允许 Harbor `datacenter-test-chart` 项目」的安全约束。

## 已知限制与注意事项

1. **Windows + Git Bash**：服务器绝对路径参数（如 `upload-path` 的 `/charts`）
   会被 MSYS 转换为 Windows 路径，需加 `MSYS_NO_PATHCONV=1` 前缀或写 `//charts`。
2. `catalog tags` 对 chart OCI artifact 仓库可能失败（skopeo 限制），
   查 chart 版本用 Harbor REST tags/list。
3. ACR 禁用 `_catalog`，`catalog repos` 报认证失败属预期。
4. 登录接口有防暴力锁定：同一用户名连续失败 5 次锁 10 分钟，
   自动化中请确保凭据正确后再批量调用。

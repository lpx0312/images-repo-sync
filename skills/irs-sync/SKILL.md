---
name: irs-sync
description: 通过 irs 命令行操作 images-repo-sync 镜像同步平台(本地 http://localhost:8080)：同步镜像到 Harbor、上传 Helm chart 包到 Chart 仓库(OCI)。触发词包括 "同步镜像"、"镜像同步"、"上传 chart"、"chart 上传"、"helm chart"、"chart 包"、"images-repo-sync"、"irs 平台"、"Harbor 同步"。
---

# irs-sync — images-repo-sync 平台操作

通过 `irs` CLI 操作本地镜像仓库同步平台，完成**镜像同步**与 **chart 包上传**。

## ⚠️ 安全红线（必须遵守）

1. **写操作只允许落在 Harbor 项目 `datacenter-test-chart`**（仓库 `dockerhub.kubekey.local`）。
   同步任务的 `--project` 必须填 `datacenter-test-chart`；chart 上传只允许用 chart 仓库 ID 1（`kubekey-chart`）。
2. **禁止对 Harbor 其他项目做任何写操作**（`library`、`datacenter-test`、`argoproj` 等一律不许推送/覆盖/删除）。浏览/列出（`catalog` 命令）属于只读操作，允许。
3. 删除类命令（`registries delete`、`chart-repos delete`）执行前必须向用户确认。
4. 上传/同步前先用只读命令核对目标（`chart-repos list` / `registries list`），不要凭记忆猜 ID。

## 环境与登录

- 平台地址：`http://localhost:8080`（本机 Docker 容器 `irs`，服务未运行时先 `docker start irs`）
- CLI：`irs`（位于 `C:\Users\lipanx\bin\irs.exe`，已在 PATH）
- 登录态：`~/.irs-cli.json` 已保存 server + token（有效期至 2026-11-20）
- token 失效（提示 401/token 无效）时重新登录：
  `irs login --server http://localhost:8080 --username admin --password <密码>`
- 首次检查：`irs health` → `irs whoami`

## 常用命令速查

| 场景 | 命令 |
|------|------|
| 看 Chart 仓库 | `irs chart-repos list`（kubekey-chart → datacenter-test-chart，ID=1） |
| 测 Chart 仓库连通 | `irs chart-repos test 1` |
| 上传本地 chart 包 | `irs charts upload 1 <路径/*.tgz 或目录>`（目录取一层 *.tgz，自动等待结果） |
| 上传服务器上的包 | `irs charts upload-path 1 /charts`（Git Bash 需加 `MSYS_NO_PATHCONV=1` 前缀） |
| 查上传记录 | `irs charts uploads`（`--status failed` 只看失败） |
| 重试失败上传 | `irs charts retry <记录ID>` |
| 看镜像仓库 | `irs registries list`（1=afc-harbor 目标，2=acr 源，4=华为SWR…） |
| 列项目/repo | `irs catalog projects 1`、`irs catalog repos 1 --project datacenter-test-chart` |
| 创建同步任务 | `irs tasks create --source 2 --target 1 --mode flat --project datacenter-test-chart --arch amd64 --refs <镜像1>,<镜像2> --wait` |
| 看任务 | `irs tasks list`、`irs tasks get <ID>`、`irs tasks logs <ID> --follow` |
| JSON 输出 | 任意命令追加 `--json`（脚本/AI 解析推荐） |

## 工作流 A：上传 chart 包

```bash
irs chart-repos list                 # 确认 kubekey-chart 的 ID(当前为 1)
irs chart-repos test 1               # 确认连通
irs charts upload 1 D:/path/to/mychart-0.1.0.tgz   # 可多个文件或整个目录
# 输出 [成功] name:version -> oci://dockerhub.kubekey.local/datacenter-test-chart/... sha256:...
irs charts uploads --size 5          # 复核记录(状态/digest/错误)
```

验证远端（可选，只读）：Harbor REST
`docker exec irs wget -q --no-check-certificate --header "Authorization: Basic <base64(admin:密码)>" -O- https://dockerhub.kubekey.local/v2/datacenter-test-chart/<chart名>/tags/list`

## 工作流 B：同步镜像（目标只能是 datacenter-test-chart）

```bash
irs registries list                  # 确认源/目标仓库 ID
irs tasks create --source 2 --target 1 --mode flat --project datacenter-test-chart \
  --arch amd64 --refs registry.cn-hangzhou.aliyuncs.com/lpx03/xxx:tag --wait
# --mode: flat(进项目扁平) / preserve_path(保留路径) / replace_host(仅换host,不进项目)
# --arch: amd64 / arm64 / all;--wait 会实时打印每个镜像的结果,失败退出码 1
irs tasks list --size 5              # 历史任务
```

> ACR（仓库 2/3）的 `catalog repos` 会报认证失败属正常（ACR 禁 _catalog），用「粘贴 refs」方式。

## 注意事项

- `charts upload` 默认等待全部结果再退出；`--no-wait` 只提交。
- 非法 tgz 会被服务端校验跳过并打印 `[跳过] 原因`，不影响其他包。
- Git Bash 下服务器绝对路径（`/charts`）会被 MSYS 转成 Windows 路径，需 `MSYS_NO_PATHCONV=1` 前缀或用 `//charts`。
- 浏览目录（`catalog repos/tags`）仅对 Harbor / 通用 Registry 类型的源仓库有效；repo 名可直接用 `project/name` 形式，服务端会锚定到配置仓库地址。ACR 源请直接粘贴完整镜像引用。
- 退出码：成功 0；命令/参数错误 2；业务失败（上传失败/任务有失败项）1。AI 自动化应检查退出码。

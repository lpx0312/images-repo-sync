# 外部集成层(skopeo/chart/registry)

> 基于阶段 4 修复后的代码(2026-10-27)。行号以当前源码为准。

## 1. 职责与架构

- **internal/skopeo**:skopeo 二进制的子进程封装。`runWithStream`(exec.go:22)argv 参数化启动(无 shell,环境仅 PATH),stdout/stderr 双流经 channel 汇聚、单循环回调,保证 handler 串行(exec.go:45-75);`captureStdout`(exec.go:94)供需完整 JSON 的命令。上层 API:Copy/InspectDigest(copy.go)、ListTags/BinVersion(catalog.go)、ParseSource/ResolveTarget(ref.go)、authfile 临时凭证(authfile.go)。
- **internal/chart**:不依赖 helm。tgz 解析定位 Chart.yaml 并校验 name/version(meta.go);推送双实现:OCI Registry v2(push.go:339,config blob + chart layer + manifest)与 ChartMuseum(push.go:144,含 Harbor 内置 ChartMuseum 识别 detectHarbor push.go:107);另提供 Probe 探测(push.go:580)。
- **internal/registry**:纯 HTTP 镜像仓库探测(probe.go:90),Bearer/Basic 双流程与网络错误分类;client.go 提供 20s 共享 client 与 BasicAuthHeader,被 chart 与 api/catalog 复用。

依赖方向:chart → registry;skopeo 由 task/runner 与 api/catalog 调用。

## 2. skopeo 命令契约

- **argv 拼装**(copy.go:72):全局 flag `--override-arch/--override-os` 必须在 `copy` 之前(ArchAll 用 copy 之后的 `--all`,二者互斥);随后 `--preserve-digests`、`--src/dest-authfile`、`--src/dest-tls-verify=false`、`docker://源 docker://目标`。
- **authfile 生命周期**:WriteAuthFile(authfile.go:22)按 host 写 0600 临时 auth.json(显式 chmod,authfile.go:45),调用方 defer CleanupAuthFiles;runner 对同一目标只生成一次,InspectDigest 复用。
- **错误翻译**:Copy 保留末尾 6 行 ringBuffer(copy.go:51);FriendlyCopyError(copy.go:121)翻译架构缺失/manifest unknown/unauthorized 三类高频错误;IsPreserveDigestsConflict(copy.go:115)识别 digest 保留冲突,runner.go:281-287 据此去掉 `--preserve-digests` 降级重试一次(华为云 SWR 基础版拒收 OCI index 场景)。
- **ref 解析**:ParseSource(ref.go:26)大小写不敏感剥 scheme,docker.io/index.docker.com/registry-1.docker.io 归一化并补 library/(ref.go:62-68,87-91);ResolveTarget 空 host 显式报错(ref.go:129-133)。

## 3. OCI 推送流程

ociClient(push.go:206,10 分钟超时)实现与 helm push 等价的 artifact:

1. 首次请求匿名,401 由 WWW-Authenticate 分流:Bearer challenge → parseBearerChallenge(push.go:503)取 realm/service → 携 Basic 到 token 端点换 `token|access_token`(push.go:324-333)→ 后续复用 Bearer;Basic/无 challenge → 直接切 Basic。
2. blob 两步 monolithic 上传(push.go:405):POST `/v2/<repo>/blobs/uploads/` 取 Location(支持跨主机预签名 URL,仅同 host 才带凭证,putUpload push.go:463)→ PUT `?digest=`。
3. 401 统一经 do() 刷新重试(push.go:228-255),PUT 会话中途过期同样重认证(push.go:439-449),避免 token 过期误报"凭证无效"。
4. 最后 PUT manifest(tag=version),取 Docker-Content-Digest 回填。

探测侧 registry.Probe(probe.go:90):匿名 ping `/v2/` → Bearer 三步换 token / Basic 重试 → classifyNetErr 分类网络错误(probe.go:244)。

## 4. 测试现状

fakeskopeo(testutil/fakeskopeo/main.go:11-27)关键字驱动:copy 支持 failcopy/noarch/preserveconflict/conflictonce/manifestunknown/slowcopy/dualstream(双流并发,配 -race 验串行化)/longline(2MB 单行验证无上限排空);list-tags 支持 failtags/badjson;inspect 支持 failinspect/varydigest(可变 digest 测差异检测)。三包约 90 个测试函数,覆盖 argv 拼装、ref 边界、authfile 权限、OCI 全流程(httptest 假 registry:token 刷新、跨主机上传、ChartMuseum `error` 字段、Harbor 新旧路径)与取消路径。盲区:3xx 重定向、PUT 后再 401 组合、probeOCI 匿名 bearer。

## 5. 已知限制与技术债
- **加密迁移语义**:存量明文密码在未设 ENCRYPT_KEY 时透传兼容;先以明文运行后补配 key 的场景,存量裸 base64 旧密文可自动解密,但**纯明文密码**会在 GCM 解密失败时按错误处理,需重录仓库密码;更换 ENCRYPT_KEY 后旧 `enc:v1:` 密文将显式报错而非静默返回明文(详见迁移指南)。

1. push.go 679 行单文件混合 Target/ChartMuseum/OCI/probe 四类职责(评审报告 5 点名)。
2. chart 与 registry 4 组重复实现已漂移:SplitHost(push.go:42)vs baseURL(probe.go:26)、classifyNetErr(push.go:561 vs probe.go:244)、readErrBody(push.go:526 vs probe.go:49,字段口径不一:chart 版多 `error` 字段)、parseBearerChallenge(push.go:503)vs parseChallenge(probe.go:215)。
3. ParseFile 全量 ReadFile(meta.go:51),上游上传上限 1GB 而解析只需 Chart.yaml,内存峰值浪费。
4. 匿名 Bearer 不支持(push.go:287-288 报"请填写账号密码"),而 registry.Probe 匿名可换 token(probe.go:167),同一匿名可读仓库探测结论矛盾。
5. 失败上传不清理:blob 已传而 manifest 失败时残留孤儿 blob;upload session 中途失败不 DELETE。
6. 密钥派生无盐单次 SHA-256(crypto.go:19;本层仓库凭据落库加解密依赖它)。
7. StderrTail 命名不符(copy.go:30,实为 stdout+stderr 合流尾部)。

## 6. 重构方案

**P1-1 push.go 拆四文件**
问题→5.1。方案:拆 target.go(Target/SplitHost/Ref/joinURL)、chartmuseum.go(detectHarbor/pushChartMuseum/probeChartMuseum/authedGet)、oci.go(ociClient/pushOCI/pushBlob/putUpload/parseBearerChallenge)、probe.go(Probe/probeOCI);公共 API(Push/Probe/Target)不变。影响面:仅包内文件重组,调用方零改动。工作量:2-3h。风险:低,纯移动 + 全量 go test 回归。

**P1-2 两包重复实现收敛**
问题→5.2。方案:registry 新增 httputil.go,导出 SplitSchemeHost/ClassifyNetErr/ReadErrBody/ParseChallenge(合并 chart 版 `error` 字段解析与截断行为),chart 删除本地副本。影响面:chart/push.go、registry/probe.go 及两包测试断言(错误文案需对齐)。工作量:3-4h。风险:中,文案变化影响测试断言与前端展示。

**P2-1 匿名 Bearer 对齐**
问题→5.4。方案:authenticate 允许无 Basic 时匿名请求 token 端点,与 Probe 口径一致。影响面:chart probeOCI/pushOCI 匿名路径。工作量:1-2h。风险:低,补匿名 token 测试即可。

**P2-2 ParseFile 流式化**
问题→5.3。方案:os.Open + gzip/tar 流式扫描 Chart.yaml(ParseBytes 抽 io.Reader 版),推送字节由调用方单独读。影响面:chart.ParseFile 与 api/chartupload 调用点。工作量:2h。风险:低。

**P2-3 失败上传清理**
问题→5.5。方案:manifest 失败或 ctx 取消时 best-effort DELETE 已传 blob 与未完成 session(405 容错)。影响面:pushOCI 失败路径新增请求。工作量:2-3h。风险:低,部分 registry 不支持 DELETE 必须忽略。

**P3-1 StderrTail 更名**:改 OutputTail,同步注释与 runner/api 引用;0.5-1h;风险低。
**P3-2 密钥派生改 HKDF**:HKDF-SHA256 + `enc:v2:` 版本前缀,存量 `enc:v1:` 双读回退;工作量 3h;风险中,涉存量密文迁移,代码在 internal/crypto。

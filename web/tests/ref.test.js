// web/src/utils/ref.js 纯函数单元测试。
// 预期值来源:
//   - internal/skopeo/ref.go          (ParseSource / ResolveTarget / StripScheme 当前实现)
//   - internal/skopeo/ref_test.go     (后端表驱动用例,逐条对齐)
//   - internal/skopeo/regression_test.go (后端 A 类 RED 回归,断言「修复后」行为,前端保持同编号)
// 约定(与后端 regression_test.go 相同):
//   - A 类:断言修复后行为,当前实现允许失败;修复 web/src/utils/ref.js 后必须全部转绿。
//   - B 类:边界测试,当前实现必须通过。

import { describe, it, expect } from 'vitest'
import { parseSource, resolveTarget } from '../src/utils/ref'
import { MODE_FLAT, MODE_PRESERVE_PATH, MODE_REPLACE_HOST } from '../src/utils/constants'

// 后端 ref_test.go 的基准三元组。
const SRC = 'gcr.io/k8s-staging/cluster-api/clusterctl:v1'
const HOST = 'harbor.example.com'
const PROJECT = 'mirror'

// ---------------------------------------------------------------------------
// A 类回归测试(断言「修复后」的正确行为,缺陷已修复)
// ---------------------------------------------------------------------------

describe('A1 scheme 剥离与大小写 —— 与后端 ref.go / regression_test.go(A4) 对齐', () => {
  it('parseSource 应剥离小写 http(s):// 前缀,host 不含 scheme', () => {
    // 回归锁定(已修复): A1 —— 当前实现只剥 docker://,把 "http:"/"https:" 当成了 host。
    // 后端 ref.go ParseSource 会依次 TrimPrefix docker://、https://、http://。
    expect(parseSource('http://reg:5000/ns/img:v1')).toEqual(['reg:5000', 'ns/img:v1'])
    expect(parseSource('https://reg.example.com/ns/img:v1')).toEqual(['reg.example.com', 'ns/img:v1'])
  })

  it('parseSource scheme 剥离与 docker.io 归一化应对大小写不敏感', () => {
    // 回归锁定(已修复): A1 —— 对齐后端 regression_test.go A4 的修复后行为:
    //   大写 scheme 同样剥离;"Docker.IO" 归一化为 docker.io 并补 library/。
    expect(parseSource('HTTP://docker.io/x')).toEqual(['docker.io', 'library/x:latest'])
    expect(parseSource('http://Docker.IO/x')).toEqual(['docker.io', 'library/x:latest'])
    expect(parseSource('Docker.IO/x')).toEqual(['docker.io', 'library/x:latest'])
    expect(parseSource('DOCKER://quay.io/jetstack/cm:v1')).toEqual(['quay.io', 'jetstack/cm:v1'])
  })

  it('resolveTarget 应剥掉 targetHost 的 http(s):// 前缀(与后端 StripScheme 一致,大小写不敏感)', () => {
    // 回归锁定(已修复): A1 —— 后端 ref.go ResolveTarget 内调用 StripScheme(EqualFold,
    // 大小写不敏感)后才拼接;当前前端原样保留 scheme,拼出非法引用。
    expect(resolveTarget(SRC, 'http://h:5000', MODE_FLAT, 'proj')).toBe('h:5000/proj/clusterctl:v1')
    expect(
      resolveTarget(SRC, 'HTTP://h:5000', MODE_PRESERVE_PATH, 'proj')
    ).toBe('h:5000/proj/k8s-staging/cluster-api/clusterctl:v1')
    expect(
      resolveTarget(SRC, 'https://harbor.example.com/', MODE_REPLACE_HOST, '')
    ).toBe('harbor.example.com/k8s-staging/cluster-api/clusterctl:v1')
  })
})

describe('A2 preserve_path 预览结果与后端一致(不含 //,host 不带 scheme)', () => {
  it('带 http:// 的源引用在 preserve_path 下不产生双斜杠', () => {
    // 回归锁定(已修复): A2 —— 当前 path 被解析为 "//reg:5000/ns/img:v1",
    // 预览拼出 "harbor.example.com/mirror//reg:5000/ns/img:v1"。
    const got = resolveTarget('http://reg:5000/ns/img:v1', HOST, MODE_PRESERVE_PATH, PROJECT)
    expect(got).toBe('harbor.example.com/mirror/ns/img:v1')
    expect(got).not.toContain('//')
  })
})

describe('A3 空 targetHost 拦截 —— 对齐后端 regression_test.go A3', () => {
  it('空/纯空白/仅斜杠的 targetHost 应返回空串,而非以 / 开头的非法引用', () => {
    // 回归锁定(已修复): A3 —— 当前拼出 "/mirror/clusterctl:v1" 这类非法引用。
    // 后端契约:出错时返回 error 且结果为空串;前端契约为返回 ''。
    expect(resolveTarget(SRC, '', MODE_FLAT, PROJECT)).toBe('')
    expect(resolveTarget(SRC, '', MODE_PRESERVE_PATH, PROJECT)).toBe('')
    expect(resolveTarget(SRC, '', MODE_REPLACE_HOST, '')).toBe('')
    expect(resolveTarget(SRC, '   ', MODE_FLAT, PROJECT)).toBe('')
    expect(resolveTarget(SRC, '/', MODE_FLAT, PROJECT)).toBe('')
  })
})

// ---------------------------------------------------------------------------
// B 类边界测试(当前实现必须通过)
// ---------------------------------------------------------------------------

describe('B 类:parseSource(表驱动对齐后端 ref_test.go TestParseSource)', () => {
  it.each([
    // ↓ 后端 ref_test.go 原表逐条对齐
    ['nginx:1.25', 'docker.io', 'library/nginx:1.25'],
    ['nginx', 'docker.io', 'library/nginx:latest'],
    ['library/nginx', 'docker.io', 'library/nginx:latest'],
    ['bitnami/redis:7.0', 'docker.io', 'bitnami/redis:7.0'],
    ['docker.io/library/nginx:1.25', 'docker.io', 'library/nginx:1.25'],
    ['gcr.io/k8s-staging/cluster-api/clusterctl:v1', 'gcr.io', 'k8s-staging/cluster-api/clusterctl:v1'],
    ['harbor.corp.net:5000/ns/img:v1', 'harbor.corp.net:5000', 'ns/img:v1'],
    ['localhost:5000/myimg:tag', 'localhost:5000', 'myimg:tag'],
    ['docker://quay.io/jetstack/cert-manager-controller:v1.0', 'quay.io', 'jetstack/cert-manager-controller:v1.0'],
    // ↓ 前端补充边界
    ['docker.io/nginx', 'docker.io', 'library/nginx:latest'], // docker.io 官方镜像补 library
    ['reg:5000/ns/img', 'reg:5000', 'ns/img:latest'], // host 带端口 + 无 tag 补 :latest
    ['ns/img@sha256:abc', 'docker.io', 'ns/img@sha256:abc'], // digest 引用:tag 为空,不补 :latest
    ['docker.io/library/nginx@sha256:abc', 'docker.io', 'library/nginx@sha256:abc'], // digest 带 host
  ])('parseSource(%j) => [%j, %j]', (input, wantHost, wantPath) => {
    expect(parseSource(input)).toEqual([wantHost, wantPath])
  })

  it('空串与纯空格输入返回 ["", ""]', () => {
    expect(parseSource('')).toEqual(['', ''])
    expect(parseSource('   ')).toEqual(['', ''])
  })
})

describe('B 类:resolveTarget(表驱动对齐后端 ref_test.go TestResolveTarget)', () => {
  it.each([
    // 后端确认的三个例子 + project 忽略语义(逐条对齐 ref_test.go)。
    [MODE_FLAT, PROJECT, 'harbor.example.com/mirror/clusterctl:v1'],
    [MODE_PRESERVE_PATH, PROJECT, 'harbor.example.com/mirror/k8s-staging/cluster-api/clusterctl:v1'],
    [MODE_REPLACE_HOST, '', 'harbor.example.com/k8s-staging/cluster-api/clusterctl:v1'],
    [MODE_REPLACE_HOST, PROJECT, 'harbor.example.com/k8s-staging/cluster-api/clusterctl:v1'], // replace_host 忽略 project
  ])('mode=%s project=%j => %j', (mode, project, want) => {
    expect(resolveTarget(SRC, HOST, mode, project)).toBe(want)
  })

  it('隐式 docker.io 边界:flat 取最后一段(对齐后端用例)', () => {
    expect(resolveTarget('nginx:1.25', HOST, MODE_FLAT, PROJECT)).toBe('harbor.example.com/mirror/nginx:1.25')
  })

  it('隐式 docker.io 边界:replace_host 保留完整源路径(对齐后端用例)', () => {
    expect(resolveTarget('bitnami/redis:7.0', HOST, MODE_REPLACE_HOST, '')).toBe('harbor.example.com/bitnami/redis:7.0')
  })

  it('target host 带端口正常拼接', () => {
    expect(
      resolveTarget('harbor.corp.net:5000/ns/img:v1', 'harbor.example.com:8443', MODE_PRESERVE_PATH, PROJECT)
    ).toBe('harbor.example.com:8443/mirror/ns/img:v1')
    expect(
      resolveTarget('reg:5000/ns/img', 'harbor.example.com:8443', MODE_FLAT, PROJECT)
    ).toBe('harbor.example.com:8443/mirror/img:latest')
  })

  it('targetHost 尾部斜杠被剥掉', () => {
    expect(resolveTarget(SRC, 'harbor.example.com/', MODE_FLAT, PROJECT)).toBe(
      'harbor.example.com/mirror/clusterctl:v1'
    )
  })

  it('flat / preserve_path 缺 project 返回空串(后端对应为 error)', () => {
    expect(resolveTarget(SRC, HOST, MODE_FLAT, '')).toBe('')
    expect(resolveTarget(SRC, HOST, MODE_PRESERVE_PATH, '   ')).toBe('')
  })

  it('未知模式与空源引用返回空串', () => {
    expect(resolveTarget(SRC, HOST, '???', PROJECT)).toBe('')
    expect(resolveTarget('', HOST, MODE_FLAT, PROJECT)).toBe('')
    expect(resolveTarget('   ', HOST, MODE_REPLACE_HOST, '')).toBe('')
  })
})

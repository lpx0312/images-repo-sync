// 镜像引用解析工具 —— 与后端 internal/skopeo/ref.go 的 ParseSource/ResolveTarget 逻辑保持一致。
// 用于前端 ModePreview 与 SyncNewView 共享,避免三份重复实现漂移。

import { MODE_FLAT, MODE_PRESERVE_PATH, MODE_REPLACE_HOST } from './constants'

/**
 * 判断一段是否是 registry host:含 '.' 或 ':'(端口),或为 localhost。
 */
function isHost(s) {
  if (s === 'localhost') return true
  return s.includes('.') || s.includes(':')
}

/**
 * 取路径最后一段(镜像名:tag)。
 */
function tagPart(path) {
  const i = path.lastIndexOf('/')
  return i >= 0 ? path.slice(i + 1) : path
}

/**
 * 解析镜像引用,返回 [host, path]。
 * 处理隐式 docker.io 与 library 前缀,补默认 tag。
 *   "nginx:1.25" -> ["docker.io", "library/nginx:1.25"]
 *   "gcr.io/a/b/c:v1" -> ["gcr.io", "a/b/c:v1"]
 */
export function parseSource(raw) {
  raw = (raw || '').trim()
  if (!raw) return ['', '']
  raw = raw.replace(/^docker:\/\//, '')
  const slash = raw.indexOf('/')
  let host, path
  if (slash < 0) {
    host = 'docker.io'
    path = raw
  } else {
    const first = raw.slice(0, slash)
    const rest = raw.slice(slash + 1)
    if (isHost(first)) {
      host = first
      path = rest
    } else {
      host = 'docker.io'
      path = raw
    }
  }
  if ((host === 'docker.io' || host === 'index.docker.com') && !path.includes('/')) {
    path = 'library/' + path
  }
  if (path && !tagPart(path).includes(':')) path += ':latest'
  return [host, path]
}

/**
 * 按同步模式把源引用解析为目标引用。
 *   MODE_FLAT:         targetHost/project/<镜像名:tag>
 *   MODE_PRESERVE_PATH: targetHost/project/<源 host 后完整路径>
 *   MODE_REPLACE_HOST:  targetHost/<源 host 后完整路径>
 */
export function resolveTarget(srcRef, targetHost, mode, project) {
  const [, srcPath] = parseSource(srcRef)
  if (!srcPath) return ''
  targetHost = (targetHost || '').trim().replace(/\/+$/, '')
  project = (project || '').trim()
  if (mode === MODE_FLAT) {
    return project ? `${targetHost}/${project}/${tagPart(srcPath)}` : ''
  }
  if (mode === MODE_PRESERVE_PATH) {
    return project ? `${targetHost}/${project}/${srcPath}` : ''
  }
  if (mode === MODE_REPLACE_HOST) {
    return `${targetHost}/${srcPath}`
  }
  return ''
}

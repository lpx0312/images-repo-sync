<template>
  <div class="page-wrap">
    <div class="section-card">
      <div class="section-header">
        <div class="title-group">
          <el-button :icon="Back" link @click="$router.push('/tasks')">返回</el-button>
          <h3 class="section-title">任务 #{{ taskId }}</h3>
          <el-tag :type="statusType(task.status)">{{ statusLabel(task.status) }}</el-tag>
        </div>
        <div class="section-actions">
          <el-button v-if="canCancel" type="danger" plain @click="onCancel">取消任务</el-button>
          <el-button :icon="Refresh" @click="reload">刷新</el-button>
        </div>
      </div>

      <!-- 统计 -->
      <div class="stats-row">
        <div class="stat-card"><div class="stat-label">总数</div><div class="stat-value">{{ task.total || 0 }}</div></div>
        <div class="stat-card"><div class="stat-label">成功</div><div class="stat-value">{{ task.succeeded || 0 }}</div></div>
        <div class="stat-card"><div class="stat-label">失败</div><div class="stat-value">{{ task.failed || 0 }}</div></div>
        <div class="stat-card"><div class="stat-label">跳过</div><div class="stat-value">{{ task.skipped || 0 }}</div></div>
      </div>

      <el-progress
        :percentage="progress"
        :status="progressStatus"
        :stroke-width="10"
        style="margin-bottom: var(--space-lg)"
      />

      <!-- 双栏:items + 日志 -->
      <div class="dual-pane">
        <div class="pane">
          <div class="pane-title">镜像明细</div>
          <el-table :data="items" size="small" max-height="420" border>
            <el-table-column label="源镜像" min-width="220">
              <template #default="{ row }">
                <span class="cell-mono">{{ row.source_ref }}</span>
              </template>
            </el-table-column>
            <el-table-column label="目标镜像" min-width="240">
              <template #default="{ row }">
                <span class="cell-mono dst">{{ row.target_ref }}</span>
              </template>
            </el-table-column>
            <el-table-column label="状态" width="90">
              <template #default="{ row }">
                <el-tag size="small" :type="itemStatusType(row.status)">{{ itemStatusLabel(row.status) }}</el-tag>
              </template>
            </el-table-column>
            <el-table-column label="信息" min-width="160">
              <template #default="{ row }">
                <el-tooltip v-if="row.error" :content="row.error" placement="top">
                  <span class="cell-muted ellipsis">{{ row.error }}</span>
                </el-tooltip>
                <span v-else-if="row.digest" class="cell-muted ellipsis">{{ row.digest.slice(0, 19) }}…</span>
                <span v-else class="cell-muted">—</span>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <div class="pane">
          <div class="pane-title">
            实时日志
            <el-button link size="small" @click="autoScroll = !autoScroll">
              {{ autoScroll ? '暂停滚动' : '恢复滚动' }}
            </el-button>
          </div>
          <div class="log-box" ref="logBox">
            <div v-if="!logs.length" class="log-empty">等待日志输出…</div>
            <div v-for="(l, i) in logs" :key="i" class="log-line" :class="l.level">{{ l.text }}</div>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, onUnmounted, nextTick } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Back, Refresh } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { taskAPI } from '@/api'
import { useAuthStore } from '@/stores/auth'
import dayjs from 'dayjs'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const taskId = Number(route.params.id)

const task = reactive({
  id: taskId, status: 'pending', total: 0, succeeded: 0, failed: 0, skipped: 0,
  mode: '', target_project: '', started_at: null, finished_at: null,
})
const items = ref([])
const logs = ref([])
const autoScroll = ref(true)
const logBox = ref(null)

let abortCtrl = null
let pollTimer = null

const progress = computed(() => {
  if (!task.total) return 0
  return Math.round(((task.succeeded + task.failed + task.skipped) / task.total) * 100)
})
const progressStatus = computed(() => {
  if (task.status === 'success') return 'success'
  if (task.status === 'failed') return 'exception'
  return ''
})
const canCancel = computed(() => task.status === 'running' || task.status === 'pending')

async function reload() {
  const res = await taskAPI.get(taskId)
  const body = res.data ?? res
  Object.assign(task, {
    status: body.status, total: body.total, succeeded: body.succeeded, failed: body.failed, skipped: body.skipped,
    mode: body.mode, target_project: body.target_project,
    started_at: body.started_at, finished_at: body.finished_at,
  })
  items.value = body.items || []
}

async function onCancel() {
  await ElMessageBox.confirm('确定取消该任务吗？', '提示', { type: 'warning' })
  await taskAPI.cancel(taskId)
  ElMessage.success('已请求取消')
}

// SSE:用 fetch + ReadableStream 手动解析 text/event-stream(EventSource 不支持自定义 Authorization 头)。
function startStream() {
  abortCtrl = new AbortController()
  const token = authStore.token
  fetch(`/api/tasks/${taskId}/stream`, {
    headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
    signal: abortCtrl.signal,
  })
    .then(async (resp) => {
      if (!resp.ok || !resp.body) {
        // 鉴权失败或任务不存在,降级为轮询。
        startPolling()
        return
      }
      const reader = resp.body.getReader()
      const decoder = new TextDecoder('utf-8')
      let buffer = ''
      while (true) {
        const { value, done } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        // SSE 帧以空行分隔。
        let idx
        while ((idx = buffer.indexOf('\n\n')) >= 0) {
          const frame = buffer.slice(0, idx)
          buffer = buffer.slice(idx + 2)
          handleFrame(frame)
        }
      }
    })
    .catch(() => {
      // 网络中断等情况,降级轮询。
      startPolling()
    })
}

function handleFrame(frame) {
  let eventName = 'message'
  let dataStr = ''
  for (const line of frame.split('\n')) {
    if (line.startsWith(':')) continue // 注释/心跳
    if (line.startsWith('event:')) eventName = line.slice(6).trim()
    else if (line.startsWith('data:')) dataStr += line.slice(5).trim()
  }
  if (!dataStr) return
  let payload
  try {
    payload = JSON.parse(dataStr)
  } catch {
    return
  }
  applyEvent(eventName, payload)
}

function applyEvent(eventName, payload) {
  switch (eventName) {
    case 'snapshot':
      if (payload?.data) {
        const d = payload.data
        Object.assign(task, {
          status: d.status, total: d.total, succeeded: d.succeeded, failed: d.failed, skipped: d.skipped,
          mode: d.mode, target_project: d.target_project,
        })
        items.value = d.items || items.value
      }
      break
    case 'task_started':
      task.status = 'running'
      pushLog('任务开始执行', 'info')
      break
    case 'item_started':
      updateItem(payload.item_id, { status: 'running' })
      pushLog(`▶ ${payload.source_ref} → ${payload.target_ref}`)
      break
    case 'item_progress':
      if (payload.message) pushLog(`   ${payload.message}`, 'muted')
      break
    case 'item_success':
      updateItem(payload.item_id, { status: 'success', digest: payload.data?.digest || '' })
      task.succeeded = (task.succeeded || 0) + 1
      pushLog(`✓ 成功 ${payload.target_ref}`, 'success')
      break
    case 'item_failed':
      updateItem(payload.item_id, { status: payload.message === '已取消' ? 'skipped' : 'failed', error: payload.message })
      if (payload.message === '已取消') {
        task.skipped = (task.skipped || 0) + 1
      } else {
        task.failed = (task.failed || 0) + 1
        pushLog(`✗ 失败 ${payload.target_ref || payload.source_ref} — ${payload.message}`, 'error')
      }
      break
    case 'task_finished':
      if (payload.data) {
        Object.assign(task, {
          status: payload.data.status,
          succeeded: payload.data.succeeded,
          failed: payload.data.failed,
          skipped: payload.data.skipped,
        })
      }
      pushLog(`任务结束: ${statusLabel(task.status)}`, task.status === 'success' ? 'success' : 'error')
      stopStream()
      reload()
      break
  }
}

function updateItem(itemId, patch) {
  const it = items.value.find((x) => x.id === itemId)
  if (it) Object.assign(it, patch)
}

function pushLog(text, level = '') {
  logs.value.push({ text: `[${dayjs().format('HH:mm:ss')}] ${text}`, level })
  if (logs.value.length > 2000) logs.value.splice(0, logs.value.length - 2000)
  if (autoScroll.value) {
    nextTick(() => {
      if (logBox.value) logBox.value.scrollTop = logBox.value.scrollHeight
    })
  }
}

function startPolling() {
  if (pollTimer) return
  pollTimer = setInterval(async () => {
    await reload()
    if (['success', 'failed', 'canceled'].includes(task.status)) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }, 3000)
}

function stopStream() {
  if (abortCtrl) {
    abortCtrl.abort()
    abortCtrl = null
  }
}

function statusType(s) {
  return { pending: 'info', running: 'warning', success: 'success', failed: 'danger', canceled: 'info' }[s] || ''
}
function statusLabel(s) {
  return { pending: '待执行', running: '进行中', success: '成功', failed: '失败', canceled: '已取消' }[s] || s
}
function itemStatusType(s) {
  return { pending: 'info', running: 'warning', success: 'success', failed: 'danger', skipped: 'info' }[s] || ''
}
function itemStatusLabel(s) {
  return { pending: '等待', running: '同步中', success: '成功', failed: '失败', skipped: '跳过' }[s] || s
}

onMounted(async () => {
  await reload()
  startStream()
})
onUnmounted(() => {
  stopStream()
  if (pollTimer) clearInterval(pollTimer)
})
</script>

<style scoped>
.page-wrap { max-width: var(--max-width); margin: 0 auto; }
.section-card {
  background: var(--color-bg-card);
  border-radius: var(--radius-lg);
  border: 1px solid var(--color-border-light);
  box-shadow: var(--shadow-sm);
  padding: var(--space-lg);
}
.section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-md);
  flex-wrap: wrap;
  gap: var(--space-sm);
}
.title-group { display: flex; align-items: center; gap: var(--space-sm); }
.section-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.section-actions { display: flex; gap: var(--space-sm); }
.stats-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-md);
  margin-bottom: var(--space-md);
}
.stat-card {
  padding: 14px 18px;
  background: var(--color-bg-muted);
  border-radius: var(--radius-md);
  border: 1px solid var(--color-border-light);
}
.stat-label { font-size: 12px; color: var(--color-text-muted); }
.stat-value { font-size: 22px; font-weight: 700; line-height: 1; margin-top: 4px; }
.dual-pane {
  display: grid;
  grid-template-columns: 1.2fr 1fr;
  gap: var(--space-md);
}
.pane {
  background: var(--color-bg-card);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  padding: var(--space-sm);
}
.pane-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-secondary);
  margin-bottom: var(--space-sm);
  display: flex;
  align-items: center;
  justify-content: space-between;
}
.cell-mono { font-family: var(--font-mono); font-size: 11.5px; word-break: break-all; }
.cell-mono.dst { color: var(--color-primary); }
.cell-muted { color: var(--color-text-muted); font-size: 11.5px; }
.ellipsis { display: inline-block; max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.log-box {
  height: 420px;
  overflow-y: auto;
  background: #0f172a;
  border-radius: var(--radius-sm);
  padding: var(--space-sm);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.6;
}
.log-empty { color: #64748b; padding: var(--space-sm); }
.log-line { color: #e2e8f0; white-space: pre-wrap; word-break: break-all; }
.log-line.success { color: #4ade80; }
.log-line.error { color: #f87171; }
.log-line.muted { color: #94a3b8; }
.log-line.info { color: #60a5fa; }
@media (max-width: 992px) {
  .dual-pane { grid-template-columns: 1fr; }
  .stats-row { grid-template-columns: repeat(2, 1fr); }
}
</style>

<template>
  <div class="page-wrap">
    <!-- 统计卡片 -->
    <div class="stats-row">
      <div class="stat-card">
        <div>
          <div class="stat-label">总任务</div>
          <div class="stat-value">{{ stats.total }}</div>
        </div>
        <div class="stat-icon-wrap"><el-icon><Files /></el-icon></div>
      </div>
      <div class="stat-card">
        <div>
          <div class="stat-label">进行中</div>
          <div class="stat-value">{{ stats.running }}</div>
        </div>
        <div class="stat-icon-wrap warning"><el-icon><Loading /></el-icon></div>
      </div>
      <div class="stat-card">
        <div>
          <div class="stat-label">成功</div>
          <div class="stat-value">{{ stats.success }}</div>
        </div>
        <div class="stat-icon-wrap success"><el-icon><CircleCheck /></el-icon></div>
      </div>
      <div class="stat-card">
        <div>
          <div class="stat-label">失败 / 取消</div>
          <div class="stat-value">{{ stats.failed }}</div>
        </div>
        <div class="stat-icon-wrap danger"><el-icon><CircleClose /></el-icon></div>
      </div>
    </div>

    <div class="section-card">
      <div class="section-header">
        <h3 class="section-title">同步任务</h3>
        <div class="section-actions">
          <el-select v-model="filterStatus" placeholder="全部状态" clearable style="width: 140px">
            <el-option label="进行中" value="running" />
            <el-option label="待执行" value="pending" />
            <el-option label="成功" value="success" />
            <el-option label="失败" value="failed" />
            <el-option label="已取消" value="canceled" />
          </el-select>
          <el-button :icon="Refresh" @click="load">刷新</el-button>
          <el-button type="primary" :icon="Plus" @click="$router.push('/sync/new')">新建同步</el-button>
        </div>
      </div>

      <el-table :data="list" v-loading="loading" stripe>
        <el-table-column label="ID" prop="id" width="70" />
        <el-table-column label="模式" width="140">
          <template #default="{ row }">
            <el-tag size="small">{{ modeLabel(row.mode) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="架构" width="110">
          <template #default="{ row }">
            <el-tag size="small" type="info">{{ archLabel(row.arch) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="状态" width="110">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType(row.status)">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="进度" width="170">
          <template #default="{ row }">
            <el-progress
              :percentage="progressOf(row)"
              :status="progressStatus(row.status)"
              :stroke-width="8"
            />
            <div class="progress-text">{{ row.succeeded }}/{{ row.total }} 成功 · {{ row.failed }} 失败</div>
          </template>
        </el-table-column>
        <el-table-column label="目标 project" min-width="140">
          <template #default="{ row }">
            <span class="cell-muted">{{ row.target_project || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="创建时间" width="170">
          <template #default="{ row }">{{ formatTime(row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="100" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" @click="$router.push(`/tasks/${row.id}`)">详情</el-button>
          </template>
        </el-table-column>
      </el-table>

      <div class="pager">
        <el-pagination
          v-model:current-page="page"
          v-model:page-size="size"
          :total="total"
          :page-sizes="[10, 20, 50]"
          layout="total, sizes, prev, pager, next, jumper"
          background
          @current-change="load"
          @size-change="load"
        />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Files, Loading, CircleCheck, CircleClose, Refresh, Plus } from '@element-plus/icons-vue'
import { taskAPI } from '@/api'
import dayjs from 'dayjs'

const loading = ref(false)
const list = ref([])
const total = ref(0)
const page = ref(1)
const size = ref(20)
const filterStatus = ref('')

const stats = computed(() => {
  const s = { total: total.value, running: 0, success: 0, failed: 0 }
  for (const t of list.value) {
    if (t.status === 'running' || t.status === 'pending') s.running++
    else if (t.status === 'success') s.success++
    else if (t.status === 'failed' || t.status === 'canceled') s.failed++
  }
  return s
})

async function load() {
  loading.value = true
  try {
    const res = await taskAPI.list({ page: page.value, size: size.value, status: filterStatus.value || undefined })
    // 拦截器返回后端响应体 { data: [...], total, page, size };业务数据在 data 字段。
    list.value = res.data || []
    total.value = res.total || 0
  } finally {
    loading.value = false
  }
}

let timer = null
onMounted(() => {
  load()
  // 有进行中任务时自动刷新。
  timer = setInterval(() => {
    if (list.value.some((t) => t.status === 'running' || t.status === 'pending')) {
      load()
    }
  }, 4000)
})
onUnmounted(() => clearInterval(timer))

function progressOf(row) {
  if (!row.total) return 0
  return Math.round(((row.succeeded + row.failed + row.skipped) / row.total) * 100)
}
function progressStatus(s) {
  if (s === 'success') return 'success'
  if (s === 'failed') return 'exception'
  return ''
}
function modeLabel(m) {
  return { flat: '① 扁平', preserve_path: '② 保持路径', replace_host: '③ 换地址' }[m] || m
}
function archLabel(a) {
  return { amd64: 'amd64', arm64: 'arm64', all: '所有架构' }[a] || a || 'amd64'
}
function statusType(s) {
  return { pending: 'info', running: 'warning', success: 'success', failed: 'danger', canceled: 'info' }[s] || ''
}
function statusLabel(s) {
  return { pending: '待执行', running: '进行中', success: '成功', failed: '失败', canceled: '已取消' }[s] || s
}
function formatTime(t) {
  return t ? dayjs(t).format('YYYY-MM-DD HH:mm:ss') : '—'
}
</script>

<style scoped>
.page-wrap { max-width: var(--max-width); margin: 0 auto; }
.stats-row {
  display: grid;
  grid-template-columns: repeat(4, 1fr);
  gap: var(--space-md);
  margin-bottom: var(--space-lg);
}
.stat-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 20px var(--space-lg);
  background: var(--color-bg-card);
  border-radius: var(--radius-lg);
  border: 1px solid var(--color-border-light);
  box-shadow: var(--shadow-sm);
  transition: all var(--transition-base);
}
.stat-card:hover { box-shadow: var(--shadow-md); transform: translateY(-2px); }
.stat-label { font-size: var(--stat-label-size); color: var(--color-text-muted); }
.stat-value { font-size: var(--stat-number-size); font-weight: 700; line-height: 1; margin-top: 4px; }
.stat-icon-wrap {
  width: 44px;
  height: 44px;
  border-radius: var(--radius-md);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 22px;
  background: var(--color-primary-bg);
  color: var(--color-primary);
}
.stat-icon-wrap.success { background: var(--color-success-bg); color: var(--color-success); }
.stat-icon-wrap.warning { background: var(--color-warning-bg); color: var(--color-warning); }
.stat-icon-wrap.danger { background: var(--color-danger-bg); color: var(--color-danger); }
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
.section-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.section-actions { display: flex; gap: var(--space-sm); align-items: center; }
.progress-text { font-size: 11px; color: var(--color-text-muted); margin-top: 2px; }
.cell-muted { color: var(--color-text-muted); font-size: 13px; }
.pager { display: flex; justify-content: center; margin-top: var(--space-md); }
@media (max-width: 768px) {
  .stats-row { grid-template-columns: repeat(2, 1fr); }
}
</style>

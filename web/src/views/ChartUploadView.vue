<template>
  <div class="page-wrap">
    <!-- 上传表单 -->
    <div class="section-card">
      <div class="section-header">
        <h3 class="section-title">上传 Chart 包</h3>
        <el-button :icon="Refresh" @click="loadRepos">刷新仓库</el-button>
      </div>

      <el-form label-width="100px">
        <el-form-item label="目标仓库">
          <el-select
            v-model="repoId"
            placeholder="选择要上传到的 Chart 仓库"
            style="max-width: 520px"
            :loading="reposLoading"
          >
            <el-option
              v-for="r in repos"
              :key="r.id"
              :value="r.id"
              :label="`${r.name}(${repoAddr(r)})`"
            >
              <div class="repo-option">
                <el-tag size="small" :type="r.type === 'oci' ? '' : 'success'" class="repo-option-tag">
                  {{ typeLabel(r.type) }}
                </el-tag>
                <span>{{ r.name }}</span>
                <span class="repo-option-addr">{{ repoAddr(r) }}</span>
              </div>
            </el-option>
          </el-select>
          <div v-if="repos.length === 0 && !reposLoading" class="form-tip" style="margin-top: 4px">
            还没有 Chart 仓库配置,
            <router-link to="/chart-repos">去新增一个</router-link>
          </div>
        </el-form-item>

        <el-form-item label="包来源">
          <el-radio-group v-model="sourceMode">
            <el-radio-button value="file">本地文件</el-radio-button>
            <el-radio-button value="path">服务器路径</el-radio-button>
          </el-radio-group>
        </el-form-item>

        <el-form-item v-if="sourceMode === 'file'" label="选择文件">
          <el-upload
            class="chart-uploader"
            drag
            multiple
            action="#"
            accept=".tgz"
            :auto-upload="false"
            :on-change="onFileChange"
            :on-remove="onFileRemove"
            :show-file-list="true"
            :file-list="fileList"
          >
            <el-icon class="el-icon--upload"><UploadFilled /></el-icon>
            <div class="el-upload__text">拖拽 .tgz 到此处,或<em>点击选择</em>(可多选)</div>
            <template #tip>
              <div class="el-upload__tip">仅支持 helm 打包的 .tgz 文件,上传时自动解析其中的 Chart.yaml</div>
            </template>
          </el-upload>
        </el-form-item>

        <el-form-item v-else label="tgz 路径">
          <el-input
            v-model="pathsText"
            type="textarea"
            :rows="4"
            style="max-width: 520px"
            placeholder="每行一个,可以是文件或目录,如:&#10;/charts/nginx-test-0.1.0.tgz&#10;/charts/packages(扫描目录下所有 .tgz)"
          />
          <div class="form-tip" style="margin-top: 4px">
            路径是服务器(容器)上的文件系统路径;目录只扫描第一层,不递归
          </div>
        </el-form-item>

        <el-form-item>
          <el-button
            type="primary"
            :icon="Upload"
            :loading="submitting"
            :disabled="!canSubmit"
            @click="onSubmit"
          >
            {{ submitting ? `上传中 ${uploadPercent}%` : '开始上传' }}
          </el-button>
        </el-form-item>
      </el-form>
    </div>

    <!-- 上传记录 -->
    <div class="section-card records-card">
      <div class="section-header">
        <h3 class="section-title">上传记录</h3>
        <div class="section-actions">
          <el-select v-model="filterStatus" placeholder="全部状态" clearable style="width: 130px" @change="onFilterChange">
            <el-option label="待执行" value="pending" />
            <el-option label="进行中" value="running" />
            <el-option label="成功" value="success" />
            <el-option label="失败" value="failed" />
          </el-select>
          <el-button :icon="Refresh" @click="loadRecords">刷新</el-button>
        </div>
      </div>

      <el-table :data="records" v-loading="recordsLoading" stripe>
        <el-table-column label="ID" prop="id" width="70" />
        <el-table-column label="Chart" min-width="170">
          <template #default="{ row }">
            <div class="chart-cell">
              <span class="chart-name">{{ row.chart_name || '—' }}</span>
              <el-tag v-if="row.chart_version" size="small" type="info" class="chart-ver">
                {{ row.chart_version }}
              </el-tag>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="文件" min-width="170">
          <template #default="{ row }">
            <span class="cell-mono">{{ row.file_name }}</span>
          </template>
        </el-table-column>
        <el-table-column label="目标仓库" min-width="200">
          <template #default="{ row }">
            <div class="chart-cell">
              <el-tag size="small" :type="row.repo_type === 'oci' ? '' : 'success'" class="repo-option-tag">
                {{ typeLabel(row.repo_type) }}
              </el-tag>
              <span class="cell-mono" :title="row.target_ref">{{ row.repo_name }}</span>
            </div>
          </template>
        </el-table-column>
        <el-table-column label="大小" width="90">
          <template #default="{ row }">{{ formatSize(row.size) }}</template>
        </el-table-column>
        <el-table-column label="状态" width="100">
          <template #default="{ row }">
            <el-tag size="small" :type="statusType(row.status)">{{ statusLabel(row.status) }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="说明" min-width="180">
          <template #default="{ row }">
            <el-tooltip v-if="row.error" :content="row.error" placement="top" :show-after="300">
              <span class="cell-error">{{ row.error }}</span>
            </el-tooltip>
            <el-tooltip v-else-if="row.digest" :content="row.digest" placement="top" :show-after="300">
              <span class="cell-mono">{{ shortDigest(row.digest) }}</span>
            </el-tooltip>
            <span v-else class="cell-muted">—</span>
          </template>
        </el-table-column>
        <el-table-column label="时间" width="165">
          <template #default="{ row }">{{ formatTime(row.finished_at || row.created_at) }}</template>
        </el-table-column>
        <el-table-column label="操作" width="90" fixed="right">
          <template #default="{ row }">
            <el-button v-if="row.status === 'failed'" link type="primary" @click="onRetry(row)">
              重试
            </el-button>
            <span v-else class="cell-muted">—</span>
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
          @current-change="loadRecords"
          @size-change="loadRecords"
        />
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { Refresh, Upload, UploadFilled } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { chartRepoAPI, chartUploadAPI } from '@/api'
import dayjs from 'dayjs'

// ---------- 目标仓库 ----------
const repos = ref([])
const reposLoading = ref(false)
const repoId = ref(null)

async function loadRepos() {
  reposLoading.value = true
  try {
    const res = await chartRepoAPI.list()
    repos.value = res.data ?? res ?? []
  } finally {
    reposLoading.value = false
  }
}

const repoAddr = (r) => (r.project ? `${r.host}/${r.project}` : r.host)
const typeLabel = (t) => ({ oci: 'OCI', chartmuseum: 'ChartMuseum' }[t] || t || 'OCI')

// ---------- 上传表单 ----------
const sourceMode = ref('file')
const fileList = ref([])
const pathsText = ref('')
const submitting = ref(false)
const uploadPercent = ref(0)

const canSubmit = computed(() => {
  if (!repoId.value) return false
  if (sourceMode.value === 'file') return fileList.value.length > 0
  return pathsText.value.trim().length > 0
})

function onFileChange(file, files) {
  fileList.value = files
}
function onFileRemove(file, files) {
  fileList.value = files
}

async function onSubmit() {
  if (!canSubmit.value) return
  submitting.value = true
  uploadPercent.value = 0
  try {
    let res
    if (sourceMode.value === 'file') {
      const fd = new FormData()
      fd.append('repo_id', repoId.value)
      for (const f of fileList.value) fd.append('files', f.raw)
      res = await chartUploadAPI.uploadFiles(fd, (e) => {
        if (e.total) uploadPercent.value = Math.round((e.loaded / e.total) * 100)
      })
      fileList.value = []
    } else {
      const paths = pathsText.value
        .split('\n')
        .map((s) => s.trim())
        .filter(Boolean)
      res = await chartUploadAPI.uploadPaths({ repo_id: repoId.value, paths })
      pathsText.value = ''
    }
    const body = res.data ?? res
    const created = body?.created?.length ?? 0
    const invalid = body?.invalid ?? []
    if (created > 0) {
      ElMessage.success(`已提交 ${created} 个 chart 包,正在后台上传`)
    } else {
      ElMessage.warning('没有可上传的 chart 包')
    }
    if (invalid.length > 0) {
      ElMessage.warning(`跳过 ${invalid.length} 个非法文件:${invalid.map((i) => i.name).join('、')}`)
    }
    page.value = 1
    await loadRecords()
  } finally {
    submitting.value = false
  }
}

// ---------- 上传记录 ----------
const records = ref([])
const recordsLoading = ref(false)
const total = ref(0)
const page = ref(1)
const size = ref(20)
const filterStatus = ref('')

async function loadRecords() {
  recordsLoading.value = true
  try {
    const res = await chartUploadAPI.list({
      page: page.value,
      size: size.value,
      status: filterStatus.value || undefined,
    })
    records.value = res.data || []
    total.value = res.total || 0
  } finally {
    recordsLoading.value = false
  }
}

function onFilterChange() {
  page.value = 1
  loadRecords()
}

async function onRetry(row) {
  await chartUploadAPI.retry(row.id)
  ElMessage.success('已重新入队')
  await loadRecords()
}

// 有进行中的记录时自动刷新。
let timer = null
onMounted(() => {
  loadRepos()
  loadRecords()
  timer = setInterval(() => {
    if (records.value.some((r) => r.status === 'running' || r.status === 'pending')) {
      loadRecords()
    }
  }, 3000)
})
onUnmounted(() => clearInterval(timer))

// ---------- 展示辅助 ----------
function statusType(s) {
  return { pending: 'info', running: 'warning', success: 'success', failed: 'danger' }[s] || ''
}
function statusLabel(s) {
  return { pending: '待执行', running: '进行中', success: '成功', failed: '失败' }[s] || s
}
function formatTime(t) {
  return t ? dayjs(t).format('YYYY-MM-DD HH:mm:ss') : '—'
}
function formatSize(n) {
  if (!n) return '—'
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  return `${(n / 1024 / 1024).toFixed(1)} MB`
}
function shortDigest(d) {
  return d.length > 19 ? d.slice(0, 19) + '…' : d
}
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
.records-card { margin-top: var(--space-lg); }
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
.repo-option { display: flex; align-items: center; gap: 8px; }
.repo-option-tag { flex-shrink: 0; }
.repo-option-addr { color: var(--color-text-muted); font-size: 12px; font-family: var(--font-mono); }
.chart-cell { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.chart-name { font-weight: 500; }
.chart-ver { flex-shrink: 0; }
.chart-uploader { max-width: 520px; width: 100%; }
.chart-uploader :deep(.el-upload-dragger) { padding: 24px 16px; }
.cell-mono { font-family: var(--font-mono); font-size: 12.5px; word-break: break-all; }
.cell-muted { color: var(--color-text-muted); font-size: 13px; }
.cell-error {
  color: var(--color-danger);
  font-size: 12.5px;
  display: inline-block;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  vertical-align: bottom;
}
.form-tip { font-size: 12px; color: var(--color-text-muted); line-height: 1.5; }
.pager { display: flex; justify-content: center; margin-top: var(--space-md); }
</style>

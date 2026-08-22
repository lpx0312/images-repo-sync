<template>
  <div class="page-wrap">
    <div class="section-card">
      <div class="section-header">
        <h3 class="section-title">新建同步任务</h3>
        <el-button :icon="Back" @click="$router.push('/tasks')">返回</el-button>
      </div>

      <el-steps :active="step" align-center class="steps">
        <el-step title="选择源镜像" />
        <el-step title="选择目标与模式" />
        <el-step title="确认提交" />
      </el-steps>

      <!-- Step 1: 选源 -->
      <div v-show="step === 0" class="step-body">
        <el-form label-width="120px">
          <el-form-item label="源仓库" required>
            <el-select
              v-model="form.sourceRegistryId"
              placeholder="选择源仓库"
              filterable
              style="width: 360px"
              @change="onSourceChange"
            >
              <el-option
                v-for="r in sourceRegistries"
                :key="r.id"
                :label="`${r.name} (${r.host})`"
                :value="r.id"
              />
            </el-select>
          </el-form-item>
        </el-form>

        <el-tabs v-model="pickTab" class="pick-tabs">
          <el-tab-pane label="粘贴镜像列表" name="list">
            <el-input
              v-model="rawList"
              type="textarea"
              :rows="10"
              placeholder="每行一个镜像引用，例如：&#10;nginx:1.25&#10;bitnami/redis:7.0&#10;gcr.io/k8s-staging/cluster-api/clusterctl:v1.0.0"
              class="mono-area"
            />
            <div class="form-tip">支持隐式 docker.io；官方镜像会自动补 library/ 前缀。每行一条，空行忽略。</div>
          </el-tab-pane>

          <el-tab-pane label="浏览目录" name="catalog" :disabled="!canBrowse">
            <CatalogBrowser
              v-if="form.sourceRegistryId && canBrowse"
              :registry-id="form.sourceRegistryId"
              :registry-host="sourceRegistry?.host || ''"
              v-model:selected="pickedRefs"
            />
            <el-empty
              v-else-if="form.sourceRegistryId"
              description="该仓库类型不支持浏览目录（仅 Harbor / 通用 Registry 支持），请使用「粘贴镜像列表」"
            />
            <el-empty v-else description="请先选择源仓库" />
          </el-tab-pane>
        </el-tabs>

        <div class="step-footer">
          <el-button @click="$router.push('/tasks')">取消</el-button>
          <el-button type="primary" :disabled="!refsForNext.length" @click="step = 1">下一步</el-button>
        </div>
      </div>

      <!-- Step 2: 选目标 + 模式 -->
      <div v-show="step === 1" class="step-body">
        <el-form label-width="120px">
          <el-form-item label="目标仓库" required>
            <el-select
              v-model="form.targetRegistryId"
              placeholder="选择目标仓库"
              filterable
              style="width: 360px"
              @change="onTargetChange"
            >
              <el-option
                v-for="r in targetRegistries"
                :key="r.id"
                :label="`${r.name} (${r.host})`"
                :value="r.id"
              />
            </el-select>
          </el-form-item>

          <el-form-item label="目标 project">
            <!-- Harbor: 拉取已有 project 列表做下拉,可搜索 + 允许手输新 project -->
            <el-select
              v-if="targetRegistryType === 'harbor'"
              v-model="form.targetProject"
              placeholder="选择或输入 project"
              filterable
              allow-create
              default-first-option
              :loading="loadingProjects"
              :disabled="form.mode === 'replace_host'"
              style="width: 360px"
            >
              <el-option
                v-for="p in projectOptions"
                :key="p"
                :label="p"
                :value="p"
              />
            </el-select>
            <!-- 非 Harbor(ACR/generic 等): 无法可靠列 namespace,纯手输 -->
            <el-input
              v-else
              v-model="form.targetProject"
              :placeholder="defaultProjectHint"
              style="width: 360px"
              :disabled="form.mode === 'replace_host'"
            />
            <div class="form-tip">
              <template v-if="form.mode === 'replace_host'">模式 ③ 不需要目标 project</template>
              <template v-else>
                模式 ①② 需要；
                <span v-if="targetRegistryType === 'harbor'">可从下拉选或手输新的 project</span>
                <span v-else>手输(ACR 即 namespace);留空时用默认「{{ targetRegistry?.default_project || '未设置' }}」</span>
              </template>
            </div>
          </el-form-item>

          <el-form-item label="目标架构" required>
            <el-radio-group v-model="form.arch">
              <el-radio-button value="amd64">仅 AMD64</el-radio-button>
              <el-radio-button value="arm64">仅 ARM64</el-radio-button>
              <el-radio-button value="all">所有架构</el-radio-button>
            </el-radio-group>
            <div class="form-tip">
              仅同步所选架构的镜像。「所有架构」会完整复制多架构 manifest list;单架构模式下目标仓库得到的是单架构镜像。
            </div>
          </el-form-item>

          <el-form-item label="同步模式" required>
            <ModePreview
              v-model="form.mode"
              :example="exampleRef"
              :target-host="targetRegistry?.host || ''"
              :project="effectiveProject"
            />
          </el-form-item>
        </el-form>

        <div class="step-footer">
          <el-button @click="step = 0">上一步</el-button>
          <el-button type="primary" :disabled="!canStep2Next" @click="step = 2">下一步</el-button>
        </div>
      </div>

      <!-- Step 3: 预览 -->
      <div v-show="step === 2" class="step-body">
        <el-descriptions :column="2" border size="small" class="summary">
          <el-descriptions-item label="源仓库">{{ sourceRegistry?.name }}</el-descriptions-item>
          <el-descriptions-item label="目标仓库">{{ targetRegistry?.name }}</el-descriptions-item>
          <el-descriptions-item label="模式">{{ modeLabel(form.mode) }}</el-descriptions-item>
          <el-descriptions-item label="目标架构">{{ archLabel(form.arch) }}</el-descriptions-item>
          <el-descriptions-item label="目标 project">{{ form.mode === 'replace_host' ? '—' : (effectiveProject || '未设置') }}</el-descriptions-item>
          <el-descriptions-item label="镜像数量">{{ refsForNext.length }}</el-descriptions-item>
        </el-descriptions>

        <div class="mapping-card">
          <div class="mapping-title">同步映射（源 → 目标）</div>
          <el-table :data="mappingRows" size="small" max-height="360" border>
            <el-table-column type="index" label="#" width="50" />
            <el-table-column label="源镜像" min-width="280">
              <template #default="{ row }">
                <span class="cell-mono">{{ row.src }}</span>
              </template>
            </el-table-column>
            <el-table-column label="目标镜像" min-width="280">
              <template #default="{ row }">
                <span class="cell-mono dst">{{ row.dst }}</span>
              </template>
            </el-table-column>
          </el-table>
        </div>

        <div class="step-footer">
          <el-button @click="step = 1">上一步</el-button>
          <el-button type="primary" :loading="submitting" @click="onSubmit">提交同步</el-button>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Back } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'
import { registryAPI, taskAPI, catalogAPI, settingsAPI } from '@/api'
import { resolveTarget } from '@/utils/ref'
import { MODE_REPLACE_HOST } from '@/utils/constants'
import ModePreview from '@/components/ModePreview.vue'
import CatalogBrowser from '@/components/CatalogBrowser.vue'

const router = useRouter()

const step = ref(0)
const pickTab = ref('list')

// 仓库不再分角色(源/目标在每次同步时选择),源和目标共用同一个列表。
const allRegistries = ref([])
const sourceRegistries = allRegistries
const targetRegistries = allRegistries

const form = reactive({
  sourceRegistryId: null,
  targetRegistryId: null,
  mode: 'flat',
  targetProject: '',
  arch: 'amd64',
})

const rawList = ref('')
const pickedRefs = ref([])

// 目标 project 候选(Harbor 类型才拉取)。
const projectOptions = ref([])
const loadingProjects = ref(false)

const sourceRegistry = computed(() => allRegistries.value.find((r) => r.id === form.sourceRegistryId))
const targetRegistry = computed(() => allRegistries.value.find((r) => r.id === form.targetRegistryId))
const targetRegistryType = computed(() => (targetRegistry.value?.type || '').toLowerCase())

// 浏览目录仅支持 Harbor 与通用 Registry:
// Harbor 走 v2 API 列 project/repo,通用 Registry 走 _catalog;
// ACR/SWR/DockerHub 无法可靠列出仓库目录,只能粘贴列表。
const sourceRegistryType = computed(() => (sourceRegistry.value?.type || '').toLowerCase())
const canBrowse = computed(() => ['harbor', 'generic'].includes(sourceRegistryType.value))

// 源仓库切换到不支持浏览的类型时,若当前停在浏览页则回到粘贴列表。
watch(canBrowse, (ok) => {
  if (!ok && pickTab.value === 'catalog') pickTab.value = 'list'
})

// 粘贴列表解析后的引用。
const listRefs = computed(() =>
  rawList.value
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean)
)

// 合并粘贴 + 浏览两个来源的引用(去重)。
const refsForNext = computed(() => {
  const set = new Set()
  const all = pickTab.value === 'list' ? listRefs.value : pickedRefs.value
  for (const r of all) set.add(r)
  return [...set]
})

const exampleRef = computed(() => refsForNext.value[0] || 'gcr.io/k8s-staging/cluster-api/clusterctl:v1')

const defaultProjectHint = computed(() =>
  targetRegistry.value?.default_project ? `默认 ${targetRegistry.value.default_project}` : '请输入'
)
const effectiveProject = computed(() => form.targetProject || targetRegistry.value?.default_project || '')

const canStep2Next = computed(() => {
  if (!form.targetRegistryId) return false
  if (form.mode !== MODE_REPLACE_HOST && !effectiveProject.value) return false
  return true
})

// 计算映射预览表(前端复刻后端解析逻辑)。
const mappingRows = computed(() => {
  const rows = []
  for (const src of refsForNext.value) {
    rows.push({ src, dst: resolveTarget(src, targetRegistry.value?.host || '', form.mode, effectiveProject.value) })
  }
  return rows
})

function modeLabel(m) {
  return { flat: '① 单一项目(扁平)', preserve_path: '② 保持源项目路径', replace_host: '③ 仅替换仓库地址' }[m]
}

function archLabel(a) {
  return { amd64: '仅 AMD64', arm64: '仅 ARM64', all: '所有架构' }[a] || a
}

function onSourceChange() {
  pickedRefs.value = []
}

// 选目标仓库:重置 project 选项,Harbor 类型拉取 project 列表,默认填入 registry.default_project。
function onTargetChange() {
  form.targetProject = ''
  projectOptions.value = []
  loadProjectOptions()
}

async function loadProjectOptions() {
  const reg = targetRegistry.value
  if (!reg) return
  // 默认值:无论哪种类型都先用 registry 的 default_project 兜底。
  form.targetProject = reg.default_project || ''
  if (targetRegistryType.value !== 'harbor') {
    return // 非 Harbor 不拉列表,走纯手输。
  }
  loadingProjects.value = true
  try {
    const res = await catalogAPI.projects(reg.id)
    const body = res.data ?? res
    projectOptions.value = body.ok ? (body.projects || []) : []
    // 若默认 project 不在列表里,前置加上,保证可见。
    if (form.targetProject && !projectOptions.value.includes(form.targetProject)) {
      projectOptions.value = [form.targetProject, ...projectOptions.value]
    }
  } finally {
    loadingProjects.value = false
  }
}

async function onSubmit() {
  submitting.value = true
  try {
    const res = await taskAPI.create({
      source_registry_id: form.sourceRegistryId,
      target_registry_id: form.targetRegistryId,
      mode: form.mode,
      target_project: form.targetProject,
      arch: form.arch,
      refs: refsForNext.value,
    })
    const body = res.data ?? res
    ElMessage.success('任务已创建')
    router.push(`/tasks/${body.id}`)
  } finally {
    submitting.value = false
  }
}
const submitting = ref(false)

onMounted(async () => {
  // 仓库不分角色,源/目标共用同一列表。
  const res = await registryAPI.list()
  allRegistries.value = res.data ?? res
  // 读取系统设置的默认架构,作为架构单选的初值。
  try {
    const sr = await settingsAPI.get()
    const sd = sr.data ?? sr
    form.arch = sd.default_arch || 'amd64'
  } catch {
    /* 读取失败用代码默认值 amd64 */
  }
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
  margin-bottom: var(--space-lg);
}
.section-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.steps { margin-bottom: var(--space-xl); }
.step-body { min-height: 320px; }
.pick-tabs { margin-top: var(--space-md); }
.mono-area :deep(textarea) { font-family: var(--font-mono); font-size: 13px; }
.form-tip { font-size: 12px; color: var(--color-text-muted); margin-top: 4px; }
.step-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-sm);
  margin-top: var(--space-xl);
}
.summary { margin-bottom: var(--space-md); }
.mapping-card { margin-top: var(--space-md); }
.mapping-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--color-text-primary);
  margin-bottom: var(--space-sm);
}
.cell-mono { font-family: var(--font-mono); font-size: 12.5px; word-break: break-all; }
.dst { color: var(--color-primary); font-weight: 600; }
</style>

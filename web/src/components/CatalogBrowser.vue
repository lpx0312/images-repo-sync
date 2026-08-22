<template>
  <div class="catalog-browser">
    <div class="browser-toolbar">
      <el-input
        v-model="repoQuery"
        placeholder="搜索 / 加载仓库（Harbor 项目可填 project 名）"
        :prefix-icon="Search"
        clearable
        style="width: 320px"
        @keyup.enter="loadRepos"
      />
      <el-button :icon="Refresh" :loading="loadingRepos" @click="loadRepos">加载仓库</el-button>
      <span v-if="repoMsg" class="msg-text" :class="{ err: !repoOk }">{{ repoMsg }}</span>
    </div>

    <el-table
      v-if="repos.length"
      :data="repos"
      v-loading="loadingRepos"
      size="small"
      max-height="180"
      border
      highlight-current-row
      @current-change="onPickRepo"
    >
      <el-table-column label="仓库" min-width="280">
        <template #default="{ row }">
          <!-- 接口返回的 repos 是字符串数组(host 后完整路径),row 本身就是仓库名 -->
          <span class="cell-mono">{{ row }}</span>
        </template>
      </el-table-column>
    </el-table>

    <div v-if="currentRepo" class="tag-area">
      <div class="tag-header">
        <span class="tag-title">{{ currentRepo }} 的 tag</span>
        <div>
          <el-button size="small" :disabled="!tagsSelected.length" @click="addSelected">添加选中({{ tagsSelected.length }})</el-button>
          <el-button size="small" @click="addAll">添加全部</el-button>
        </div>
      </div>
      <el-table
        :data="tags"
        v-loading="loadingTags"
        size="small"
        max-height="200"
        border
        @selection-change="onTagSelChange"
      >
        <el-table-column type="selection" width="44" />
        <el-table-column label="Tag" min-width="200">
          <template #default="{ row }">
            <span class="cell-mono">{{ row }}</span>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <div v-if="selected.length" class="selected-area">
      <div class="selected-title">
        已选镜像({{ selected.length }})
        <el-button link type="danger" @click="$emit('update:selected', [])">清空</el-button>
      </div>
      <div class="selected-chips">
        <el-tag
          v-for="(r, i) in selected"
          :key="r"
          closable
          size="small"
          class="chip"
          @close="removeAt(i)"
        >
          <span class="cell-mono">{{ r }}</span>
        </el-tag>
      </div>
    </div>
  </div>
</template>

<script setup>
import { ref, watch } from 'vue'
import { Search, Refresh } from '@element-plus/icons-vue'
import { catalogAPI } from '@/api'

const props = defineProps({
  registryId: { type: Number, required: true },
  // 仓库地址(harbor.example.com[:port]);拼进选出的完整镜像引用,
  // 保证任务执行时 skopeo 从正确的源仓库拉取,而不是被解析成 docker.io。
  registryHost: { type: String, default: '' },
  selected: { type: Array, default: () => [] },
})
const emit = defineEmits(['update:selected'])

const repoQuery = ref('')
const repos = ref([])
const loadingRepos = ref(false)
const repoMsg = ref('')
const repoOk = ref(true)

const currentRepo = ref('')
const tags = ref([])
const tagsSelected = ref([])
const loadingTags = ref(false)

async function loadRepos() {
  loadingRepos.value = true
  repoMsg.value = ''
  try {
    const res = await catalogAPI.repos(props.registryId, {
      project: repoQuery.value.includes('/') ? '' : repoQuery.value,
      q: repoQuery.value.includes('/') ? repoQuery.value : '',
    })
    const body = res.data ?? res
    repoOk.value = body.ok !== false
    repos.value = body.repos || []
    repoMsg.value = body.ok === false ? body.message : ''
    currentRepo.value = ''
    tags.value = []
  } finally {
    loadingRepos.value = false
  }
}

function onPickRepo(row) {
  if (!row) return
  currentRepo.value = row // row 即仓库名字符串
  loadTags(row)
}

async function loadTags(repo) {
  loadingTags.value = true
  tags.value = []
  try {
    const res = await catalogAPI.tags(props.registryId, { repo })
    const body = res.data ?? res
    if (body.ok === false) {
      repoMsg.value = body.message
      return
    }
    tags.value = body.tags || []
  } finally {
    loadingTags.value = false
  }
}

function onTagSelChange(sel) {
  tagsSelected.value = sel
}

function refOf(tag) {
  // 目录里的 repo 名不含仓库地址(如 project/repo),拼上配置的 host
  // 生成完整引用 host/project/repo:tag,供同步任务直接使用。
  const repo = props.registryHost
    ? `${props.registryHost}/${currentRepo.value}`
    : currentRepo.value
  return `${repo}:${tag}`
}

function addSelected() {
  const cur = new Set(props.selected)
  for (const t of tagsSelected.value) cur.add(refOf(t))
  emit('update:selected', [...cur])
}

function addAll() {
  const cur = new Set(props.selected)
  for (const t of tags.value) cur.add(refOf(t))
  emit('update:selected', [...cur])
}

function removeAt(i) {
  const next = [...props.selected]
  next.splice(i, 1)
  emit('update:selected', next)
}

// 切换源仓库时重置。
watch(() => props.registryId, () => {
  repos.value = []
  tags.value = []
  currentRepo.value = ''
  repoMsg.value = ''
  emit('update:selected', [])
})
</script>

<style scoped>
.catalog-browser { display: flex; flex-direction: column; gap: var(--space-md); }
.browser-toolbar { display: flex; align-items: center; gap: var(--space-sm); flex-wrap: wrap; }
.msg-text { font-size: 12px; color: var(--color-text-muted); }
.msg-text.err { color: var(--color-danger); }
.tag-area { border-top: 1px dashed var(--color-border); padding-top: var(--space-md); }
.tag-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: var(--space-sm);
}
.tag-title { font-size: 13px; font-weight: 600; color: var(--color-text-primary); }
.selected-area {
  border-top: 1px dashed var(--color-border);
  padding-top: var(--space-md);
}
.selected-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-primary);
  margin-bottom: var(--space-sm);
  display: flex;
  align-items: center;
  gap: var(--space-sm);
}
.selected-chips { display: flex; flex-wrap: wrap; gap: 6px; max-height: 120px; overflow-y: auto; }
.chip { max-width: 100%; }
.cell-mono { font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
</style>

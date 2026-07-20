<template>
  <div class="page-wrap">
    <div class="section-card">
      <div class="section-header">
        <h3 class="section-title">仓库管理</h3>
        <div class="section-actions">
          <el-input
            v-model="keyword"
            placeholder="搜索名称 / 地址"
            :prefix-icon="Search"
            clearable
            style="width: 240px"
          />
          <el-button type="primary" :icon="Plus" @click="openCreate">新增仓库</el-button>
        </div>
      </div>

      <el-table :data="filteredList" stripe v-loading="loading">
        <el-table-column label="名称" prop="name" min-width="140" />
        <el-table-column label="地址" min-width="220">
          <template #default="{ row }">
            <span class="cell-mono">{{ row.host }}</span>
            <el-tag v-if="row.insecure" size="small" type="warning" style="margin-left: 8px">insecure</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="类型" prop="type" width="110">
          <template #default="{ row }">
            <el-tag size="small">{{ row.type || 'generic' }}</el-tag>
          </template>
        </el-table-column>
        <el-table-column label="默认 project" prop="default_project" width="140">
          <template #default="{ row }">
            <span class="cell-muted">{{ row.default_project || '—' }}</span>
          </template>
        </el-table-column>
        <el-table-column label="用户名" prop="username" width="140">
          <template #default="{ row }">
            <span class="cell-muted">{{ row.username || (row.has_password ? '—' : '匿名') }}</span>
          </template>
        </el-table-column>
        <el-table-column label="操作" width="220" fixed="right">
          <template #default="{ row }">
            <el-button link type="primary" :loading="testingId === row.id" @click="onTest(row)">测试</el-button>
            <el-button link type="primary" @click="openEdit(row)">编辑</el-button>
            <el-button link type="danger" @click="onRemove(row)">删除</el-button>
          </template>
        </el-table-column>
      </el-table>
    </div>

    <!-- 新增 / 编辑抽屉 -->
    <el-drawer
      v-model="drawerVisible"
      :title="editing ? '编辑仓库' : '新增仓库'"
      size="460px"
      :close-on-click-modal="false"
    >
      <el-form ref="formRef" :model="form" :rules="rules" label-width="100px">
        <el-form-item label="名称" prop="name">
          <el-input v-model="form.name" placeholder="如 my-harbor" />
        </el-form-item>
        <el-form-item label="仓库地址" prop="host">
          <el-input v-model="form.host" placeholder="harbor.example.com[:5000]" />
        </el-form-item>
        <el-form-item label="类型" prop="type">
          <el-select v-model="form.type" style="width: 100%">
            <el-option label="Harbor" value="harbor" />
            <el-option label="Docker Hub" value="dockerhub" />
            <el-option label="阿里云 ACR" value="acr" />
            <el-option label="通用 Registry" value="generic" />
          </el-select>
        </el-form-item>
        <el-form-item label="用户名" prop="username">
          <el-input v-model="form.username" placeholder="可选" />
        </el-form-item>
        <el-form-item label="密码" prop="password">
          <el-input
            v-model="form.password"
            type="password"
            show-password
            :placeholder="editing ? '留空表示不修改' : '可选'"
          />
        </el-form-item>
        <el-form-item label="默认 project">
          <el-input v-model="form.default_project" placeholder="目标模式 flat/preserve_path 的默认 project" />
        </el-form-item>
        <el-form-item label="跳过 TLS">
          <el-switch v-model="form.insecure" />
          <span class="form-tip" style="margin-left: 12px">自签证书时开启</span>
        </el-form-item>
      </el-form>
      <template #footer>
        <el-button @click="drawerVisible = false">取消</el-button>
        <el-button type="primary" :loading="saving" @click="onSave">保存</el-button>
      </template>
    </el-drawer>
  </div>
</template>

<script setup>
import { ref, reactive, computed, onMounted } from 'vue'
import { Search, Plus } from '@element-plus/icons-vue'
import { ElMessage, ElMessageBox } from 'element-plus'
import { registryAPI } from '@/api'

const loading = ref(false)
const keyword = ref('')
const list = ref([])

const filteredList = computed(() => {
  const kw = keyword.value.trim().toLowerCase()
  if (!kw) return list.value
  return list.value.filter(
    (r) => r.name.toLowerCase().includes(kw) || (r.host || '').toLowerCase().includes(kw)
  )
})

async function load() {
  loading.value = true
  try {
    const res = await registryAPI.list()
    list.value = res.data ?? res ?? []
  } finally {
    loading.value = false
  }
}

const drawerVisible = ref(false)
const editing = ref(null)
const saving = ref(false)
const testingId = ref(null)
const formRef = ref()

const form = reactive({
  name: '',
  host: '',
  type: 'generic',
  username: '',
  password: '',
  default_project: '',
  insecure: false,
})

const rules = {
  name: [{ required: true, message: '请输入名称', trigger: 'blur' }],
  host: [{ required: true, message: '请输入仓库地址', trigger: 'blur' }],
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '', host: '', type: 'generic',
    username: '', password: '', default_project: '', insecure: false,
  })
  drawerVisible.value = true
}

function openEdit(row) {
  editing.value = row
  Object.assign(form, {
    name: row.name,
    host: row.host,
    type: row.type || 'generic',
    username: row.username || '',
    password: '',
    default_project: row.default_project || '',
    insecure: !!row.insecure,
  })
  drawerVisible.value = true
}

async function onSave() {
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return
  saving.value = true
  try {
    if (editing.value) {
      await registryAPI.update(editing.value.id, { ...form })
    } else {
      await registryAPI.create({ ...form })
    }
    ElMessage.success('已保存')
    drawerVisible.value = false
    await load()
  } finally {
    saving.value = false
  }
}

async function onRemove(row) {
  await ElMessageBox.confirm(`确定删除仓库「${row.name}」吗?`, '提示', {
    type: 'warning',
  })
  await registryAPI.remove(row.id)
  ElMessage.success('已删除')
  await load()
}

async function onTest(row) {
  testingId.value = row.id
  try {
    const res = await registryAPI.test(row.id)
    const body = res.data ?? res
    if (body.ok) {
      ElMessage.success(body.message || '连接成功')
    } else {
      ElMessage.error(body.message || '连接失败')
    }
  } finally {
    testingId.value = null
  }
}

onMounted(load)
</script>

<style scoped>
.page-wrap {
  max-width: var(--max-width);
  margin: 0 auto;
}
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
.cell-mono { font-family: var(--font-mono); font-size: 12.5px; word-break: break-all; }
.cell-muted { color: var(--color-text-muted); font-size: 13px; }
.form-tip { font-size: 12px; color: var(--color-text-muted); }
</style>

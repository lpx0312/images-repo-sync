<template>
  <div class="page-wrap">
    <div class="section-card">
      <div class="section-header">
        <h3 class="section-title">系统设置</h3>
      </div>

      <el-form label-width="120px" v-loading="loading">
        <el-form-item label="默认同步架构">
          <el-radio-group v-model="form.default_arch">
            <el-radio value="amd64">仅 AMD64</el-radio>
            <el-radio value="arm64">仅 ARM64</el-radio>
            <el-radio value="all">所有架构</el-radio>
          </el-radio-group>
          <div class="form-tip">
            新建同步任务时默认选中的目标架构。可随时修改,不影响历史任务。
          </div>
        </el-form-item>

        <el-form-item>
          <el-button type="primary" :loading="saving" @click="onSave">保存设置</el-button>
          <el-button @click="load">重置</el-button>
        </el-form-item>
      </el-form>
    </div>
  </div>
</template>

<script setup>
import { ref, reactive, onMounted } from 'vue'
import { ElMessage } from 'element-plus'
import { settingsAPI } from '@/api'

const loading = ref(false)
const saving = ref(false)

const form = reactive({
  default_arch: 'amd64',
})

async function load() {
  loading.value = true
  try {
    const res = await settingsAPI.get()
    const data = res.data ?? res
    form.default_arch = data.default_arch || 'amd64'
  } finally {
    loading.value = false
  }
}

async function onSave() {
  saving.value = true
  try {
    await settingsAPI.update({ default_arch: form.default_arch })
    ElMessage.success('设置已保存')
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<style scoped>
.page-wrap { max-width: var(--max-width); margin: 0 auto; }
.section-card {
  background: var(--color-bg-card);
  border-radius: var(--radius-lg);
  border: 1px solid var(--color-border-light);
  box-shadow: var(--shadow-sm);
  padding: var(--space-lg);
  max-width: 720px;
}
.section-header { margin-bottom: var(--space-lg); }
.section-title { margin: 0; font-size: 16px; font-weight: 600; color: var(--color-text-primary); }
.form-tip { font-size: 12px; color: var(--color-text-muted); margin-top: 4px; }
</style>

<template>
  <div class="mode-preview">
    <div class="preview-title">同步模式预览</div>
    <div v-if="!example" class="preview-empty">在左侧选择镜像后将显示转换结果</div>

    <div
      v-for="m in modes"
      :key="m.value"
      class="mode-row"
      :class="{ active: modelValue === m.value, disabled: m.disabled }"
      @click="!m.disabled && $emit('update:modelValue', m.value)"
    >
      <el-radio :model-value="modelValue" :value="m.value" :disabled="m.disabled" @click.prevent>
        <span class="mode-name">{{ m.label }}</span>
      </el-radio>
      <div class="mode-desc">{{ m.desc }}</div>
      <div v-if="example" class="mode-mapping">
        <span class="src cell-mono">{{ example }}</span>
        <el-icon class="arrow"><Right /></el-icon>
        <span class="dst cell-mono" :class="{ faded: m.disabled }">
          {{ compute(m.value) || '—' }}
        </span>
      </div>
    </div>
  </div>
</template>

<script setup>
import { Right } from '@element-plus/icons-vue'
import { resolveTarget } from '@/utils/ref'
import { MODE_FLAT, MODE_PRESERVE_PATH, MODE_REPLACE_HOST } from '@/utils/constants'

const props = defineProps({
  modelValue: { type: String, default: MODE_FLAT },
  example: { type: String, default: '' }, // 当前选中的代表性源镜像
  targetHost: { type: String, default: '' },
  project: { type: String, default: '' },
})
defineEmits(['update:modelValue'])

const modes = [
  {
    value: MODE_FLAT,
    label: '① 单一项目(扁平)',
    desc: '进入目标 project,只保留镜像名 + tag,丢弃中间路径',
    disabled: false,
  },
  {
    value: MODE_PRESERVE_PATH,
    label: '② 保持源项目路径',
    desc: '进入目标 project,且源 host 后完整路径原样保留',
    disabled: false,
  },
  {
    value: MODE_REPLACE_HOST,
    label: '③ 仅替换仓库地址',
    desc: '不加 project 前缀,只把源 host 换成目标 host',
  },
]

function compute(mode) {
  return resolveTarget(props.example, props.targetHost, mode, props.project)
}
</script>

<style scoped>
.mode-preview {
  background: var(--color-bg-muted);
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  padding: var(--space-md);
}
.preview-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--color-text-secondary);
  margin-bottom: var(--space-sm);
}
.preview-empty {
  font-size: 13px;
  color: var(--color-text-muted);
  padding: var(--space-sm) 0;
}
.mode-row {
  border: 1px solid var(--color-border-light);
  border-radius: var(--radius-md);
  padding: 12px 14px;
  margin-bottom: var(--space-sm);
  cursor: pointer;
  transition: all var(--transition-fast);
  background: var(--color-bg-card);
}
.mode-row:last-child { margin-bottom: 0; }
.mode-row:hover { border-color: var(--color-primary-lighter); }
.mode-row.active {
  border-color: var(--color-primary);
  box-shadow: 0 0 0 3px rgba(30, 64, 175, 0.08);
}
.mode-row.disabled { opacity: 0.5; cursor: not-allowed; }
.mode-name { font-weight: 600; color: var(--color-text-primary); }
.mode-desc { font-size: 12px; color: var(--color-text-muted); margin: 4px 0 8px 22px; }
.mode-mapping {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  flex-wrap: wrap;
  margin-left: 22px;
}
.cell-mono { font-family: var(--font-mono); font-size: 12px; word-break: break-all; }
.src { color: var(--color-text-secondary); }
.dst {
  color: var(--color-primary);
  font-weight: 600;
}
.dst.faded { color: var(--color-text-muted); }
.arrow { color: var(--color-text-muted); flex-shrink: 0; }
</style>

<template>
  <div class="login-page">
    <div class="login-bg">
      <div class="bg-shape bg-shape-1"></div>
      <div class="bg-shape bg-shape-2"></div>
      <div class="bg-shape bg-shape-3"></div>
    </div>

    <div class="login-card">
      <div class="login-header">
        <div class="login-logo">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="28" height="28">
            <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
            <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
            <line x1="12" y1="22.08" x2="12" y2="12"/>
          </svg>
        </div>
        <h2 class="login-title">镜像仓库同步平台</h2>
        <p class="login-subtitle">登录以继续使用</p>
      </div>

      <el-form
        ref="formRef"
        :model="form"
        :rules="rules"
        class="login-form"
        @keyup.enter="handleLogin"
      >
        <el-form-item prop="username">
          <el-input
            v-model="form.username"
            placeholder="用户名"
            :prefix-icon="User"
            size="large"
            autofocus
          />
        </el-form-item>

        <el-form-item prop="password">
          <el-input
            v-model="form.password"
            type="password"
            placeholder="密码"
            :prefix-icon="Lock"
            size="large"
            show-password
          />
        </el-form-item>

        <div class="login-options">
          <el-checkbox v-model="form.rememberMe">记住登录状态</el-checkbox>
        </div>

        <el-form-item>
          <el-button
            type="primary"
            size="large"
            class="login-btn"
            :loading="loading"
            @click="handleLogin"
          >
            {{ loading ? '登录中...' : '登 录' }}
          </el-button>
        </el-form-item>
      </el-form>

      <div v-if="showExpiredTip" class="expired-tip">
        <el-icon><Warning /></el-icon>
        登录已过期，请重新登录
      </div>
    </div>

    <div class="login-footer">
      &copy; {{ new Date().getFullYear() }} 镜像仓库同步平台
    </div>
  </div>
</template>

<script setup>
import { ref, onMounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { User, Lock, Warning } from '@element-plus/icons-vue'
import { ElMessage } from 'element-plus'

const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()

const formRef = ref()
const loading = ref(false)
const showExpiredTip = ref(false)

const form = ref({
  username: '',
  password: '',
  rememberMe: false,
})

const rules = {
  username: [{ required: true, message: '请输入用户名', trigger: 'blur' }],
  password: [
    { required: true, message: '请输入密码', trigger: 'blur' },
    { min: 6, message: '密码至少 6 位', trigger: 'blur' },
  ],
}

onMounted(() => {
  if (route.query.expired === '1') {
    showExpiredTip.value = true
  }
  if (authStore.isLoggedIn) {
    router.replace(route.query.redirect || '/tasks')
  }
})

async function handleLogin() {
  if (!formRef.value) return
  const valid = await formRef.value.validate().catch(() => false)
  if (!valid) return

  loading.value = true
  try {
    await authStore.login(form.value.username, form.value.password, form.value.rememberMe)
    ElMessage.success('登录成功')
    const redirect = route.query.redirect || '/tasks'
    router.replace(redirect)
  } catch {
    // 错误已由 API 拦截器统一提示。
  } finally {
    loading.value = false
  }
}
</script>

<style scoped>
.login-page {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  align-items: center;
  justify-content: center;
  background: var(--color-bg-page);
  position: relative;
  overflow: hidden;
  padding: var(--space-lg);
}
.login-bg { position: absolute; inset: 0; pointer-events: none; }
.bg-shape { position: absolute; border-radius: 50%; opacity: 0.5; filter: blur(80px); }
.bg-shape-1 { width: 500px; height: 500px; background: var(--color-primary-lighter); top: -150px; right: -100px; opacity: 0.25; }
.bg-shape-2 { width: 400px; height: 400px; background: var(--color-primary-bg); bottom: -100px; left: -80px; opacity: 0.4; }
.bg-shape-3 { width: 250px; height: 250px; background: var(--color-success-light); top: 40%; left: 60%; opacity: 0.12; }
.login-card {
  position: relative;
  width: 100%;
  max-width: 400px;
  background: var(--color-bg-card);
  border-radius: var(--radius-xl);
  padding: 40px 36px 32px;
  box-shadow: var(--shadow-xl);
  border: 1px solid var(--color-border-light);
}
.login-header { text-align: center; margin-bottom: var(--space-xl); }
.login-logo {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 56px;
  height: 56px;
  border-radius: var(--radius-lg);
  background: linear-gradient(135deg, var(--color-primary), var(--color-primary-light));
  color: var(--color-text-inverse);
  margin-bottom: var(--space-md);
}
.login-title { margin: 0 0 6px; font-size: 22px; font-weight: 700; color: var(--color-text-primary); letter-spacing: -0.01em; }
.login-subtitle { margin: 0; font-size: 14px; color: var(--color-text-muted); }
.login-form { margin-top: var(--space-sm); }
.login-form :deep(.el-input__wrapper) {
  border-radius: var(--radius-md);
  box-shadow: 0 0 0 1px var(--color-border) inset;
}
.login-form :deep(.el-input__wrapper:hover) {
  box-shadow: 0 0 0 1px var(--color-primary-lighter) inset;
}
.login-form :deep(.el-input__wrapper.is-focus) {
  box-shadow: 0 0 0 1px var(--color-primary) inset, 0 0 0 3px rgba(30, 64, 175, 0.1);
}
.login-options { display: flex; justify-content: space-between; align-items: center; margin-bottom: 20px; }
.login-btn {
  width: 100%;
  border-radius: var(--radius-md) !important;
  font-size: 15px;
  height: 44px;
  font-weight: 600;
  letter-spacing: 0.02em;
}
.expired-tip {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  margin-top: var(--space-md);
  padding: 10px var(--space-md);
  border-radius: var(--radius-md);
  background: var(--color-danger-bg);
  color: var(--color-danger);
  font-size: 13px;
}
.login-footer { position: relative; margin-top: var(--space-xl); font-size: 13px; color: var(--color-text-muted); }
</style>

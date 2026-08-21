<template>
  <div id="app">
    <router-view v-if="!showLayout" />

    <el-container v-else class="layout-container">
      <header class="app-header">
        <div class="header-inner">
          <div class="logo" @click="router.push('/tasks')">
            <div class="logo-icon">
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" width="22" height="22">
                <path d="M21 16V8a2 2 0 0 0-1-1.73l-7-4a2 2 0 0 0-2 0l-7 4A2 2 0 0 0 3 8v8a2 2 0 0 0 1 1.73l7 4a2 2 0 0 0 2 0l7-4A2 2 0 0 0 21 16z"/>
                <polyline points="3.27 6.96 12 12.01 20.73 6.96"/>
                <line x1="12" y1="22.08" x2="12" y2="12"/>
              </svg>
            </div>
            <span class="logo-text">镜像仓库同步</span>
          </div>

          <nav class="nav-links desktop-only">
            <router-link
              v-for="item in navItems"
              :key="item.path"
              :to="item.path"
              class="nav-item"
              :class="{ active: isActiveRoute(item.path) }"
            >
              <component :is="item.icon" class="nav-icon" />
              <span>{{ item.label }}</span>
            </router-link>
          </nav>

          <div class="header-actions">
            <el-button class="desktop-only new-btn" type="primary" :icon="Plus" @click="router.push('/sync/new')">
              新建同步
            </el-button>

            <el-dropdown trigger="click" @command="handleUserCommand" class="desktop-only">
              <button class="user-btn">
                <div class="user-avatar">
                  {{ (authStore.username || 'U').charAt(0).toUpperCase() }}
                </div>
                <span class="user-name">{{ authStore.username }}</span>
                <el-icon class="dropdown-arrow"><ArrowDown /></el-icon>
              </button>
              <template #dropdown>
                <el-dropdown-menu>
                  <el-dropdown-item command="password">
                    <el-icon><Key /></el-icon>修改密码
                  </el-dropdown-item>
                  <el-dropdown-item divided command="logout">
                    <el-icon><SwitchButton /></el-icon>退出登录
                  </el-dropdown-item>
                </el-dropdown-menu>
              </template>
            </el-dropdown>

            <button class="hamburger-btn mobile-only" @click="mobileMenuOpen = true">
              <el-icon :size="22"><Menu /></el-icon>
            </button>
          </div>
        </div>
      </header>

      <el-main class="main-content">
        <transition name="page" mode="out-in">
          <router-view />
        </transition>
      </el-main>

      <footer class="app-footer">
        <span>&copy; {{ new Date().getFullYear() }} 镜像仓库同步平台</span>
      </footer>
    </el-container>

    <el-drawer
      v-model="mobileMenuOpen"
      direction="rtl"
      size="260px"
      :show-close="false"
      class="mobile-drawer"
    >
      <template #header>
        <div class="drawer-header">
          <div class="user-avatar drawer-avatar">
            {{ (authStore.username || 'U').charAt(0).toUpperCase() }}
          </div>
          <span class="drawer-username">{{ authStore.username }}</span>
        </div>
      </template>
      <div class="drawer-nav">
        <router-link
          v-for="item in navItems"
          :key="item.path"
          :to="item.path"
          class="drawer-nav-item"
          :class="{ active: isActiveRoute(item.path) }"
          @click="mobileMenuOpen = false"
        >
          <component :is="item.icon" class="nav-icon" />
          <span>{{ item.label }}</span>
        </router-link>
        <div class="drawer-nav-item" @click="goNew(); mobileMenuOpen = false">
          <el-icon class="nav-icon"><Plus /></el-icon>
          <span>新建同步</span>
        </div>
      </div>
      <div class="drawer-footer">
        <el-button text @click="showChangePassword = true; mobileMenuOpen = false">
          <el-icon><Key /></el-icon>修改密码
        </el-button>
        <el-button text type="danger" @click="handleUserCommand('logout'); mobileMenuOpen = false">
          <el-icon><SwitchButton /></el-icon>退出登录
        </el-button>
      </div>
    </el-drawer>

    <ChangePasswordDialog v-model:visible="showChangePassword" />
  </div>
</template>

<script setup>
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useRouter, useRoute } from 'vue-router'
import { useAuthStore } from '@/stores/auth'
import { ArrowDown, Key, SwitchButton, Box, Files, Setting, Plus, Menu, Collection, UploadFilled } from '@element-plus/icons-vue'
import { ElMessageBox } from 'element-plus'
import ChangePasswordDialog from '@/components/ChangePasswordDialog.vue'

const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()

const showChangePassword = ref(false)
const mobileMenuOpen = ref(false)
const showLayout = computed(() => route.name !== 'Login' && authStore.isLoggedIn)

const navItems = [
  { path: '/tasks', label: '同步任务', icon: Box },
  { path: '/chart-upload', label: 'Chart 上传', icon: UploadFilled },
  { path: '/registries', label: '仓库管理', icon: Files },
  { path: '/chart-repos', label: 'Chart 仓库', icon: Collection },
  { path: '/settings', label: '系统设置', icon: Setting },
]

function goNew() {
  router.push('/sync/new')
}

const isActiveRoute = (path) => route.path === path || route.path.startsWith(path + '/')

const handleUserCommand = (command) => {
  if (command === 'password') {
    showChangePassword.value = true
  } else if (command === 'logout') {
    ElMessageBox.confirm('确定要退出登录吗？', '提示', {
      confirmButtonText: '确定',
      cancelButtonText: '取消',
      type: 'warning',
    }).then(async () => {
      await authStore.logout()
      router.push('/login')
    }).catch(() => {})
  }
}

function onActivity() {
  authStore.updateActivityTime()
}

onMounted(() => {
  window.addEventListener('mousemove', onActivity)
  window.addEventListener('keydown', onActivity)
  window.addEventListener('click', onActivity)
})
onUnmounted(() => {
  window.removeEventListener('mousemove', onActivity)
  window.removeEventListener('keydown', onActivity)
  window.removeEventListener('click', onActivity)
})
</script>

<style scoped>
.layout-container {
  min-height: 100vh;
  display: flex;
  flex-direction: column;
}
.app-header {
  position: sticky;
  top: 0;
  z-index: 100;
  background: var(--color-bg-header);
  border-bottom: 1px solid var(--color-border);
  backdrop-filter: blur(12px);
}
.header-inner {
  display: flex;
  align-items: center;
  height: 56px;
  max-width: var(--max-width);
  margin: 0 auto;
  padding: 0 var(--space-lg);
  gap: var(--space-xl);
}
.logo {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  cursor: pointer;
  flex-shrink: 0;
  transition: opacity var(--transition-fast);
}
.logo:hover { opacity: 0.85; }
.logo-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 34px;
  height: 34px;
  border-radius: var(--radius-md);
  background: linear-gradient(135deg, var(--color-primary), var(--color-primary-light));
  color: var(--color-text-inverse);
}
.logo-text {
  font-size: 16px;
  font-weight: 700;
  color: var(--color-text-primary);
  letter-spacing: -0.01em;
}
.nav-links {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
  flex: 1;
}
.nav-item {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 14px;
  border-radius: var(--radius-md);
  font-size: 14px;
  font-weight: 500;
  color: var(--color-text-secondary);
  text-decoration: none;
  transition: all var(--transition-fast);
  cursor: pointer;
  white-space: nowrap;
}
.nav-item:hover, .nav-item.active {
  color: var(--color-primary);
  background: var(--color-primary-bg);
}
.nav-item.active { font-weight: 600; }
.nav-icon { width: 16px; height: 16px; flex-shrink: 0; }
.header-actions {
  display: flex;
  align-items: center;
  flex-shrink: 0;
  gap: var(--space-sm);
}
.new-btn {
  border-radius: var(--radius-md) !important;
}
.user-btn {
  display: flex;
  align-items: center;
  gap: var(--space-sm);
  padding: 4px 10px 4px 4px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-full);
  background: var(--color-bg-card);
  cursor: pointer;
  transition: all var(--transition-fast);
  outline: none;
}
.user-btn:hover {
  border-color: var(--color-primary-lighter);
  background: var(--color-primary-bg);
}
.user-avatar {
  width: 28px;
  height: 28px;
  border-radius: 50%;
  background: linear-gradient(135deg, var(--color-primary), var(--color-primary-light));
  color: var(--color-text-inverse);
  display: flex;
  align-items: center;
  justify-content: center;
  font-size: 12px;
  font-weight: 700;
}
.user-name {
  font-size: 13px;
  font-weight: 500;
  color: var(--color-text-primary);
  max-width: 80px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.dropdown-arrow { font-size: 12px; color: var(--color-text-muted); }
.main-content {
  flex: 1;
  background: var(--color-bg-page);
  padding: var(--space-lg);
  min-height: 0;
}
.app-footer {
  padding: var(--space-md) var(--space-lg);
  text-align: center;
  font-size: 13px;
  color: var(--color-text-muted);
  border-top: 1px solid var(--color-border-light);
  background: var(--color-bg-card);
}
.mobile-only { display: none; }
.hamburger-btn {
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 36px;
  border: 1px solid var(--color-border);
  border-radius: var(--radius-md);
  background: var(--color-bg-card);
  cursor: pointer;
  color: var(--color-text-secondary);
  transition: all var(--transition-fast);
}
.hamburger-btn:hover {
  border-color: var(--color-primary-lighter);
  color: var(--color-primary);
}
.drawer-header { display: flex; align-items: center; gap: var(--space-sm); }
.drawer-avatar { width: 36px; height: 36px; font-size: 14px; }
.drawer-username { font-size: 15px; font-weight: 600; color: var(--color-text-primary); }
.drawer-nav { display: flex; flex-direction: column; gap: var(--space-xs); }
.drawer-nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 10px 14px;
  border-radius: var(--radius-md);
  font-size: 15px;
  font-weight: 500;
  color: var(--color-text-secondary);
  text-decoration: none;
  transition: all var(--transition-fast);
  cursor: pointer;
}
.drawer-nav-item:hover, .drawer-nav-item.active {
  color: var(--color-primary);
  background: var(--color-primary-bg);
}
.drawer-footer {
  margin-top: auto;
  padding-top: var(--space-lg);
  border-top: 1px solid var(--color-border-light);
  display: flex;
  flex-direction: column;
  gap: var(--space-xs);
}
@media (max-width: 768px) {
  .desktop-only { display: none !important; }
  .mobile-only { display: flex !important; }
  .header-inner { padding: 0 var(--space-md); }
  .logo-text { font-size: 14px; }
  .main-content { padding: var(--space-md); }
}
</style>

import { createRouter, createWebHistory } from 'vue-router'
import { useAuthStore } from '@/stores/auth'

const routes = [
  {
    path: '/login',
    name: 'Login',
    component: () => import('@/views/LoginView.vue'),
    meta: { public: true },
  },
  {
    path: '/tasks',
    name: 'TaskList',
    component: () => import('@/views/TaskListView.vue'),
  },
  {
    path: '/tasks/:id',
    name: 'TaskDetail',
    component: () => import('@/views/TaskDetailView.vue'),
  },
  {
    path: '/sync/new',
    name: 'SyncNew',
    component: () => import('@/views/SyncNewView.vue'),
  },
  {
    path: '/registries',
    name: 'Registries',
    component: () => import('@/views/RegistryView.vue'),
  },
  {
    path: '/',
    redirect: '/tasks',
  },
  {
    path: '/:pathMatch(.*)*',
    redirect: '/tasks',
  },
]

const router = createRouter({
  history: createWebHistory(),
  routes,
})

// 全局守卫:未登录访问受保护页 → 跳登录。
router.beforeEach((to) => {
  const authStore = useAuthStore()
  if (!to.meta.public && !authStore.isLoggedIn) {
    return { name: 'Login', query: { redirect: to.fullPath } }
  }
  if (to.name === 'Login' && authStore.isLoggedIn) {
    return { name: 'TaskList' }
  }
})

export default router

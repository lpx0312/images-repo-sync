import axios from 'axios'
import { ElMessage } from 'element-plus'
import { useAuthStore } from '@/stores/auth'

// client 是带统一拦截器的 axios 实例。
const client = axios.create({
  baseURL: '/api',
  timeout: 30000,
})

// 请求拦截:自动附加 Authorization。
client.interceptors.request.use((config) => {
  const authStore = useAuthStore()
  if (authStore.token) {
    config.headers.Authorization = `Bearer ${authStore.token}`
  }
  return config
})

// 响应拦截:统一拆 {data} / {error};401 清登录态并跳登录。
client.interceptors.response.use(
  (response) => {
    const body = response.data
    // 后端成功统一返回 { data: ... } 或 { data, total, ... }。
    if (body && typeof body === 'object' && ('data' in body || 'total' in body)) {
      return body
    }
    return body
  },
  (error) => {
    const status = error.response?.status
    const msg = error.response?.data?.error || error.message || '请求失败'
    // 请求的 URL,用于区分登录接口和其他接口。
    const reqURL = error.config?.url || ''

    if (status === 401) {
      // 登录接口的 401 = 账号密码错误,直接弹提示,不要清 token/跳转。
      if (reqURL.includes('/auth/login')) {
        ElMessage.error(msg)
      } else {
        // 其他接口的 401 = token 过期/无效,清登录态并跳登录页。
        const authStore = useAuthStore()
        authStore.clearAuth()
        const { pathname, search } = window.location
        if (!pathname.startsWith('/login')) {
          const redirect = encodeURIComponent(pathname + search)
          window.location.href = `/login?expired=1&redirect=${redirect}`
        }
      }
    } else if (status >= 500) {
      ElMessage.error('服务器异常,请稍后重试')
    } else {
      ElMessage.error(msg)
    }
    return Promise.reject(error)
  }
)

export default client

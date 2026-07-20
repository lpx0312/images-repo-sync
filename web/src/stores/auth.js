import { defineStore } from 'pinia'
import { ref, computed } from 'vue'
import { authAPI } from '@/api'

export const useAuthStore = defineStore('auth', () => {
  const token = ref(localStorage.getItem('token') || sessionStorage.getItem('token') || '')
  const user = ref(JSON.parse(localStorage.getItem('user') || sessionStorage.getItem('user') || 'null'))
  const lastActivityTime = ref(Date.now())
  let autoLogoutTimer = null

  const isLoggedIn = computed(() => !!token.value)
  const username = computed(() => user.value?.username || '')

  async function login(usernameVal, password, rememberMe = false) {
    const res = await authAPI.login({ username: usernameVal, password, remember_me: rememberMe })
    const body = res.data ?? res
    token.value = body.token
    user.value = body.user

    const storage = rememberMe ? localStorage : sessionStorage
    storage.setItem('token', body.token)
    storage.setItem('user', JSON.stringify(body.user))
    if (!rememberMe) {
      localStorage.removeItem('token')
      localStorage.removeItem('user')
    } else {
      sessionStorage.removeItem('token')
      sessionStorage.removeItem('user')
    }
    startAutoLogoutTimer()
    return body
  }

  async function logout() {
    if (token.value) {
      try {
        await authAPI.logout()
      } catch {
        /* ignore */
      }
    }
    clearAuth()
  }

  function clearAuth() {
    token.value = ''
    user.value = null
    localStorage.removeItem('token')
    localStorage.removeItem('user')
    sessionStorage.removeItem('token')
    sessionStorage.removeItem('user')
    stopAutoLogoutTimer()
  }

  async function fetchCurrentUser() {
    if (!token.value) return
    try {
      const res = await authAPI.me()
      user.value = res.data ?? res
      const storage = localStorage.getItem('token') ? localStorage : sessionStorage
      storage.setItem('user', JSON.stringify(user.value))
    } catch {
      clearAuth()
    }
  }

  async function changePassword(oldPassword, newPassword) {
    await authAPI.changePassword({ old_password: oldPassword, new_password: newPassword })
    clearAuth()
  }

  function updateActivityTime() {
    lastActivityTime.value = Date.now()
  }

  function startAutoLogoutTimer() {
    stopAutoLogoutTimer()
    autoLogoutTimer = setInterval(() => {
      const timeout = 30 * 60 * 1000
      if (Date.now() - lastActivityTime.value > timeout) {
        clearAuth()
        window.location.href = '/login?expired=1'
      }
    }, 60 * 1000)
  }

  function stopAutoLogoutTimer() {
    if (autoLogoutTimer) {
      clearInterval(autoLogoutTimer)
      autoLogoutTimer = null
    }
  }

  if (token.value) {
    startAutoLogoutTimer()
  }

  return {
    token,
    user,
    isLoggedIn,
    username,
    login,
    logout,
    clearAuth,
    fetchCurrentUser,
    changePassword,
    updateActivityTime,
    startAutoLogoutTimer,
    stopAutoLogoutTimer,
  }
})

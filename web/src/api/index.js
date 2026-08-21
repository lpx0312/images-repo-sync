import client from './client'

export const authAPI = {
  login: (payload) => client.post('/auth/login', payload),
  logout: () => client.post('/auth/logout'),
  me: () => client.get('/auth/me'),
  changePassword: (payload) => client.put('/auth/password', payload),
}

export const registryAPI = {
  list: (params) => client.get('/registries', { params }),
  create: (payload) => client.post('/registries', payload),
  update: (id, payload) => client.put(`/registries/${id}`, payload),
  remove: (id) => client.delete(`/registries/${id}`),
  test: (id) => client.post(`/registries/${id}/test`),
}

export const catalogAPI = {
  repos: (id, params) => client.get(`/catalog/${id}/repos`, { params }),
  tags: (id, params) => client.get(`/catalog/${id}/tags`, { params }),
  projects: (id) => client.get(`/catalog/${id}/projects`),
}

export const taskAPI = {
  create: (payload) => client.post('/tasks', payload),
  list: (params) => client.get('/tasks', { params }),
  get: (id) => client.get(`/tasks/${id}`),
  cancel: (id) => client.post(`/tasks/${id}/cancel`),
}

export const settingsAPI = {
  get: () => client.get('/settings'),
  update: (payload) => client.put('/settings', payload),
}

export const chartRepoAPI = {
  list: () => client.get('/chart-repos'),
  create: (payload) => client.post('/chart-repos', payload),
  update: (id, payload) => client.put(`/chart-repos/${id}`, payload),
  remove: (id) => client.delete(`/chart-repos/${id}`),
  test: (id) => client.post(`/chart-repos/${id}/test`),
}

export const chartUploadAPI = {
  // 大文件上传放宽超时(全局默认 30s 不够)。
  uploadFiles: (formData, onProgress) =>
    client.post('/charts/upload-files', formData, { timeout: 600000, onUploadProgress: onProgress }),
  uploadPaths: (payload) => client.post('/charts/upload-paths', payload, { timeout: 60000 }),
  list: (params) => client.get('/charts/uploads', { params }),
  retry: (id) => client.post(`/charts/uploads/${id}/retry`),
}

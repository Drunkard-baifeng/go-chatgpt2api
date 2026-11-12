import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { nanocatZhCN, setNanocatLocale } from 'nanocat-ui'
import 'nanocat-ui/styles.css'
import router from '@/router/index.ts'
import { setUnauthorizedHandler } from '@/api/client'
import { useAuthStore } from '@/stores/auth'
import { registerLocalIcons } from '@/lib/icons'
import { applyThemeMode, getStoredThemeMode } from '@/lib/theme'
import App from './App.vue'
import './styles/base.css'
import './styles/theme.css'
import './styles/workspace.css'
import './styles/drawers.css'

setNanocatLocale(nanocatZhCN)
registerLocalIcons()
applyThemeMode(getStoredThemeMode())

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)

setUnauthorizedHandler(() => {
  const authStore = useAuthStore()
  const redirect = router.currentRoute.value.fullPath
  authStore.clearIdentity()
  void router.replace({
    name: 'login',
    query: redirect && redirect !== '/login' ? { redirect } : undefined,
  }).catch(() => {})
})

app.use(router)

app.mount('#app')

<template>
  <main class="cpa-login">
    <div class="cpa-login-brand"><span class="cpa-login-symbol">C</span> ChatGPT2API</div>
    <section class="cpa-login-card" aria-labelledby="login-title">
      <div class="cpa-login-icon" aria-hidden="true"><svg width="28" height="28" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><rect x="5" y="10" width="14" height="11" rx="3"/><path d="M8 10V7a4 4 0 0 1 8 0v3m-4 5v2"/></svg></div>
      <h1 id="login-title">欢迎回来</h1><p>登录你的 ChatGPT2API 管理控制台</p>
      <form @submit.prevent="handleLogin">
        <label for="password">管理密钥</label>
        <Input id="password" v-model="password" type="password" size="md" block autocomplete="current-password" placeholder="输入管理密钥或用户密钥" :disabled="isLoading" />
        <Button type="submit" size="md" variant="primary" block :disabled="isLoading || !password">{{ isLoading ? '正在登录…' : '登录控制台' }}</Button>
      </form>
      <p class="cpa-login-help">使用部署时设置的管理密钥，或管理员分配的用户密钥。</p>
    </section>
    <footer>ChatGPT2API · 你的 AI 服务控制台</footer>
  </main>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { Button, Input } from 'nanocat-ui'
import { useToast } from '@/composables/useToast'
import { resolveLoginRedirect } from '~/router/routes'
import { useAuthStore } from '@/stores/auth'

const router = useRouter()
const route = useRoute()
const authStore = useAuthStore()
const toast = useToast()

const password = ref('')
const isLoading = ref(false)

async function handleLogin() {
  if (!password.value) return

  isLoading.value = true

  try {
    const loggedIn = await authStore.login(password.value)
    if (!loggedIn) {
      toast.error('密钥无效或已失效。')
      return
    }
    await router.replace(resolveLoginRedirect(route.query.redirect, authStore.homeRoute))
  } catch (error: unknown) {
    toast.error(error instanceof Error ? error.message : '登录失败，请检查密码。')
  } finally {
    isLoading.value = false
  }
}
</script>

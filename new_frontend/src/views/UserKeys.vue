<template>
  <div class="user-keys-page" :class="{ 'lg:min-h-0 lg:flex-1': isWorkspaceLayout }">
    <PanelHeader title="用户密钥管理" class="shrink-0">
      <template #copy><p class="mt-1 text-sm text-muted-foreground">管理普通用户的调用密钥与访问权限。</p></template>
      <template #actions>
        <Button size="sm" variant="outline" :disabled="userKeysLoading || Boolean(userKeyBusy)" @click="loadUserKeys">
          <Icon icon="lucide:refresh-cw" aria-hidden="true" />{{ userKeysLoading ? '刷新中...' : '刷新' }}
        </Button>
        <Button size="sm" variant="primary" :disabled="Boolean(userKeyBusy)" @click="openUserKeyCreateModal">
          <Icon icon="lucide:plus" aria-hidden="true" />创建用户密钥
        </Button>
      </template>
    </PanelHeader>

    <div v-if="newUserKey" class="key-reveal shrink-0" role="status">
      <div class="min-w-0"><p class="font-semibold">新密钥只展示一次，请现在复制保存。</p><p class="mt-2 break-all font-mono text-sm">{{ newUserKey }}</p></div>
      <Button size="sm" variant="outline" @click="copyUserKey(newUserKey)">复制密钥</Button>
    </div>
    <div v-if="userKeysError" class="key-error shrink-0" role="alert">
      <Icon icon="lucide:circle-alert" aria-hidden="true" /><span>{{ userKeysError }}{{ userKeysLoaded ? '，当前保留上次加载的列表。' : '' }}</span>
    </div>

    <PagePanel flush class="key-list-panel" :class="{ 'lg:min-h-0 lg:flex-1': isWorkspaceLayout }" :aria-busy="userKeysLoading">
      <div class="key-list-scroll" :class="{ 'lg:min-h-0 lg:flex-1 lg:overflow-y-auto': isWorkspaceLayout }">
        <PageLoadingState v-if="!userKeysLoaded && !userKeysError" title="正在加载用户密钥" description="读取普通用户密钥列表。" />
        <StateBlock v-else-if="!userKeysLoaded" title="用户密钥加载失败" description="请点击上方刷新按钮重试。" />
        <StateBlock v-else-if="userKeys.length === 0" title="暂无用户密钥" description="创建后可分发给需要使用对话画图或调用接口的用户。" />
        <UserKeysTable v-else :user-keys="userKeys" :user-key-busy="userKeyBusy" @edit="openUserKeyEditModal" @toggle="toggleUserKey" @delete="deleteUserKey" />
      </div>
      <div class="key-list-footer" role="status">{{ userKeysLoaded ? `共 ${userKeys.length} 条密钥` : '尚未加载' }}<span v-if="userKeysLoading && userKeysLoaded"> · 正在刷新</span></div>
    </PagePanel>

    <UserKeyModals :modal="userKeyModal" :form="userKeyForm" :editing-user-key="editingUserKey" :busy="userKeyBusy" @close="closeUserKeyModal" @create="createUserKey" @update="updateUserKey" />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Icon } from '@iconify/vue'
import { Button } from 'nanocat-ui'
import PagePanel from '@/components/ui/PagePanel.vue'
import PanelHeader from '@/components/ui/PanelHeader.vue'
import StateBlock from '@/components/ui/StateBlock.vue'
import PageLoadingState from '@/components/ai/PageLoadingState.vue'
import { usePageRuntime } from '@/composables/usePageRuntime'
import { usePageVisibilityReload } from '@/composables/usePageQuery'
import { useListLayoutPreference } from '@/composables/useListLayoutPreference'
import UserKeyModals from './user-keys/UserKeyModals.vue'
import UserKeysTable from './user-keys/UserKeysTable.vue'
import { useUserKeysRuntime } from './user-keys/userKeysRuntime'

defineOptions({ name: 'UserKeys' })
const { isWorkspaceLayout } = useListLayoutPreference()
const runtime = usePageRuntime('user-keys')
const keys = useUserKeysRuntime({ runtime, requestKey: 'user-keys:list' })
const {
  userKeys, userKeysLoaded, userKeysLoading, userKeysError, userKeyBusy, userKeyModal,
  editingUserKey, newUserKey, userKeyForm, copyUserKey, openUserKeyCreateModal,
  openUserKeyEditModal, closeUserKeyModal, loadUserKeys, createUserKey, updateUserKey,
  toggleUserKey, deleteUserKey,
} = keys
const canRefresh = computed(() => !userKeyBusy.value && !userKeyModal.value)
const visibilityReload = usePageVisibilityReload({ runtime, invalidate: keys.invalidate, reload: loadUserKeys, shouldReload: canRefresh })
runtime.onActivate(({ initial }) => {
  if (initial && runtime.canRun.value) void loadUserKeys()
  else visibilityReload.reloadIfPending()
})
runtime.onDeactivate(visibilityReload.invalidateForHide)
</script>

<style scoped>
.user-keys-page { display:flex; flex-direction:column; min-width:0; gap:20px; }
.key-list-panel { display:flex; flex-direction:column; }
.key-list-scroll { min-width:0; }
.key-list-footer { flex-shrink:0; padding:14px 20px; border-top:1px solid hsl(var(--border)); color:hsl(var(--muted-foreground)); font-size:12px; }
.key-reveal { display:flex; align-items:flex-start; justify-content:space-between; flex-wrap:wrap; gap:12px; padding:16px 20px; border:1px solid hsl(var(--tone-success-border)); border-radius:var(--app-radius-md); background:hsl(var(--tone-success-bg)); color:hsl(var(--tone-success-foreground)); font-size:14px; }
.key-reveal > div { flex:1 1 240px; }
.key-error { display:flex; align-items:center; gap:8px; padding:12px 16px; border-radius:var(--app-radius-md); background:hsl(var(--destructive) / .1); color:hsl(var(--destructive)); font-size:13px; }
.key-error > svg { flex-shrink:0; width:16px; height:16px; }
</style>

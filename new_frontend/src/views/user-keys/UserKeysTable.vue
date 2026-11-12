<template>
  <table class="user-keys-table" aria-label="用户密钥列表">
    <colgroup>
      <col class="key-name-col" />
      <col class="key-status-col" />
      <col class="key-date-col" />
      <col class="key-date-col" />
      <col class="key-actions-col" />
    </colgroup>
    <thead>
      <tr><th scope="col">名称</th><th scope="col">状态</th><th scope="col">创建时间</th><th scope="col">最近使用</th><th scope="col" class="key-actions-heading">操作</th></tr>
    </thead>
    <tbody>
      <tr v-for="item in userKeys" :key="item.id">
        <td class="key-name" data-label="名称"><span>{{ item.name || '普通用户' }}</span></td>
        <td data-label="状态">
          <StateBadge :tone="item.enabled ? 'success' : 'muted'" size="sm">{{ item.enabled ? '已启用' : '已禁用' }}</StateBadge>
        </td>
        <td class="key-time" data-label="创建时间"><time :datetime="item.created_at || undefined">{{ formatTime(item.created_at) }}</time></td>
        <td class="key-time" data-label="最近使用"><time :datetime="item.last_used_at || undefined">{{ formatTime(item.last_used_at) }}</time></td>
        <td class="key-actions-cell" data-label="操作">
          <div class="key-actions">
            <Button size="sm" variant="ghost" :aria-label="`编辑 ${item.name || '普通用户'}`" :disabled="Boolean(userKeyBusy)" @click="$emit('edit', item)">
              <Icon icon="lucide:pencil" aria-hidden="true" />编辑
            </Button>
            <Button size="sm" variant="ghost" :aria-label="`${item.enabled ? '禁用' : '启用'} ${item.name || '普通用户'}`" :disabled="Boolean(userKeyBusy)" @click="$emit('toggle', item)">
              <Icon :icon="item.enabled ? 'lucide:ban' : 'lucide:circle-check'" aria-hidden="true" />{{ item.enabled ? '禁用' : '启用' }}
            </Button>
            <Button size="sm" variant="ghost" :aria-label="`删除 ${item.name || '普通用户'}`" root-class="text-destructive" :disabled="Boolean(userKeyBusy)" @click="$emit('delete', item)">
              <Icon icon="lucide:trash-2" aria-hidden="true" />删除
            </Button>
          </div>
        </td>
      </tr>
    </tbody>
  </table>
</template>

<script setup lang="ts">
import { Icon } from '@iconify/vue'
import { Button } from 'nanocat-ui'
import type { UserKey } from '@/api/userKeys'
import StateBadge from '@/components/ai/StateBadge.vue'

defineProps<{ userKeys: UserKey[]; userKeyBusy: string }>()
defineEmits<{ edit: [item: UserKey]; toggle: [item: UserKey]; delete: [item: UserKey] }>()

const dateFormat = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai', year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})
function formatTime(value?: string | null): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? '—' : dateFormat.format(date).replace(/\//g, '-')
}
</script>

<style scoped>
.user-keys-table { width:100%; border-collapse:collapse; table-layout:fixed; text-align:left; font-size:14px; }
.key-name-col { width:24%; }
.key-status-col { width:12%; }
.key-date-col { width:20%; }
.key-actions-col { width:24%; }
th { position:sticky; top:0; z-index:1; padding:15px 20px; background:hsl(var(--muted)); color:var(--console-text-regular); font-size:14px; font-weight:650; white-space:nowrap; }
td { height:76px; padding:14px 20px; border-top:1px solid hsl(var(--border)); vertical-align:middle; }
tbody tr:hover { background:hsl(var(--muted) / .28); }
.key-name { font-weight:650; overflow-wrap:anywhere; }
.key-time { color:var(--console-text-regular); font-size:14px; font-weight:500; font-variant-numeric:tabular-nums; }
.key-time time { display:inline-block; }
.key-actions-heading { text-align:right; }
.key-actions { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:4px; }
.key-actions :deep(.ui-btn) { padding-inline:8px; }
@media(max-width:1199px) and (min-width:768px) {
  th, td { padding-inline:12px; }
  .key-name-col { width:20%; }
  .key-status-col { width:12%; }
  .key-actions-col { width:28%; }
}
@media(max-width:767px) {
  .user-keys-table, tbody { display:block; }
  colgroup { display:none; }
  thead { position:absolute; width:1px; height:1px; padding:0; overflow:hidden; clip-path:inset(50%); white-space:nowrap; }
  tbody tr { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:12px; padding:16px; border-top:1px solid hsl(var(--border)); }
  tbody tr:first-child { border-top:0; }
  td { display:block; height:auto; padding:0; border:0; }
  .key-name { align-self:center; font-size:15px; }
  .key-time { grid-column:1 / -1; display:flex; justify-content:space-between; gap:12px; }
  .key-time::before { content:attr(data-label); font-weight:500; }
  .key-actions-cell { grid-column:1 / -1; padding-top:12px; border-top:1px solid hsl(var(--border)); }
  .key-actions { gap:8px; }
  .key-actions :deep(.ui-btn) { flex:1; min-height:40px; }
}
</style>

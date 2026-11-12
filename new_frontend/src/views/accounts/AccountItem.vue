<template>
  <component :is="layout === 'row' ? 'tr' : 'article'" class="cpa-account-item" :class="[`is-${layout}`, { 'is-selected': selected }]"
    :aria-selected="layout === 'row' ? selected : undefined" :aria-label="layout === 'card' ? accountPrimaryText(item) : undefined">
    <component :is="cellTag" class="cpa-account-select">
      <Checkbox :model-value="selected" :aria-label="`选择账号 ${accountPrimaryText(item)}`" @update:model-value="emit('toggle-select', item.id, $event)" />
    </component>
    <component :is="cellTag" class="cpa-account-identity">
      <div class="cpa-account-identity-main">
        <span class="cpa-account-avatar" aria-hidden="true"><Icon icon="lucide:user-round" /></span>
        <div class="cpa-account-identity-copy">
          <h3 :title="accountPrimaryText(item)">{{ accountPrimaryText(item) }}</h3>
          <p class="cpa-account-id" :title="accountSecondaryText(item)">{{ accountSecondaryText(item) }}</p>
          <AccountCredentialStatus v-if="layout === 'card'" :item="item" @copy-credential="emit('copy-credential', item, $event)" />
        </div>
      </div>
    </component>
    <template v-if="layout === 'row'">
      <td><AccountCredentialStatus kind="access" :item="item" @copy-credential="emit('copy-credential', item, $event)" /></td>
      <td><AccountCredentialStatus kind="refresh" :item="item" @copy-credential="emit('copy-credential', item, $event)" /></td>
    </template>
    <component :is="cellTag" class="cpa-account-status">
      <StatusDetailPill :label="statusText(item)" :tone="item.status_tone" title="状态详情" detail-label="状态说明" raw-error-label="原始报错"
        :card-class="statusDetailCardClass" :detail="statusDetailText(item)" :raw-error="statusRawError(item)" />
      <template v-if="layout === 'card'">
        <p class="cpa-account-source">{{ accountSourceText(item) }}</p>
        <p v-if="item.group_name" class="cpa-account-meta" :title="item.group_name">{{ item.group_name }}</p>
      </template>
    </component>
    <template v-if="layout === 'row'">
      <td>{{ item.source_label }}</td>
      <td><span class="cpa-account-cell-text" :title="item.plan_label">{{ item.plan_label }}</span></td>
      <td><span class="cpa-account-cell-text" :title="item.group_name || '未分组'">{{ item.group_name || '未分组' }}</span></td>
    </template>
    <component :is="cellTag" class="cpa-account-activity">
      <span class="cpa-account-section-label">最近调用</span>
      <p class="cpa-account-date">{{ formatAccountDate(item.last_used_at) }}</p>
      <p v-if="layout === 'card'" class="cpa-account-meta">创建 {{ accountCreatedText(item) }}</p>
    </component>
    <template v-if="layout === 'row'">
      <td class="cpa-account-date">{{ accountCreatedText(item) }}</td>
      <td class="cpa-account-number" :aria-label="callRatioLabel">
        <div class="cpa-account-call-counts">
          <MetaChip class="cpa-account-metric" tone="success" size="md" strong :aria-label="`成功 ${formatCount(item.success_count)} 次`">{{ formatCount(item.success_count) }}</MetaChip>
          <span class="cpa-account-call-separator" aria-hidden="true">/</span>
          <MetaChip class="cpa-account-metric" tone="error" size="md" strong :aria-label="`失败 ${formatCount(item.failure_count)} 次`">{{ formatCount(item.failure_count) }}</MetaChip>
        </div>
      </td>
      <td class="cpa-account-number"><MetaChip class="cpa-account-metric" tone="info" size="md" strong>{{ formatCount(item.image_inflight) }}</MetaChip></td>
    </template>
    <div v-else class="cpa-account-usage">
      <span class="cpa-account-section-label">调用记录</span>
      <div class="cpa-account-counts">
        <MetaChip class="cpa-account-metric" tone="success" size="md" strong>成功 {{ formatCount(item.success_count) }}</MetaChip>
        <MetaChip class="cpa-account-metric" tone="error" size="md" strong>失败 {{ formatCount(item.failure_count) }}</MetaChip>
      </div>
      <div class="cpa-account-call-bar" role="img" :aria-label="callRatioLabel" :title="callRatioLabel">
        <span class="is-success" :style="{ width: `${callRatio.success}%` }" />
        <span class="is-failure" :style="{ width: `${callRatio.failure}%` }" />
      </div>
      <div class="cpa-account-inflight"><MetaChip class="cpa-account-metric" tone="info" size="md" strong>正在生图 {{ formatCount(item.image_inflight) }}</MetaChip></div>
    </div>
    <component :is="cellTag" class="cpa-account-quota">
      <span v-if="layout === 'card'" class="cpa-account-section-label">图片额度</span>
      <MetaChip class="cpa-account-metric" size="md" strong>{{ item.quota_label }}</MetaChip>
      <p v-if="layout === 'card'" class="cpa-account-meta"><Icon icon="lucide:clock-3" aria-hidden="true" />恢复 {{ accountRestoreText(item) }}</p>
    </component>
    <template v-if="layout === 'row'">
      <td class="cpa-account-date">{{ accountRestoreText(item) }}</td>
      <td>
        <button type="button" class="cpa-account-switch" role="switch" :aria-checked="item.enabled" :aria-label="`${item.enabled_action_label} ${accountPrimaryText(item)}`" :title="item.enabled_action_label" :disabled="actionBusy" @click="emit('toggle-enabled', item)"><span aria-hidden="true" /></button>
      </td>
    </template>
    <component :is="cellTag" class="cpa-account-actions-cell">
      <div class="cpa-account-actions">
        <div class="cpa-account-quick-actions">
          <button type="button" class="cpa-account-icon-action" :disabled="actionBusy" :aria-label="syncing ? '同步中' : '同步账号与额度'" title="同步账号与额度" @click="emit('sync-account', item)"><Icon icon="lucide:refresh-cw" :class="{ 'is-spinning': syncing }" /></button>
          <button type="button" class="cpa-account-icon-action" :disabled="actionBusy" aria-label="编辑账号" title="编辑账号" @click="emit('edit', item)"><Icon icon="lucide:settings-2" /></button>
          <button type="button" class="cpa-account-icon-action" :disabled="actionBusy" aria-label="测试账号" title="测试账号" @click="emit('test', item)"><Icon icon="lucide:flask-conical" /></button>
          <button type="button" class="cpa-account-icon-action" :disabled="actionBusy || !item.can_refresh_access_token" :aria-label="refreshingAccessToken ? '刷新 AT 中' : '刷新 AT'" :title="item.can_refresh_access_token ? '刷新 AT' : '当前凭证不支持刷新 AT'" @click="emit('refresh-access-token', item)"><Icon icon="lucide:key-round" :class="{ 'is-spinning': refreshingAccessToken }" /></button>
          <button type="button" class="cpa-account-icon-action is-danger" :disabled="actionBusy" aria-label="删除账号" title="删除账号" @click="emit('remove', item)"><Icon icon="lucide:trash-2" /></button>
        </div>
        <div v-if="layout === 'card'" class="cpa-account-enabled">
          <button type="button" class="cpa-account-switch" role="switch" :aria-checked="item.enabled" :aria-label="`${item.enabled_action_label} ${accountPrimaryText(item)}`" :title="item.enabled_action_label" :disabled="actionBusy" @click="emit('toggle-enabled', item)"><span aria-hidden="true" /></button>
          <span>{{ item.enabled ? '已启用' : '已禁用' }}</span>
        </div>
      </div>
    </component>
  </component>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Icon } from '@iconify/vue'
import { Checkbox, MetaChip, StatusDetailPill } from 'nanocat-ui'
import type { Account } from '@/api/accounts'
import AccountCredentialStatus from '@/views/accounts/AccountCredentialStatus.vue'
import { accountCreatedText, accountPrimaryText, accountRestoreText, accountSecondaryText, accountSourceText, formatAccountDate, statusRawError, statusText } from '@/views/accounts/viewUtils'

const props = withDefaults(defineProps<{
  item: Account; layout: 'row' | 'card'; selected: boolean; syncing?: boolean
  refreshingAccessToken?: boolean; busy?: boolean; statusDetailCardClass?: string
  statusDetailText: (item: Account) => string
}>(), { syncing: false, refreshingAccessToken: false, busy: false, statusDetailCardClass: '' })
const cellTag = computed(() => props.layout === 'row' ? 'td' : 'div')
const actionBusy = computed(() => props.busy || props.syncing || props.refreshingAccessToken)
const callRatio = computed(() => {
  const total = props.item.success_count + props.item.failure_count
  if (total === 0) return { success: 0, failure: 0 }
  const success = props.item.success_count / total * 100
  return { success, failure: 100 - success }
})
const callRatioLabel = computed(() => props.item.success_count + props.item.failure_count === 0
  ? '暂无调用记录'
  : `成功 ${formatCount(props.item.success_count)} 次，失败 ${formatCount(props.item.failure_count)} 次`)
const formatCount = (value: number) => (value || 0).toLocaleString('zh-CN')
const emit = defineEmits<{
  'toggle-select': [id: string, checked: unknown]
  'copy-credential': [item: Account, kind: 'access' | 'refresh']
  edit: [item: Account]; test: [item: Account]; 'toggle-enabled': [item: Account]
  'sync-account': [item: Account]; 'refresh-access-token': [item: Account]; remove: [item: Account]
}>()
</script>

<style scoped>
/* Account group geometry and quick actions adapted from CPA AccountsPage.module.scss. */
.cpa-account-item { --account-row-bg:var(--console-glass); --account-row-border:var(--console-border); color:var(--app-text-primary); }
.cpa-account-item.is-selected { --account-row-bg:color-mix(in srgb,var(--console-primary-solid) 7%,var(--console-glass)); --account-row-border:hsl(var(--primary) / .42); }
.is-row { height:72px; }
.is-row > td { padding:10px 14px; vertical-align:middle; font-size:12px; background:var(--account-row-bg); border-top:1px solid var(--account-row-border); border-bottom:1px solid var(--account-row-border); transition:background-color .18s,border-color .18s; }
.is-row > td:first-child { border-left:1px solid var(--account-row-border); border-radius:8px 0 0 8px; padding-right:0; }
.is-row > td:last-child { border-right:1px solid var(--account-row-border); border-radius:0 8px 8px 0; }
.is-row.is-selected > td:first-child { box-shadow:inset 3px 0 0 var(--console-primary-solid); }
.cpa-account-identity-main { display:flex; align-items:center; gap:12px; min-width:0; }
.cpa-account-avatar { display:grid; place-items:center; width:38px; height:38px; flex-shrink:0; border-radius:12px; background:hsl(var(--primary) / .09); color:var(--console-primary-solid); }
.cpa-account-avatar svg { width:21px; height:21px; }
.cpa-account-identity-copy { min-width:0; display:flex; flex-direction:column; align-items:flex-start; gap:7px; }
.is-row .cpa-account-identity-copy { gap:3px; }
.cpa-account-cell-text { display:block; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.cpa-account-number { font-weight:700; font-variant-numeric:tabular-nums; }
.cpa-account-metric { font-size:14px !important; }
.cpa-account-call-counts { display:flex; align-items:center; flex-wrap:wrap; gap:6px; }
.cpa-account-call-separator { color:var(--console-text-regular); }
.cpa-account-identity h3 { max-width:100%; font-size:14px; font-weight:750; overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.cpa-account-id { max-width:100%; font-size:11px; color:var(--console-text-regular); overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.cpa-account-source { margin-top:10px; font-size:12px; font-weight:600; }
.cpa-account-meta { display:flex; align-items:center; gap:5px; margin-top:8px; font-size:11px; line-height:1.5; color:var(--console-text-regular); overflow-wrap:anywhere; }
.cpa-account-meta svg { width:13px; height:13px; flex-shrink:0; }
.cpa-account-date { font-size:12px; font-weight:600; font-variant-numeric:tabular-nums; }
.cpa-account-section-label { display:block; margin-bottom:6px; color:var(--console-text-regular); font-size:11px; font-weight:400; }
.is-row .cpa-account-activity > .cpa-account-section-label,.is-row .cpa-account-usage > .cpa-account-section-label { display:none; }
.cpa-account-counts { display:flex; align-items:center; flex-wrap:wrap; gap:8px; font-variant-numeric:tabular-nums; }
.cpa-account-inflight { margin-top:10px; font-variant-numeric:tabular-nums; }
.cpa-account-call-bar { display:flex; height:6px; margin-top:6px; overflow:hidden; border-radius:999px; background:var(--data-track-bg); }
.cpa-account-call-bar > span { height:100%; flex-shrink:0; }
.cpa-account-call-bar > .is-success { background:var(--data-green-base); }
.cpa-account-call-bar > .is-failure { background:var(--data-red-base); }
.cpa-account-quota { font-variant-numeric:tabular-nums; }
.cpa-account-actions { display:flex; align-items:center; justify-content:flex-end; gap:12px; }
.cpa-account-quick-actions { display:grid; grid-template-columns:repeat(3,30px); gap:5px; }
.is-row .cpa-account-quick-actions { grid-template-columns:repeat(5,30px); }
.cpa-account-icon-action { display:grid; place-items:center; width:30px; height:30px; border-radius:8px; background:hsl(var(--primary) / .085); color:var(--console-primary-solid); transition:background-color .18s,transform .18s; }
.cpa-account-icon-action svg { width:16px; height:16px; }
.cpa-account-icon-action.is-danger { background:hsl(var(--tone-error-bg)); color:hsl(var(--tone-error-strong)); }
.cpa-account-enabled { display:flex; flex-direction:column; align-items:center; gap:8px; border-left:1px solid var(--console-border); padding-left:12px; }
.cpa-account-enabled > span { font-size:11px; color:var(--console-text-regular); white-space:nowrap; }
.cpa-account-switch { display:flex; align-items:center; width:38px; height:22px; padding:2px; border:1px solid hsl(var(--input)); border-radius:999px; background:hsl(var(--muted-foreground) / .25); transition:background-color .18s; }
.cpa-account-switch span { width:16px; height:16px; border-radius:50%; background:#fff; box-shadow:0 1px 3px rgb(0 0 0 / .15); transition:transform .18s; }
.cpa-account-switch[aria-checked='true'] { background:var(--console-primary-solid); border-color:var(--console-primary-solid); }
.cpa-account-switch[aria-checked='true'] span { transform:translateX(16px); }
.cpa-account-item button:disabled { opacity:.4; cursor:not-allowed; }
.cpa-account-item button:focus-visible { outline:2px solid hsl(var(--ring)); outline-offset:3px; }
.is-card { display:grid; grid-template-columns:24px minmax(0,1fr); gap:18px 8px; align-content:start; padding:18px; border:1px solid var(--account-row-border); border-radius:12px; background:var(--account-row-bg); transition:border-color .18s,background-color .18s; }
.is-card .cpa-account-select { padding-top:9px; }
.is-card .cpa-account-avatar { display:none; }
.is-card .cpa-account-status { grid-column:1 / -1; display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.is-card .cpa-account-source,.is-card .cpa-account-status .cpa-account-meta { margin:0; }
.is-card .cpa-account-activity,.is-card .cpa-account-usage,.is-card .cpa-account-quota,.is-card .cpa-account-actions-cell { grid-column:1 / -1; }
.is-card .cpa-account-activity { padding-top:14px; border-top:1px solid var(--console-border); }
.is-card .cpa-account-usage { padding:12px; border-radius:10px; background:hsl(var(--secondary) / .5); }
.is-card .cpa-account-actions-cell { padding-top:14px; border-top:1px solid var(--console-border); }
.is-card .cpa-account-actions { justify-content:space-between; }
.is-card .cpa-account-quick-actions { grid-template-columns:repeat(5,30px); }
.is-card .cpa-account-enabled { padding-left:10px; }
.is-spinning { animation:cpa-account-spin 1s linear infinite; }
@keyframes cpa-account-spin { to { transform:rotate(360deg); } }
@media(hover:hover) {
  .cpa-account-item:hover { --account-row-border:hsl(var(--primary) / .25); --account-row-bg:color-mix(in srgb,var(--console-primary-solid) 3%,var(--console-glass)); }
  .cpa-account-item.is-selected:hover { --account-row-bg:color-mix(in srgb,var(--console-primary-solid) 8%,var(--console-glass)); }
  .cpa-account-icon-action:hover:not(:disabled) { background:hsl(var(--primary) / .16); transform:translateY(-1px); }
  .cpa-account-icon-action.is-danger:hover:not(:disabled) { background:hsl(var(--tone-error-bg) / .8); }
}
@media(prefers-reduced-motion:reduce) { .cpa-account-item,.is-row > td,.cpa-account-icon-action,.cpa-account-switch,.cpa-account-switch span { transition:none; } .is-spinning { animation:none; } .cpa-account-icon-action:hover { transform:none; } }
</style>

<template>
  <tr v-memo="[signature, active]" class="monitor-request-row" tabindex="0"
    :aria-label="`查看请求 ${row.call_id} 的详情`"
    @click="emit('open-detail', row)"
    @keydown.enter.self.prevent="emit('open-detail', row)"
    @keydown.space.self.prevent="emit('open-detail', row)">
    <td class="request-identity" data-label="请求 / 模型">
      <strong :title="row.model || '-'">{{ row.model || '-' }}</strong>
      <span class="request-id" :title="row.call_id">{{ shortCallId(row.call_id) }}</span>
      <small>{{ active ? row.endpoint || '-' : row.ended_at || row.updated_at || '-' }}</small>
    </td>
    <td class="request-status" :data-label="active ? '当前阶段' : '状态'">
      <StateBadge :tone="active ? 'info' : row.presentation.status_tone" shape="rounded" size="xs">
        {{ active ? row.presentation.stage_text : row.presentation.status_label }}
      </StateBadge>
    </td>
    <td class="request-duration" :data-label="active ? '已耗时' : '总耗时'">
      <strong>{{ row.presentation.duration_text }}</strong>
    </td>
    <td class="request-timing" data-label="关键耗时">{{ row.presentation.metric_digest }}</td>
    <td class="request-account" data-label="账号 / 出口">
      <template v-if="active">
        <span>{{ row.account_email || '-' }}</span>
        <small>{{ row.presentation.egress_text }} · {{ row.presentation.account_attempt_text }}</small>
        <small v-if="row.previous_account_email" :title="row.previous_account_email">上一账号：{{ row.previous_account_email }}</small>
      </template>
      <span v-else>{{ row.presentation.account_egress_text }}</span>
    </td>
    <td class="request-action">
      <button type="button" tabindex="-1" :aria-label="`查看请求 ${row.call_id} 的详情`" @click.stop="emit('open-detail', row)">
        <Icon icon="lucide:chevron-right" aria-hidden="true" />
      </button>
    </td>
  </tr>
</template>

<script setup lang="ts">
import { Icon } from '@iconify/vue'
import type { RealtimeMonitorRecord } from '@/api/monitor'
import StateBadge from '@/components/ai/StateBadge.vue'
import { shortCallId } from '@/views/monitor/monitorView'

defineProps<{ row: RealtimeMonitorRecord; signature: string; active?: boolean }>()
const emit = defineEmits<{ (e: 'open-detail', row: RealtimeMonitorRecord): void }>()
</script>

<style scoped>
/* CPA monitoring table: compact type, separate identity metadata and quiet row hover. */
.monitor-request-row { cursor:pointer; }
.monitor-request-row td { padding:10px 12px; height:76px; border-bottom:1px solid var(--console-border); font-size:12px; line-height:1.45; vertical-align:middle; overflow-wrap:anywhere; transition:background .18s ease; }
.monitor-request-row:hover td { background:color-mix(in srgb,var(--console-primary-solid) 6%,transparent); }
.monitor-request-row:focus-visible { outline:2px solid var(--console-primary-solid); outline-offset:-2px; }
.request-identity strong,.request-account > span { display:block; font-size:13px; font-weight:600; }
.request-identity strong { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.request-id { font-size:11px; font-family:var(--font-mono,monospace); color:var(--console-text-regular); }
.monitor-request-row small { display:block; margin-top:2px; font-size:11px; color:hsl(var(--muted-foreground)); }
.request-duration strong { font-size:16px; font-weight:600; font-variant-numeric:tabular-nums; }
.request-timing { color:var(--console-text-regular); }
.request-action button { display:flex; align-items:center; justify-content:center; width:28px; height:28px; border:1px solid var(--console-border); border-radius:8px; color:var(--console-text-regular); background:transparent; }
.request-action button:hover { color:var(--console-primary-solid); border-color:var(--console-primary-solid); }
@media(max-width:760px) {
  .monitor-request-row { display:grid; grid-template-columns:minmax(0,1fr) auto; gap:10px 12px; padding:14px 16px; border-bottom:1px solid var(--console-border); }
  .monitor-request-row td { display:block; height:auto; padding:0; border:0; min-width:0; }
  .monitor-request-row:hover { background:color-mix(in srgb,var(--console-primary-solid) 6%,transparent); }
  .monitor-request-row:hover td { background:transparent; }
  .request-status { grid-column:2; grid-row:1; align-self:start; }
  .request-duration { grid-column:1; }
  .request-duration::before,.request-timing::before,.request-account::before { content:attr(data-label); display:block; margin-bottom:3px; font-size:10px; color:hsl(var(--muted-foreground)); }
  .request-timing,.request-account { grid-column:1 / -1; }
  .request-action { grid-column:2; grid-row:2; align-self:center; }
  .request-action button { width:32px; height:32px; }
}
@media(prefers-reduced-motion:reduce) { .monitor-request-row td { transition:none; } }
</style>

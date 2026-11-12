<template>
  <button v-memo="[signature]" type="button" class="monitor-slow-card" @click="emit('open-detail', row)">
    <span class="slow-heading">
      <span class="slow-identity"><strong>{{ row.model || '-' }}</strong><small :title="row.call_id">{{ shortCallId(row.call_id) }} · {{ row.endpoint || '-' }}</small></span>
      <span class="slow-duration"><small>总耗时</small><strong>{{ row.presentation.duration_text }}</strong></span>
    </span>
    <span class="slow-state"><StateBadge :tone="row.presentation.status_tone" size="xs" shape="rounded">{{ row.presentation.status_label }}</StateBadge><span>耗时明细</span></span>
    <span class="slow-metrics">
      <span v-for="item in row.presentation.slow_metrics" :key="item.key" class="slow-metric" :class="{ 'is-important': item.important }">
        <span>{{ item.label }}</span><strong>{{ item.value_text }}</strong>
      </span>
    </span>
    <span v-if="row.presentation.slow_reason" class="slow-reason">{{ row.presentation.slow_reason }}</span>
    <span v-if="row.presentation.error_text" class="slow-error">{{ row.presentation.error_text }}</span>
    <span class="slow-footer">查看请求详情<Icon icon="lucide:chevron-right" aria-hidden="true" /></span>
  </button>
</template>

<script setup lang="ts">
import { Icon } from '@iconify/vue'
import type { RealtimeMonitorRecord } from '@/api/monitor'
import StateBadge from '@/components/ai/StateBadge.vue'
import { shortCallId } from '@/views/monitor/monitorView'
defineProps<{ row: RealtimeMonitorRecord; signature: string }>()
const emit = defineEmits<{ (e: 'open-detail', row: RealtimeMonitorRecord): void }>()
</script>

<style scoped>
.monitor-slow-card { display:flex; flex-direction:column; gap:12px; min-width:0; width:100%; padding:14px; text-align:left; border:1px solid var(--console-border); border-radius:11px; background:var(--console-glass); transition:border-color .18s ease,background .18s ease,transform .18s ease; }
.monitor-slow-card:hover { border-color:var(--console-primary-solid); background:color-mix(in srgb,var(--console-primary-solid) 4%,var(--cpa-drawer-surface)); transform:translateY(-1px); }
.monitor-slow-card:focus-visible { outline:2px solid var(--console-primary-solid); outline-offset:2px; }
.slow-heading { display:flex; justify-content:space-between; gap:16px; }
.slow-identity { display:grid; align-content:start; gap:4px; min-width:0; }
.slow-identity strong { font-size:13px; overflow-wrap:anywhere; }
.slow-identity small,.slow-duration small { font-size:11px; color:hsl(var(--muted-foreground)); overflow-wrap:anywhere; }
.slow-duration { display:flex; flex-direction:column; flex:none; align-items:flex-end; gap:2px; }
.slow-duration strong { font-size:22px; font-weight:600; font-variant-numeric:tabular-nums; }
.slow-state { display:flex; align-items:center; justify-content:space-between; gap:8px; font-size:11px; color:hsl(var(--muted-foreground)); }
.slow-metrics { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); column-gap:20px; }
.slow-metric { display:flex; align-items:baseline; justify-content:space-between; gap:8px; padding:8px 0; border-top:1px solid var(--console-border); font-size:12px; color:var(--console-text-regular); overflow-wrap:anywhere; }
.slow-metric strong { flex-shrink:0; color:hsl(var(--foreground)); font-variant-numeric:tabular-nums; font-weight:600; }
.slow-metric.is-important strong { color:var(--data-blue-base); }
.slow-reason,.slow-error { font-size:12px; line-height:1.5; overflow-wrap:anywhere; }
.slow-reason { color:var(--console-text-regular); }
.slow-error { color:var(--data-red-base); }
.slow-footer { display:flex; align-items:center; justify-content:flex-end; gap:6px; margin-top:auto; font-size:11px; color:var(--console-primary-solid); }
@media(max-width:640px) { .slow-metrics { grid-template-columns:minmax(0,1fr); } }
@media(prefers-reduced-motion:reduce) { .monitor-slow-card { transition:none; transform:none; } }
</style>

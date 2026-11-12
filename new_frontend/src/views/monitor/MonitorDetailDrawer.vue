<template>
  <RequestDetailDrawer :open="open" title="请求详情" :loading="loading" :error="error"
    max-width="40rem" root-class="monitor-call-detail-drawer" detached
    loading-title="正在读取请求详情" loading-description="正在整理阶段时间线..."
    error-title="请求详情加载失败" @close="emit('close')">
    <template v-if="record">
      <section class="call-summary">
        <div class="call-summary-heading">
          <div><StateBadge :tone="record.presentation.status_tone" shape="rounded">{{ record.presentation.status_label }}</StateBadge><h2>{{ record.model || '-' }}</h2></div>
          <div class="call-duration"><span>总耗时</span><strong>{{ record.presentation.duration_text }}</strong></div>
        </div>
        <p class="call-id">{{ record.call_id }}</p>
        <p class="call-endpoint">{{ record.endpoint || '-' }}</p>
      </section>
      <section class="call-information">
        <h3>请求信息</h3>
        <dl class="call-facts">
          <div><dt>账号</dt><dd>{{ record.account_email || '-' }}</dd></div>
          <div><dt>出口</dt><dd>{{ record.presentation.egress_text }}</dd></div>
          <div><dt>账号尝试</dt><dd>{{ record.presentation.account_attempt_text || '-' }}</dd></div>
          <div v-if="record.previous_account_email"><dt>上一账号</dt><dd>{{ record.previous_account_email }}</dd></div>
          <div><dt>关键耗时</dt><dd>{{ record.presentation.metric_digest }}</dd></div>
        </dl>
      </section>
      <RequestDetailFields
        v-if="record.detail_presentation"
        :primary-fields="record.detail_presentation.primary_fields"
        :diagnostic-fields="record.detail_presentation.diagnostic_fields"
        @copy="copyDetail"
      />
      <StateBlock v-if="record.detail_presentation?.diagnostic_notice" title="诊断原文缺失" :description="record.detail_presentation.diagnostic_notice" compact />
      <DetailTextBlock title="对外错误" :content="record.public_error || record.presentation.error_text" tone="danger" @copy="copyDetail" />
      <DetailTextBlock title="上游错误" :content="record.upstream_error" tone="danger" @copy="copyDetail" />
      <DetailTextBlock title="上游文本" :content="record.upstream_text" tone="warning" @copy="copyDetail" />
      <DetailTextBlock title="底层错误" :content="record.raw_error" tone="danger" @copy="copyDetail" />
      <LogsImageAttemptTimeline
        v-if="record.detail_presentation?.has_attempt_breakdown"
        :attempts="attempts"
        :groups="record.detail_presentation.attempt_groups"
        :requested-count="record.image_requested_count"
        :succeeded-count="record.image_succeeded_count"
        :failed-count="record.image_failed_count"
        :switch-count="record.switch_count"
      />
      <StateBlock v-if="record.presentation.slow_reason" title="耗时分析" :description="record.presentation.slow_reason" compact />
      <section class="call-stages">
        <div class="call-section-heading"><h3>阶段时间线</h3><span>{{ events.length }} 条记录</span></div>
        <ol v-if="events.length" class="call-timeline">
          <li v-for="(event, index) in events" :key="`${event.event}-${event.time}-${index}`">
            <span class="call-stage-index" aria-hidden="true">{{ index + 1 }}</span>
            <div class="call-stage-content">
              <div class="call-stage-heading"><strong>{{ event.label || event.event }}</strong><time :datetime="event.time || undefined">{{ formatMonitorEventTime(event.time) }}</time></div>
              <p v-if="event.timing_text && event.timing_text !== '-'" class="call-stage-timing">{{ event.timing_text }}</p>
              <p v-if="event.detail_text" class="call-stage-description">{{ event.detail_text }}</p>
            </div>
          </li>
        </ol>
        <EmptyState v-else plain title="暂无阶段记录" description="该请求没有保留可展示的阶段事件。" />
      </section>
      <DetailTextBlock :title="record.request_text_truncated ? '输入原文（超过 5 万字，已截断）' : '输入原文'"
        :content="record.request_text_full || record.request_text || ''" @copy="copyDetail" />
      <DetailTextBlock title="结果 URL" :content="(record.image_urls || []).join('\n')" @copy="copyDetail" />
    </template>
  </RequestDetailDrawer>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { EmptyState } from 'nanocat-ui'
import type { RealtimeMonitorRecordDetail } from '@/api/monitor'
import { normalizeImageAttempts } from '@/api/logs'
import RequestDetailDrawer from '@/components/ai/RequestDetailDrawer.vue'
import DetailTextBlock from '@/components/ai/DetailTextBlock.vue'
import StateBadge from '@/components/ai/StateBadge.vue'
import StateBlock from '~/components/ui/StateBlock.vue'
import RequestDetailFields from '@/views/logs/RequestDetailFields.vue'
import LogsImageAttemptTimeline from '@/views/logs/LogsImageAttemptTimeline.vue'
import { writeClipboardText } from '@/lib/clipboard'
import { useToast } from '@/composables/useToast'
import { formatMonitorEventTime } from '@/views/monitor/monitorView'
const props = defineProps<{ open: boolean; record: RealtimeMonitorRecordDetail | null; loading: boolean; error: string }>()
const emit = defineEmits<{ (e: 'close'): void }>()
const events = computed(() => props.record?.events || [])
const attempts = computed(() => normalizeImageAttempts(props.record?.attempts || []))
const toast = useToast()
async function copyDetail(value: string) {
  try { await writeClipboardText(value); toast.success('已复制') }
  catch { toast.error('复制失败') }
}
</script>

<style scoped>
/* CPA account configuration sections: 14px padding, 11px corners, 32% muted surface. */
.call-summary,.call-information { min-width:0; padding:14px; border:1px solid var(--console-border); border-radius:11px; background:color-mix(in srgb,hsl(var(--muted)) 32%,var(--cpa-drawer-surface)); }
.call-summary-heading { display:flex; align-items:flex-start; justify-content:space-between; gap:16px; }
.call-summary-heading > div:first-child { min-width:0; }
.call-summary h2 { margin:10px 0 0; font-size:16px; font-weight:600; overflow-wrap:anywhere; }
.call-duration { display:flex; flex:none; flex-direction:column; align-items:flex-end; gap:4px; font-variant-numeric:tabular-nums; }
.call-duration span { font-size:11px; color:hsl(var(--muted-foreground)); }
.call-duration strong { font-size:28px; font-weight:600; line-height:1.2; }
.call-id { margin-top:12px; font-family:var(--font-mono,monospace); font-size:11px; color:var(--console-text-regular); overflow-wrap:anywhere; user-select:all; }
.call-endpoint { margin-top:4px; font-size:12px; color:hsl(var(--muted-foreground)); overflow-wrap:anywhere; }
h3 { margin:0; font-size:14px; font-weight:600; }
.call-facts { margin:10px 0 0; }
.call-facts > div { display:grid; grid-template-columns:76px minmax(0,1fr); gap:12px; padding:9px 0; border-top:1px solid var(--console-border); font-size:12px; line-height:1.5; }
.call-facts dt { color:hsl(var(--muted-foreground)); }
.call-facts dd { margin:0; overflow-wrap:anywhere; }
.call-section-heading { display:flex; justify-content:space-between; align-items:center; gap:12px; margin-bottom:14px; }
.call-section-heading > span { font-size:11px; color:hsl(var(--muted-foreground)); }
.call-timeline { list-style:none; margin:0; padding:0; }
.call-timeline li { position:relative; display:flex; gap:12px; padding-bottom:14px; }
.call-timeline li:not(:last-child)::before { content:''; position:absolute; top:24px; bottom:0; left:11px; width:1px; background:var(--console-border); }
.call-stage-index { z-index:1; display:flex; align-items:center; justify-content:center; flex:none; width:24px; height:24px; border:1px solid color-mix(in srgb,var(--data-blue-base) 25%,var(--console-border)); border-radius:8px; font-size:10px; font-variant-numeric:tabular-nums; background:color-mix(in srgb,var(--data-blue-base) 8%,var(--cpa-drawer-surface)); color:var(--data-blue-base); }
.call-stage-content { flex:1; min-width:0; padding:2px 0 12px; border-bottom:1px solid var(--console-border); }
.call-timeline li:last-child .call-stage-content { border-bottom:0; }
.call-stage-heading { display:flex; flex-wrap:wrap; align-items:baseline; justify-content:space-between; gap:4px 12px; }
.call-stage-heading strong { font-size:12px; font-weight:600; overflow-wrap:anywhere; }
.call-stage-heading time { font-size:12px; font-weight:700; color:var(--app-text-primary); font-variant-numeric:tabular-nums; white-space:nowrap; }
.call-stage-timing { margin-top:6px; font-size:11px; color:var(--data-blue-base); overflow-wrap:anywhere; }
.call-stage-description { margin-top:4px; font-size:12px; color:var(--console-text-regular); line-height:1.5; white-space:pre-wrap; overflow-wrap:anywhere; }
@media(max-width:640px) { .call-duration strong { font-size:24px; } .call-facts > div { grid-template-columns:64px minmax(0,1fr); gap:8px; } }
</style>

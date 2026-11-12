<template>
  <div
    class="monitor-page cpa-monitor"
    :class="{ 'monitor-page--table': activeDetailPanel !== 'slow' }"
  >
    <section class="monitor-overview-panel">
      <PanelHeader class="monitor-overview-header" title="请求健康状态">
        <template #copy>
          <p class="mt-1 text-xs text-muted-foreground">
            观察请求执行、账号切换与上游响应。最近更新：{{ monitorData?.updated_at || '未获取' }}
          </p>
        </template>
        <template #actions>
          <StateBadge :tone="autoRefresh ? 'success' : 'muted'" shape="rounded" size="xs">
            {{ autoRefresh ? '自动刷新' : '已暂停' }}
          </StateBadge>
          <label class="flex items-center gap-2 text-xs text-muted-foreground">
            <span class="whitespace-nowrap">间隔</span>
            <Input
              :model-value="String(refreshIntervalSeconds)"
              type="number"
              min="1"
              max="300"
              step="1"
              root-class="w-16"
              @update:model-value="setRefreshIntervalInput"
              @blur="applyRefreshInterval()"
              @change="applyRefreshInterval()"
              @keyup.enter="applyRefreshInterval()"
            />
            <span class="whitespace-nowrap">秒</span>
          </label>
          <Button size="sm" variant="outline" :disabled="isLoading" @click="refreshMonitor(false)">
            <Icon icon="lucide:refresh-cw" aria-hidden="true" />
            {{ isLoading ? '刷新中...' : '立即刷新' }}
          </Button>
          <Button size="sm" variant="outline" @click="toggleAutoRefresh">
            <Icon :icon="autoRefresh ? 'lucide:pause' : 'lucide:play'" aria-hidden="true" />
            {{ autoRefresh ? '暂停刷新' : '继续刷新' }}
          </Button>
        </template>
      </PanelHeader>

      <section v-if="overviewGroup" :aria-label="overviewGroup.title">
        <div class="cpa-section-label monitor-section-title"><h2>{{ overviewGroup.title }}</h2><span>{{ overviewGroup.meta }}</span></div>
        <div class="monitor-overview-grid">
          <MetricCard v-for="item in overviewGroup.items" :key="item.key"
            class="monitor-overview-metric" :data-tone="item.tone"
            :label="item.label" :value="item.value" :description="item.meta"
            :icon="overviewIcons[item.key] || 'lucide:activity'" :decoration="false" />
        </div>
      </section>

      <PagePanel v-if="selectedDiagnosticGroup" flush class="monitor-diagnostics-panel">
        <div class="monitor-diagnostics-heading">
          <h2>链路诊断</h2>
          <span>{{ selectedDiagnosticGroup.meta }}</span>
        </div>
        <div class="monitor-diagnostic-tabs">
          <ConsoleSegmentedTabs v-model="activeDiagnosticGroup" :options="diagnosticOptions"
            aria-label="链路诊断分类" fit="content" />
        </div>
        <dl class="monitor-diagnostics-grid" :aria-label="selectedDiagnosticGroup.title">
          <div v-for="item in selectedDiagnosticGroup.items" :key="item.key"
            class="monitor-metric-cell" :data-tone="item.tone" :data-unavailable="item.value === '—'">
            <dt>{{ item.label }}</dt>
            <dd>{{ item.value }}</dd>
            <dd v-if="item.meta" class="monitor-metric-caption">{{ item.meta }}</dd>
          </div>
        </dl>
      </PagePanel>

      <StateBlock
        v-if="loadError"
        compact
        dashed
        title="实时监控加载失败"
        :description="loadError"
      />
    </section>

    <PageLoadingState
      v-if="!monitorData && !loadError"
      title="正在获取实时快照"
      description="正在读取当前进程的活跃请求、完成记录和瓶颈诊断。"
      compact
    />

    <PagePanel v-else-if="monitorData" flush class="monitor-detail-panel"
      :class="{ 'monitor-detail-panel--empty': detailPanelEmpty, 'monitor-detail-panel--slow': activeDetailPanel === 'slow' }">
      <div class="monitor-detail-tabs">
        <ConsoleSegmentedTabs
          v-model="activeDetailPanel"
          :options="detailPanelOptions"
          aria-label="实时监控明细"
          fit="content"
        />
      </div>

      <section v-if="activeDetailPanel === 'active'" class="monitor-detail-section">
        <div class="monitor-detail-header">
          <PanelHeader title="活跃请求" align="start">
            <template #copy>
              <p class="mt-1 text-xs text-muted-foreground">
                正在运行的请求，按进入时间排序。
              </p>
            </template>
            <template #actions>
              <MetaChip size="xs" tone="info">当前并发 {{ activeRows.length }} / {{ executionCapacity }}</MetaChip>
              <MetaChip size="xs" tone="muted">{{ entryQueueText }}</MetaChip>
              <MetaChip v-if="monitorData?.tracking_dropped" size="xs" tone="warning">超出跟踪容量 {{ monitorData.tracking_dropped }}</MetaChip>
            </template>
          </PanelHeader>
        </div>
        <div v-if="activeStageItems.length" class="flex flex-wrap gap-2 px-4 pb-3">
          <MetaChip
            v-for="item in activeStageItems"
            :key="item.label"
            size="xs"
            tone="muted"
          >
            {{ item.label }} {{ item.count }}
          </MetaChip>
        </div>
        <TableShell
          fill
          scroll-mode="contained"
          hover-rows
          unframed
          sticky-header
          :show-empty="activeRows.length === 0"
          :empty-colspan="6"
          empty-title="暂无活跃请求"
          empty-description="开始压测或发起图片请求后，这里会实时出现运行中的调用。"
          table-class="monitor-table"
        >
          <template #head>
            <tr>
              <th class="monitor-request-column">请求 / 模型</th>
              <th class="monitor-status-column">当前阶段</th>
              <th class="monitor-duration-column">已耗时</th>
              <th>关键耗时</th>
              <th class="monitor-account-column">账号 / 出口</th>
              <th class="monitor-action-column"><span class="sr-only">详情</span></th>
            </tr>
          </template>
          <MonitorRequestRow
            active
            v-for="row in activeRows"
            :key="row.call_id"
            :row="row"
            :signature="activeRowSignature(row)"
            @open-detail="openDetail"
          />
        </TableShell>
      </section>

      <section v-else-if="activeDetailPanel === 'recent'" class="monitor-detail-section">
        <div class="monitor-detail-header">
          <PanelHeader title="最近完成" align="start">
            <template #copy>
              <p class="mt-1 text-xs text-muted-foreground">
                最近完成的调用，窗口保存在进程内存中。
              </p>
            </template>
            <template #actions>
              <MetaChip size="xs" tone="muted">{{ completedWindowText }}</MetaChip>
            </template>
          </PanelHeader>
        </div>
        <TableShell
          fill
          scroll-mode="contained"
          hover-rows
          unframed
          sticky-header
          :show-empty="recentRows.length === 0"
          :empty-colspan="6"
          empty-title="暂无完成记录"
          empty-description="当前进程启动后还没有请求完成。"
          table-class="monitor-table"
        >
          <template #head>
            <tr>
              <th class="monitor-request-column">请求 / 模型</th>
              <th class="monitor-status-column">状态</th>
              <th class="monitor-duration-column">总耗时</th>
              <th>关键耗时</th>
              <th class="monitor-account-column">账号 / 出口</th>
              <th class="monitor-action-column"><span class="sr-only">详情</span></th>
            </tr>
          </template>
          <MonitorRequestRow
            v-for="row in recentRows"
            :key="`recent-${row.call_id}-${row.ended_at}`"
            :row="row"
            :signature="recentRowSignature(row)"
            @open-detail="openDetail"
          />
        </TableShell>
      </section>

      <section v-else-if="activeDetailPanel === 'slow'" class="monitor-detail-section">
        <div class="monitor-detail-header">
          <PanelHeader title="慢请求" align="start">
            <template #copy>
              <p class="mt-1 text-xs text-muted-foreground">
                活跃请求及完成窗口内总耗时至少 120 秒的请求，按总耗时排序。
              </p>
            </template>
          </PanelHeader>
        </div>
        <div
          v-if="slowRows.length"
          class="monitor-detail-card-list scrollbar-slim px-4 pb-4"
        >
          <MonitorSlowCard
            v-for="row in slowRows"
            :key="`slow-${row.call_id}-${row.ended_at}`"
            :row="row"
            :signature="slowRowSignature(row)"
            @open-detail="openDetail"
          />
        </div>
        <div v-else class="monitor-detail-empty px-4 pb-4">
          <EmptyState plain title="暂无慢请求" description="窗口内没有可排序的完成请求。" />
        </div>
      </section>

    </PagePanel>

    <MonitorDetailDrawer
      :open="detailOpen"
      :record="detailRecord"
      :loading="detailLoading"
      :error="detailError"
      @close="closeDetail"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, shallowRef } from 'vue'
import { Icon } from '@iconify/vue'
import MetricCard from '~/components/MetricCard.vue'
import { Button, EmptyState, Input, TableShell } from 'nanocat-ui'
import type { SegmentedOption, SegmentedValue } from 'nanocat-ui'
import {
  monitorApi,
  type RealtimeMonitorResponse,
} from '@/api/monitor'
import ConsoleSegmentedTabs from '@/components/ai/ConsoleSegmentedTabs.vue'
import MetaChip from '@/components/ai/MetaChip.vue'
import PagePanel from '~/components/ui/PagePanel.vue'
import PanelHeader from '~/components/ui/PanelHeader.vue'
import PageLoadingState from '@/components/ai/PageLoadingState.vue'
import StateBadge from '@/components/ai/StateBadge.vue'
import StateBlock from '~/components/ui/StateBlock.vue'
import { usePageQuery, useSerialVisibilityPolling } from '@/composables/usePageQuery'
import { usePageRuntime } from '@/composables/usePageRuntime'
import MonitorRequestRow from './monitor/MonitorRequestRow.vue'
import MonitorDetailDrawer from './monitor/MonitorDetailDrawer.vue'
import MonitorSlowCard from './monitor/MonitorSlowCard.vue'
import { useMonitorDetailRuntime } from '@/views/monitor/monitorDetailRuntime'
import {
  activeRowSignature,
  recentRowSignature,
  slowRowSignature,
} from '@/views/monitor/monitorView'

defineOptions({ name: 'Monitor' })

const monitorData = shallowRef<RealtimeMonitorResponse | null>(null)
const isLoading = ref(false)
const loadError = ref('')
const autoRefresh = ref(true)
const REFRESH_INTERVAL_STORAGE_KEY = 'chatgpt2api_monitor_refresh_interval_secs'
const DEFAULT_REFRESH_INTERVAL_SECONDS = 5
const MIN_REFRESH_INTERVAL_SECONDS = 1
const MAX_REFRESH_INTERVAL_SECONDS = 300
const refreshIntervalSeconds = ref(readStoredRefreshInterval())
const pageRuntime = usePageRuntime('monitor')
const REFRESH_REQUEST_KEY = 'monitor:refresh'
const POLL_TIMER_KEY = 'monitor:poll'
const monitorQuery = usePageQuery({
  runtime: pageRuntime,
  key: REFRESH_REQUEST_KEY,
  loading: isLoading,
  error: loadError,
  errorMessage: 'Request failed',
})
const monitorDetail = useMonitorDetailRuntime({
  loadDetail: callId => monitorApi.detail(callId),
})
const {
  detailOpen,
  detailLoading,
  detailError,
  detailRecord,
  openDetail,
  closeDetail,
} = monitorDetail
const monitorPolling = useSerialVisibilityPolling({
  runtime: pageRuntime,
  key: POLL_TIMER_KEY,
  intervalMs: () => normalizedRefreshIntervalSeconds() * 1000,
  enabled: () => autoRefresh.value,
  action: () => refreshMonitor(true, 'auto'),
})

const activeRows = computed(() => monitorData.value?.active || [])
const recentRows = computed(() => monitorData.value?.recent.slice(0, 20) || [])
const slowRows = computed(() => monitorData.value?.slow.slice(0, 8) || [])
const activeDetailPanel = ref<SegmentedValue>('active')
const detailPanelOptions = computed<SegmentedOption[]>(() => [
  { value: 'active', label: '活跃请求', count: activeRows.value.length },
  { value: 'recent', label: '最近完成', count: recentRows.value.length },
  { value: 'slow', label: '慢请求', count: slowRows.value.length },
])
const executionCapacity = computed(() => monitorData.value?.execution_capacity?.total || '—')
const detailPanelEmpty = computed(() => (activeDetailPanel.value === 'active' ? activeRows.value : activeDetailPanel.value === 'recent' ? recentRows.value : slowRows.value).length === 0)
const completedWindowText = computed(() => monitorData.value?.completed_window_text || '窗口 0 / 0')
const activeStageItems = computed(() => monitorData.value?.active_stage_items || [])
const entryQueueText = computed(() => monitorData.value?.entry_queue_text || '-')
const diagnosticGroups = computed(() => monitorData.value?.diagnostic_groups || [])
const overviewGroup = computed(() => diagnosticGroups.value.find(group => group.key === 'overview'))
const diagnosticOptions = computed<SegmentedOption[]>(() => diagnosticGroups.value
  .filter(group => group.key !== 'overview')
  .map(group => ({ value: group.key, label: group.title })))
const activeDiagnosticGroup = ref<SegmentedValue>('entry_egress')
const selectedDiagnosticGroup = computed(() => diagnosticGroups.value.find(group => group.key === activeDiagnosticGroup.value)
  || diagnosticGroups.value.find(group => group.key !== 'overview'))
const overviewIcons: Record<string, string> = {
  active: 'lucide:activity', completed: 'lucide:layers', success: 'lucide:circle-check',
  failed: 'lucide:circle-x', text_review: 'lucide:file-text', success_rate: 'lucide:chart-no-axes-combined',
  slow_total: 'lucide:clock', rate_limited: 'lucide:shield-alert',
}

async function loadMonitor(silent = true, source: 'auto' | 'manual' = silent ? 'auto' : 'manual') {
  const autoRequest = source === 'auto'
  if (autoRequest && (!pageRuntime.canRun.value || !autoRefresh.value)) return
  if (isLoading.value && silent) return
  await monitorQuery.run(
    () => monitorApi.realtime(),
    {
      apply: (data) => {
        if (autoRequest && !autoRefresh.value) return
        monitorData.value = data
        loadError.value = ''
      },
      silentLoading: silent,
    },
  )
}

async function refreshMonitor(
  silent = true,
  source: 'auto' | 'manual' = silent ? 'auto' : 'manual',
) {
  await Promise.all([
    loadMonitor(silent, source),
    monitorDetail.refreshIfRunning(),
  ])
}

function startPolling() {
  monitorPolling.start()
}

function stopPolling() {
  monitorPolling.stop()
  monitorQuery.invalidate()
}

function activateMonitor(refresh = false) {
  if (refresh) {
    void refreshMonitor(false, 'manual')
  }
  startPolling()
}

function deactivateMonitor() {
  isLoading.value = false
  stopPolling()
}

function handleVisibilityChange() {
  startPolling()
  if (autoRefresh.value) {
    void refreshMonitor(true, 'auto')
  }
}

function toggleAutoRefresh() {
  autoRefresh.value = !autoRefresh.value
  if (autoRefresh.value) {
    applyRefreshInterval(false)
    startPolling()
    void refreshMonitor(true, 'auto')
  } else {
    stopPolling()
  }
}

function clampRefreshInterval(value: unknown) {
  const seconds = Math.round(Number(value || DEFAULT_REFRESH_INTERVAL_SECONDS))
  if (!Number.isFinite(seconds)) return DEFAULT_REFRESH_INTERVAL_SECONDS
  return Math.min(MAX_REFRESH_INTERVAL_SECONDS, Math.max(MIN_REFRESH_INTERVAL_SECONDS, seconds))
}

function readStoredRefreshInterval() {
  try {
    return clampRefreshInterval(window.localStorage.getItem(REFRESH_INTERVAL_STORAGE_KEY))
  } catch {
    return DEFAULT_REFRESH_INTERVAL_SECONDS
  }
}

function normalizedRefreshIntervalSeconds() {
  return clampRefreshInterval(refreshIntervalSeconds.value)
}

function setRefreshIntervalInput(value: unknown) {
  refreshIntervalSeconds.value = clampRefreshInterval(value)
}

function applyRefreshInterval(restart = true) {
  const nextValue = normalizedRefreshIntervalSeconds()
  refreshIntervalSeconds.value = nextValue
  try {
    window.localStorage.setItem(REFRESH_INTERVAL_STORAGE_KEY, String(nextValue))
  } catch {
    // ignore storage errors
  }
  if (restart && autoRefresh.value) {
    startPolling()
  }
}

pageRuntime.onActivate(({ initial }) => {
  activateMonitor(true)
})

pageRuntime.onShow(() => {
  handleVisibilityChange()
})

pageRuntime.onHide(() => {
  deactivateMonitor()
})

pageRuntime.onDeactivate(() => {
  deactivateMonitor()
  closeDetail()
})
</script>

<style scoped>
:deep(.monitor-table) { width:100%; table-layout:fixed; border-collapse:collapse; text-align:left; }
:deep(.monitor-table th) { padding:10px 12px; background:var(--app-surface-muted); border-bottom:1px solid var(--console-border); color:var(--console-text-regular); font-size:11px; font-weight:600; }
:deep(.monitor-request-column) { width:24%; }
:deep(.monitor-status-column) { width:112px; }
:deep(.monitor-duration-column) { width:104px; }
:deep(.monitor-account-column) { width:28%; }
:deep(.monitor-action-column) { width:52px; }
:deep(.table-shell__scroll) { overscroll-behavior:contain; }
@media(max-width:760px) {
  :deep(.monitor-table),:deep(.monitor-table tbody) { display:block; min-width:0; }
  :deep(.monitor-table thead) { display:none; }
  :deep(.table-shell__state-row),:deep(.table-shell__state-row td) { display:block; width:100%; }
}

/* CPA metric-card and summary-cell geometry; backend owns all diagnostic values. */
.monitor-page,.monitor-overview-panel { display:flex; flex-direction:column; gap:20px; min-width:0; }
.monitor-page--table { min-height:calc(100dvh - var(--console-header-height) - 2 * var(--console-gap)); }
.monitor-overview-panel { flex:none; }
.monitor-overview-header { padding:16px 24px; border:1px solid var(--console-glass-border); border-radius:12px; background:var(--console-glass); box-shadow:0 4px 12px rgb(0 0 0 / .05); }
.monitor-section-title { margin-bottom:12px; }
.monitor-overview-grid { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:12px; }
.monitor-overview-metric { --metric-accent:var(--data-slate-base); }
.monitor-overview-metric[data-tone="success"],.monitor-metric-cell[data-tone="success"] { --metric-accent:var(--data-green-base); }
.monitor-overview-metric[data-tone="danger"],.monitor-metric-cell[data-tone="danger"] { --metric-accent:var(--data-red-base); }
.monitor-overview-metric[data-tone="warning"],.monitor-metric-cell[data-tone="warning"] { --metric-accent:var(--data-amber-base); }
.monitor-overview-metric[data-tone="info"],.monitor-metric-cell[data-tone="info"] { --metric-accent:var(--data-blue-base); }
.monitor-overview-metric:not([data-tone="muted"]) :deep(strong) { color:var(--metric-accent); }
.monitor-diagnostics-heading { display:flex; align-items:baseline; flex-wrap:wrap; gap:8px 16px; padding:16px 20px 12px; }
.monitor-diagnostics-heading h2 { font-size:16px; font-weight:600; }
.monitor-diagnostics-heading > span { font-size:12px; color:var(--console-text-regular); }
.monitor-diagnostic-tabs { padding:0 20px 16px; min-width:0; }
.monitor-diagnostics-grid { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); margin:0; padding:0 20px 12px; gap:0 24px; }
.monitor-metric-cell { --metric-accent:hsl(var(--foreground)); display:grid; grid-template-columns:minmax(0,1fr) auto; align-content:center; align-items:baseline; gap:5px 12px; min-width:0; min-height:88px; padding:14px 0; border-top:1px solid var(--console-border); }
.monitor-metric-cell dt { font-size:12px; color:var(--console-text-regular); overflow-wrap:anywhere; }
.monitor-metric-cell dd:not(.monitor-metric-caption) { font-size:20px; font-weight:600; font-variant-numeric:tabular-nums; color:var(--metric-accent); }
.monitor-metric-cell .monitor-metric-caption { grid-column:1 / -1; font-size:11px; color:hsl(var(--muted-foreground)); overflow-wrap:anywhere; }
.monitor-metric-cell[data-unavailable="true"] dd:not(.monitor-metric-caption) { color:hsl(var(--muted-foreground)); }
.monitor-detail-panel { display:flex; flex:1 0 32rem; flex-direction:column; min-width:0; height:32rem; min-height:0; overflow:hidden; }
.monitor-detail-panel--slow { flex:0 0 auto; height:auto; min-height:0; }
.monitor-detail-panel--slow.monitor-detail-panel--empty { min-height:20rem; }
.monitor-detail-tabs { flex:none; min-width:0; border-bottom:1px solid var(--console-border); padding:12px 20px; }
.monitor-detail-header { padding:16px 20px; flex:none; }
.monitor-detail-section { display:flex; min-height:0; flex:1; flex-direction:column; overflow:hidden; }
.monitor-detail-card-list { display:grid; grid-template-columns:repeat(auto-fit,minmax(min(100%,440px),1fr)); align-content:start; gap:12px; padding-top:2px; min-height:0; max-height:65dvh; overflow-y:auto; scrollbar-gutter:stable; }
.monitor-detail-empty { display:flex; min-height:0; flex:1; align-items:center; justify-content:center; }
/* Empty states do not inherit the data table's desktop minimum width. */
:deep(.table-shell--state .monitor-table) { min-width:0; }
:deep(.table-shell--state thead) { display:none; }
@media(min-width:1800px) {
  .monitor-overview-grid { grid-template-columns:repeat(8,minmax(0,1fr)); }
  .monitor-diagnostics-grid { grid-template-columns:repeat(8,minmax(0,1fr)); }
  .monitor-metric-cell { grid-template-columns:minmax(0,1fr); gap:8px; }
  .monitor-metric-cell dd:not(.monitor-metric-caption) { font-size:24px; }
}
@media(max-width:1100px) { .monitor-diagnostics-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } }
@media(max-width:640px) {
  .monitor-page--table { min-height:calc(100dvh - var(--console-header-height) - 24px); }
  .monitor-page,.monitor-overview-panel { gap:16px; }
  .monitor-overview-header { padding:16px; }
  .monitor-overview-grid { grid-template-columns:repeat(2,minmax(0,1fr)); gap:8px; }
  .monitor-diagnostics-heading { padding:16px 16px 12px; }
  .monitor-diagnostic-tabs { padding:0 16px 12px; }
  .monitor-diagnostics-grid { padding:0 16px 8px; gap:0 16px; }
  .monitor-metric-cell { grid-template-columns:minmax(0,1fr); gap:6px; }
  .monitor-metric-cell dd:not(.monitor-metric-caption) { font-size:22px; }
  .monitor-detail-header { padding:16px; }
  .monitor-detail-tabs { padding:12px; }
  .monitor-detail-card-list { grid-template-columns:minmax(0,1fr); }
}
</style>

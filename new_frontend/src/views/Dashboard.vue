<template>
  <div class="cpa-dashboard">
    <PageLoadingState
      v-if="!dashboardDataReady && !dashboardLoadError"
      title="正在加载概览"
      description="读取最新账号和调用数据。"
    />

    <StateBlock
      v-else-if="!dashboardDataReady"
      title="概览加载失败"
      :description="dashboardLoadError"
    >
      <Button size="sm" variant="outline" root-class="mt-4" @click="retryDashboard">
        重新加载
      </Button>
    </StateBlock>

    <template v-else>
    <div
      v-if="dashboardDataWarning"
      class="flex items-center gap-2 rounded-md border border-amber-300/70 bg-amber-50 px-3 py-2 text-sm text-amber-800 dark:border-amber-700/70 dark:bg-amber-950/30 dark:text-amber-200"
      role="status"
    >
      <Icon icon="lucide:triangle-alert" class="h-4 w-4 shrink-0" />
      <span>{{ dashboardDataWarning }}</span>
    </div>

    <div class="cpa-dashboard-header">
      <div class="cpa-dashboard-status"><span class="cpa-status-dot" aria-hidden="true"></span><div><span>控制台版本</span><strong>{{ dashboardVersion }}</strong></div></div>
      <div class="cpa-dashboard-endpoint"><span>API Base</span><strong :title="apiBaseUrl">{{ apiBaseUrl }}</strong></div>
      <div class="cpa-dashboard-actions">
        <Button size="sm" variant="outline" @click="retryDashboard"><Icon icon="lucide:refresh-cw" aria-hidden="true" />刷新</Button>
        <RouterLink class="ui-btn ui-btn-sm ui-btn-outline cpa-settings-shortcut" to="/settings" aria-label="系统配置"><Icon icon="lucide:settings-2" aria-hidden="true" /></RouterLink>
      </div>
    </div>
    <section class="cpa-runtime">
      <PagePanel class="cpa-resource-panel">
        <PanelHeader title="资源占用" align="start" />
          <section class="cpa-resource-content" aria-label="资源占用">
            <div class="cpa-resource-grid">
              <div
                v-for="metric in runtimeResourceMetrics"
                :key="metric.label"
                class="cpa-resource-item" :style="{ '--resource-accent': metric.color }"
              >
                <div class="cpa-resource-heading">
                  <span class="cpa-resource-icon"><Icon :icon="metric.icon" aria-hidden="true" /></span>
                  <span>{{ metric.label }}</span>
                </div>
                <strong class="cpa-resource-value">{{ metric.value }}</strong>
                <div class="cpa-resource-meter" :role="metric.progress === null ? undefined : 'meter'"
                  :aria-label="`${metric.label}占用率`" :aria-valuenow="boundedPercent(metric.progress)"
                  :aria-valuemin="0" :aria-valuemax="100">
                  <span v-if="metric.progress !== null" :style="{ width: `${boundedPercent(metric.progress)}%` }" />
                </div>
                <span class="cpa-resource-caption">{{ metric.caption }}</span>
              </div>
            </div>
          </section>
      </PagePanel>
      <PagePanel class="cpa-system-panel">
        <PanelHeader title="系统概览" align="start" />
          <section
            class="mt-4 min-w-0"
            aria-label="运行信息"
          >
            <dl class="grid min-w-0 grid-cols-1 gap-x-6 sm:grid-cols-2">
              <div
                v-for="detail in runtimeDetails"
                :key="detail.label"
                class="flex min-w-0 items-center justify-between gap-3 border-b border-border/50 py-2 first:pt-0 sm:[&:nth-child(2)]:pt-0"
              >
                <dt class="flex min-w-0 shrink-0 items-center gap-2 text-xs text-muted-foreground">
                  <Icon :icon="detail.icon" class="h-4 w-4 shrink-0" aria-hidden="true" />
                  <span class="truncate">{{ detail.label }}</span>
                </dt>
                <dd class="min-w-0 truncate text-right text-sm font-medium tabular-nums" :title="detail.value">{{ detail.value }}</dd>
              </div>
            </dl>
          </section>
      </PagePanel>
    </section>

    <div class="cpa-section-label"><h2>账号资源</h2><span>账号池概况</span></div>
    <section
      aria-label="账号概览"
      class="cpa-metrics-grid"
    >
      <MetricCard
        v-for="(stat, index) in accountStats"
        :key="stat.label"
        :label="stat.label"
        :value="stat.value"
        :icon="stat.icon"
        :icon-tone="accountMetricTones[index]"
      />
    </section>

    <div class="cpa-section-label"><h2>调用概览</h2><span>最近 24 小时</span></div>
    <section
      aria-label="调用概览"
      class="cpa-metrics-grid"
    >
      <MetricCard
        v-for="stat in callStats"
        :key="stat.label"
        :label="stat.label"
        :value="stat.value"
        :icon="stat.icon"
        :icon-tone="stat.iconTone"
      />
    </section>

    <section class="cpa-charts">
      <ChartCard root-class="cpa-chart-card" title="模型请求分布">
        <template #title-extra>
          <HelpTip text="仅统计成功及部分成功请求。" />
        </template>
        <template #actions>
          <TimeRangeTabs v-model="modelTimeRange" aria-label="模型请求分布时间范围" />
        </template>
        <DashboardChart kind="models" title="模型请求分布" :range="dashboardRanges?.[modelTimeRange]" :model-names="chartModelNames" />
      </ChartCard>
      <ChartCard root-class="cpa-chart-card" title="调用结果趋势">
        <template #actions>
          <TimeRangeTabs v-model="trendTimeRange" aria-label="调用结果趋势时间范围" />
        </template>
        <DashboardChart kind="outcomes" title="调用结果趋势" :range="dashboardRanges?.[trendTimeRange]" :model-names="chartModelNames" />
      </ChartCard>

      <ChartCard root-class="cpa-chart-card" title="调用活跃度">
        <template #actions>
          <TimeRangeTabs v-model="activityTimeRange" aria-label="调用活跃度时间范围" />
        </template>
        <div class="cpa-activity">
          <div
            v-if="activityHasData"
            class="cpa-activity-content"
          >
            <div class="cpa-activity-summary">
              <div><span>累计调用</span><strong>{{ formatCount(dashboardRanges?.[activityTimeRange]?.totals.total) }}<small>次</small></strong></div>
              <div><span>成功率</span><strong class="cpa-activity-rate">{{ formatPercent(dashboardRanges?.[activityTimeRange]?.totals.success_rate) }}</strong></div>
            </div>
            <p class="cpa-activity-caption">{{ activityTimeRange === '24h' ? '每格代表 1 小时' : '每格代表 1 天' }} · 颜色越深，调用越多</p>
            <div
              :key="`${activityAnimationEpoch}-${activityTimeRange}`"
              class="cpa-activity-grid"
              :style="{ gridTemplateColumns: `repeat(${activityColumnCount}, minmax(0, 1fr))` }"
            >
              <span
                v-for="(bucket, index) in activityBuckets"
                :key="`${activityAnimationEpoch}-${activityTimeRange}-${bucket.start_at}`"
                class="dashboard-activity-cell grid min-w-0"
                :style="{ animationDelay: `${Math.min(index * 2, 32)}ms` }"
              >
                <HoverCard card-class="w-52" :offset="8" focusable>
                  <span
                    class="dashboard-activity-cell-trigger"
                    :class="activityCellClass(bucket.total_calls)"
                    role="img"
                    :aria-label="activityTooltip(bucket)"
                  ></span>
                  <template #content>
                    <div class="mb-2 text-xs font-semibold text-foreground">{{ bucket.label }}</div>
                    <div class="grid gap-1.5 text-xs">
                      <div
                        v-for="item in activityTooltipItems(bucket)"
                        :key="item.label"
                        class="flex items-center justify-between gap-4"
                      >
                        <span class="inline-flex items-center gap-1.5 text-muted-foreground">
                          <span class="h-2 w-2 rounded-full" :class="item.markerClass"></span>
                          {{ item.label }}
                        </span>
                        <span class="font-semibold text-foreground">{{ item.value }}</span>
                      </div>
                    </div>
                  </template>
                </HoverCard>
              </span>
            </div>
            <div class="cpa-activity-axis">
              <span>{{ activityBuckets[0]?.label || '--' }}</span>
              <span>{{ activityBuckets[activityBuckets.length - 1]?.label || '--' }}</span>
            </div>
            <div class="cpa-activity-footer">
              <span>成功 {{ formatCount(dashboardRanges?.[activityTimeRange]?.totals.success) }} · 失败 {{ formatCount(dashboardRanges?.[activityTimeRange]?.totals.final_failed) }}</span>
              <span class="cpa-activity-scale">
                <span>少</span>
                <i v-for="level in 5" :key="level" :class="`activity-level-${level - 1}`" />
                <span>多</span>
              </span>
            </div>
          </div>
          <p v-else class="w-full text-center text-sm text-muted-foreground" role="status">
            当前范围内暂无可统计请求
          </p>
        </div>
      </ChartCard>

      <ChartCard root-class="cpa-chart-card" title="成功耗时趋势">
        <template #actions>
          <TimeRangeTabs v-model="responseTimeTimeRange" aria-label="成功耗时趋势时间范围" />
        </template>
        <DashboardChart kind="duration" title="成功耗时趋势" :range="dashboardRanges?.[responseTimeTimeRange]" :model-names="chartModelNames" />
      </ChartCard>

      <ChartCard root-class="cpa-chart-card" title="模型耗时">
        <template #actions>
          <TimeRangeTabs v-model="modelResponseTimeTimeRange" aria-label="模型耗时时间范围" />
        </template>
        <DashboardChart kind="model-duration" title="模型耗时" :range="dashboardRanges?.[modelResponseTimeTimeRange]" :model-names="chartModelNames" />
      </ChartCard>
    </section>

    </template>
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { RouterLink } from 'vue-router'
import { usePublicRuntimeConfig } from '@/composables/usePublicRuntimeConfig'
import { Button, ChartCard, HelpTip, HoverCard } from 'nanocat-ui'
import MetricCard from '~/components/MetricCard.vue'
import { Icon } from '@iconify/vue'
import PageLoadingState from '@/components/ai/PageLoadingState.vue'
import PagePanel from '~/components/ui/PagePanel.vue'
import PanelHeader from '~/components/ui/PanelHeader.vue'
import StateBlock from '~/components/ui/StateBlock.vue'
import TimeRangeTabs from '@/components/ai/TimeRangeTabs.vue'
import { formatRequestDuration } from '@/lib/requestDuration'
import type { DashboardBucket } from '@/types/api'
import { useDashboardPage } from '@/views/dashboard/useDashboardPage'
import DashboardChart from './dashboard/DashboardChart.vue'

defineOptions({ name: 'Dashboard' })
const { apiBaseUrl } = usePublicRuntimeConfig()

const {
  stats: accountStats,
  dashboardRanges,
  dashboardRuntime,
  dashboardOperations,
  dashboardDataReady,
  dashboardLoadError,
  dashboardDataWarning,
  dashboardVersion,
  retryDashboard,
  modelTimeRange,
  trendTimeRange,
  activityTimeRange,
  activityAnimationEpoch,
  responseTimeTimeRange,
  modelResponseTimeTimeRange,
  activityBuckets,
  activityHasData,
} = useDashboardPage()

// Share the data/polling owner, but bind only CPA chart components to the DOM.
// Presentation follows the shared six-item account summary order.
const accountMetricTones = ['info', 'success', 'warning', 'error', 'neutral', 'info'] as const
const chartModelNames = computed(() => [...new Set(Object.values(dashboardRanges.value ?? {}).flatMap(range => [
  ...Object.keys(range.trend.model_success_requests), ...Object.keys(range.trend.model_avg_success_duration_ms),
]))].sort())

const callStats = computed(() => {
  const range = dashboardRanges.value?.['24h']
  const totals = range?.totals
  const switching = range?.switching
  return [
    {
      label: '当前并发',
      value: formatCount(dashboardOperations.value?.active_requests),
      icon: 'lucide:layers-3',
      iconTone: 'info' as const,
    },
    {
      label: '总调用量',
      value: formatCount(totals?.total),
      icon: 'lucide:messages-square',
      iconTone: 'neutral' as const,
    },
    {
      label: '成功率',
      value: formatPercent(totals?.success_rate),
      icon: 'lucide:circle-check',
      iconTone: 'success' as const,
    },
    {
      label: '平均成功耗时',
      value: formatRequestDuration(totals?.avg_success_duration_ms) || '--',
      icon: 'lucide:timer',
      iconTone: 'info' as const,
    },
    {
      label: '触发切号',
      value: formatCount(switching?.requests),
      icon: 'lucide:repeat-2',
      iconTone: 'warning' as const,
    },
    {
      label: '切换恢复率',
      value: formatPercent(switching?.recovery_rate),
      icon: 'lucide:shield-check',
      iconTone: 'success' as const,
    },
  ]
})

const activityMaxCalls = computed(() => Math.max(
  0,
  ...activityBuckets.value.map((bucket) => bucket.total_calls),
))
const activityColumnCount = computed(() => {
  if (activityTimeRange.value === '24h') return 12
  if (activityTimeRange.value === '7d') return 7
  return 10
})

const runtimeResourceMetrics = computed(() => {
  const runtime = dashboardRuntime.value
  return [
    {
      label: '应用 CPU',
      value: formatPercent(runtime?.process_cpu_percent),
      icon: 'lucide:cpu',
      color: 'var(--data-blue-base)',
      caption: '应用进程 · CPU 使用率',
      progress: runtime?.process_cpu_percent ?? null,
    },
    {
      label: '应用内存',
      value: formatBytes(runtime?.process_memory_bytes),
      icon: 'lucide:memory-stick',
      color: 'var(--data-violet-base)',
      caption: '占可见内存 ' + formatPercent(runtime?.process_memory_percent),
      progress: runtime?.process_memory_percent ?? null,
    },
    {
      label: memoryScopeLabel(runtime?.memory_scope),
      value: formatPercent(runtime?.memory_percent),
      icon: 'lucide:memory-stick',
      color: 'var(--data-amber-base)',
      caption: memoryScopeLabel(runtime?.memory_scope) + ' · 使用率',
      progress: runtime?.memory_percent ?? null,
    },
    {
      label: '数据盘',
      value: formatPercent(runtime?.storage_percent),
      icon: 'lucide:hard-drive',
      color: 'var(--data-slate-base)',
      caption: '数据目录所在磁盘 · 使用率',
      progress: runtime?.storage_percent ?? null,
    },
  ]
})

const runtimeDetails = computed(() => {
  const runtime = dashboardRuntime.value
  return [
    {
      label: '运行方式',
      value: runtime
        ? runtime.runtime_label || (runtime.runtime_mode === 'docker'
          ? 'Docker'
          : `Python ${runtime.python_version}`)
        : '--',
      icon: 'lucide:server-cog',
    },
    { label: '应用版本', value: dashboardVersion.value, icon: 'lucide:tag' },
    {
      label: '实例名称',
      value: runtime?.instance_name || '--',
      icon: 'lucide:hard-drive',
    },
    {
      label: '系统发行版',
      value: runtime?.distribution || '--',
      icon: 'lucide:binary',
    },
    {
      label: '内核 / 架构',
      value: runtime ? `${runtime.kernel_version} / ${runtime.architecture}` : '--',
      icon: 'lucide:binary',
    },
    {
      label: 'CPU 容量',
      value: formatCpuCapacity(runtime?.cpu_capacity),
      icon: 'lucide:cpu',
    },
    {
      label: '服务启动',
      value: formatDateTime(runtime?.service_started_at),
      icon: 'lucide:clock-3',
    },
    {
      label: '入站速率',
      value: formatRate(runtime?.network_rx_bytes_per_sec),
      icon: 'lucide:arrow-down-to-line',
    },
    {
      label: '服务运行',
      value: formatUptime(runtime?.service_uptime_seconds),
      icon: 'lucide:timer',
    },
    {
      label: '出站速率',
      value: formatRate(runtime?.network_tx_bytes_per_sec),
      icon: 'lucide:arrow-up-from-line',
    },
  ]
})

function boundedPercent(value: number | null) {
  return value === null ? undefined : Math.min(100, Math.max(0, value))
}

function formatCount(value: number | null | undefined) {
  if (value === null || value === undefined || !Number.isFinite(value)) return '--'
  return Math.max(0, Math.trunc(value)).toLocaleString('zh-CN')
}

function formatPercent(value: number | null | undefined) {
  return value === null || value === undefined ? '--' : `${value.toFixed(1)}%`
}

function formatBytes(value: number | null | undefined) {
  if (value === null || value === undefined || !Number.isFinite(value)) return '--'
  if (value < 1_024) return `${Math.round(value)} B`
  if (value < 1_024 ** 2) return `${(value / 1_024).toFixed(1)} KB`
  if (value < 1_024 ** 3) return `${(value / 1_024 ** 2).toFixed(1)} MB`
  if (value < 1_024 ** 4) return `${(value / 1_024 ** 3).toFixed(1)} GB`
  return `${(value / 1_024 ** 4).toFixed(1)} TB`
}

function formatRate(value: number | null | undefined) {
  if (value === null || value === undefined) return '--'
  if (value < 1_000) return `${Math.round(value)} B/s`
  if (value < 1_000_000) return `${(value / 1_000).toFixed(1)} KB/s`
  return `${(value / 1_000_000).toFixed(1)} MB/s`
}

function formatCpuCapacity(value: number | null | undefined) {
  if (value === null || value === undefined || !Number.isFinite(value) || value <= 0) return '--'
  const digits = Number.isInteger(value) ? 0 : 2
  return `${value.toLocaleString('zh-CN', { maximumFractionDigits: digits })} 核`
}

function memoryScopeLabel(scope: 'container' | 'system' | 'visible' | undefined) {
  if (scope === 'container') return '容器内存'
  if (scope === 'system') return '系统内存'
  return '可见内存'
}

function formatUptime(value: number | null | undefined) {
  if (value === null || value === undefined) return '--'
  const seconds = Math.max(0, Math.trunc(value))
  const days = Math.floor(seconds / 86_400)
  const hours = Math.floor((seconds % 86_400) / 3_600)
  const minutes = Math.floor((seconds % 3_600) / 60)
  if (days > 0) return `${days}天 ${hours}小时`
  if (hours > 0) return `${hours}小时 ${minutes}分`
  return `${minutes}分`
}

function formatDateTime(value: string | null | undefined) {
  if (!value) return '--'
  return value.replace('T', ' ').replace(/([+-]\d\d:\d\d|Z)$/, '')
}

function activityCellClass(value: number) {
  const maximum = activityMaxCalls.value
  if (value <= 0 || maximum <= 0) return 'activity-level-0'
  const ratio = Math.log1p(value) / Math.log1p(maximum)
  if (ratio <= 0.25) return 'activity-level-1'
  if (ratio <= 0.5) return 'activity-level-2'
  if (ratio <= 0.75) return 'activity-level-3'
  return 'activity-level-4'
}

function activityTooltip(bucket: DashboardBucket) {
  return `${bucket.label} · 调用 ${formatCount(bucket.total_calls)} · 成功 ${formatCount(bucket.success_calls)} · 失败 ${formatCount(bucket.final_failed_calls)} · 成功率 ${formatPercent(bucket.success_rate)}`
}

function activityTooltipItems(bucket: DashboardBucket) {
  return [
    {
      label: '调用',
      value: formatCount(bucket.total_calls),
      markerClass: 'bg-slate-500 dark:bg-slate-400',
    },
    {
      label: '成功',
      value: formatCount(bucket.success_calls),
      markerClass: 'bg-emerald-500',
    },
    {
      label: '失败',
      value: formatCount(bucket.final_failed_calls),
      markerClass: 'bg-rose-500',
    },
    {
      label: '成功率',
      value: formatPercent(bucket.success_rate),
      markerClass: 'bg-amber-500',
    },
  ]
}

</script>

<style scoped>
.cpa-activity { display:flex; align-items:center; min-height:300px; padding:20px 4px 8px; }
.cpa-activity-content { width:100%; min-width:0; }
.cpa-activity-summary { display:flex; justify-content:space-between; flex-wrap:wrap; gap:16px; margin-bottom:28px; }
.cpa-activity-summary > div { display:flex; flex-direction:column; gap:6px; }
.cpa-activity-summary span { font-size:12px; color:var(--console-text-regular); }
.cpa-activity-summary strong { font-size:28px; font-weight:650; font-variant-numeric:tabular-nums; line-height:1.2; }
.cpa-activity-summary small { margin-left:6px; font-size:12px; font-weight:400; color:var(--console-text-regular); }
.cpa-activity-rate { color:#10b981; }
.cpa-activity-caption { margin-bottom:12px; font-size:12px; color:var(--console-text-regular); }
.cpa-activity-grid { display:grid; gap:6px; }
.dashboard-activity-cell-trigger { display:block; width:100%; min-width:0; height:24px; border-radius:4px; border:1px solid rgb(16 185 129 / .08); transition:transform .18s,box-shadow .18s; }
.dashboard-activity-cell-trigger:hover { transform:translateY(-2px); box-shadow:0 4px 12px rgb(16 185 129 / .18); }
.cpa-activity-axis,.cpa-activity-footer { display:flex; justify-content:space-between; gap:8px; font-size:11px; color:var(--console-text-regular); }
.cpa-activity-axis { margin-top:10px; }
.cpa-activity-footer { margin-top:24px; padding-top:16px; border-top:1px solid var(--console-border); flex-wrap:wrap; }
.cpa-activity-scale { display:flex; align-items:center; gap:4px; }
.cpa-activity-scale i { display:block; width:10px; height:10px; border-radius:2px; }
.activity-level-0 { background:hsl(var(--muted)); }
.activity-level-1 { background:#d1fae5; }
.activity-level-2 { background:#6ee7b7; }
.activity-level-3 { background:#34d399; }
.activity-level-4 { background:#10b981; }
html[data-theme='dark'] .activity-level-1 { background:#164c40; }
html[data-theme='dark'] .activity-level-2 { background:#13745b; }
html[data-theme='dark'] .activity-level-3 { background:#10b981; }
html[data-theme='dark'] .activity-level-4 { background:#34d399; }
html[data-theme='dark'] .cpa-activity-rate { color:#34d399; }
.dashboard-activity-cell {
  animation: dashboard-activity-cell-enter 180ms ease-out both;
}

@keyframes dashboard-activity-cell-enter {
  from {
    opacity: 0.35;
  }
  to {
    opacity: 1;
  }
}

@media (prefers-reduced-motion: reduce) {
  .dashboard-activity-cell,
  .dashboard-activity-cell-trigger {
    animation: none;
    transition: none;
  }

  .dashboard-activity-cell-trigger:hover {
    transform: none;
  }
}
</style>

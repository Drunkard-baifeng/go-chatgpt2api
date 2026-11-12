import type { DashboardRangeStats } from '@/types/api'

// Adapted from CPA-Manager-Plus dataPalette / TrafficOverviewCard; see THIRD_PARTY_NOTICES.md.
export type ChartKind = 'models' | 'outcomes' | 'duration' | 'model-duration'
type Point = number | null
type Series = { name: string; values: Point[]; color: string; percent?: boolean; dashed?: boolean }
export const chartPalette = (dark: boolean) => ({
  colors: dark
    ? ['#60a5fa', '#34d399', '#a78bfa', '#fbbf24', '#22d3ee', '#f87171', '#2dd4bf', '#f472b6']
    : ['#3b82f6', '#10b981', '#8b5cf6', '#f59e0b', '#06b6d4', '#ef4444', '#14b8a6', '#ec4899'],
  text: dark ? '#e5e5e5' : '#0f172a',
  muted: dark ? '#a3a3a3' : '#64748b',
  grid: dark ? 'rgba(255,255,255,.1)' : '#e8edf5',
  pointer: dark ? '#7a7a7a' : '#94a3b8',
  surface: dark ? 'rgba(24,28,40,.96)' : 'rgba(255,255,255,.96)',
  border: dark ? 'rgba(255,255,255,.12)' : '#dbe3ef',
})
const escapeHtml = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, char => (
  ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[char]!
))
const seconds = (values: Point[]) => values.map(value => value === null ? null : value / 1000)
const compact = (value: number) => Intl.NumberFormat('zh-CN', { notation: 'compact', maximumFractionDigits: 1 }).format(value)
const gradient = (color: string) => ({
  type: 'linear', x: 0, y: 0, x2: 0, y2: 1,
  colorStops: [{ offset: 0, color: `${color}30` }, { offset: 1, color: `${color}00` }],
})

export function buildChartModel(kind: ChartKind, range: DashboardRangeStats | undefined, modelNames: string[], dark: boolean) {
  const palette = chartPalette(dark)
  const colorForModel = (name: string) => palette.colors[Math.max(0, modelNames.indexOf(name)) % palette.colors.length]!
  const trend = range?.trend
  const duration = kind === 'duration' || kind === 'model-duration'
  const series: Series[] = kind === 'models'
    ? Object.entries(trend?.model_success_requests ?? {}).filter(([, values]) => values.some(value => value > 0))
      .map(([name, values]) => ({ name, values, color: colorForModel(name) }))
    : kind === 'model-duration'
      ? Object.entries(trend?.model_avg_success_duration_ms ?? {}).filter(([, values]) => values.some(value => value !== null))
        .map(([name, values]) => ({ name, values: seconds(values), color: colorForModel(name) }))
      : kind === 'duration'
        ? [{ name: '平均耗时', values: seconds(range?.buckets.map(bucket => bucket.avg_success_duration_ms) ?? []), color: palette.colors[0]! }]
        : [
          { name: '成功', values: trend?.success_requests ?? [], color: palette.colors[0]! },
          { name: '失败', values: trend?.final_failed_requests ?? [], color: palette.colors[5]! },
          { name: '切号', values: trend?.switch_count ?? [], color: palette.colors[2]!, dashed: true },
          { name: '成功率', values: trend?.success_rate ?? [], color: palette.colors[3]!, percent: true, dashed: true },
        ]
  const hasData = series.some(item => item.values.some(value => value !== null && (duration || item.percent || value > 0)))
  const labels = trend?.labels ?? []
  const axisColor = duration ? palette.colors[0] : palette.muted
  return {
    series,
    hasData,
    option: {
      backgroundColor: 'transparent',
      animationDuration: 260, animationDurationUpdate: 260,
      textStyle: { fontFamily: 'system-ui, sans-serif' },
      grid: { left: 12, right: 24, top: 28, bottom: 8, containLabel: true },
      legend: { show: false, data: series.map(item => item.name) },
      tooltip: {
        trigger: 'axis', confine: true, backgroundColor: palette.surface, borderColor: palette.border,
        borderRadius: 10, padding: 12, textStyle: { color: palette.text, fontSize: 12 },
        extraCssText: 'box-shadow:0 16px 36px rgba(15,23,42,.14);max-width:min(360px,90vw);white-space:normal;',
        axisPointer: { type: 'line', snap: true, lineStyle: { color: palette.pointer, type: 'dashed' } },
        formatter: (raw: unknown) => {
          if (!Array.isArray(raw)) return ''
          const items = raw as { axisValue?: string; seriesName?: string; value?: Point; color?: string }[]
          return `<div style="font-weight:600;margin-bottom:8px">${escapeHtml(items[0]?.axisValue)}</div>` + items.map(item => {
            if (item.value == null || !Number.isFinite(item.value)) return ''
            const descriptor = series.find(series => series.name === item.seriesName)
            const value = descriptor?.percent ? `${item.value.toFixed(1)}%`
              : duration ? `${Number(item.value.toFixed(2)).toLocaleString('zh-CN')} 秒` : item.value.toLocaleString('zh-CN')
            return `<div style="display:flex;align-items:baseline;gap:16px;margin-top:6px"><span style="flex:1;overflow-wrap:anywhere"><i style="display:inline-block;width:7px;height:7px;border-radius:50%;margin-right:7px;background:${descriptor?.color ?? palette.muted}"></i>${escapeHtml(item.seriesName)}</span><strong style="white-space:nowrap;font-variant-numeric:tabular-nums">${escapeHtml(value)}</strong></div>`
          }).join('')
        },
      },
      xAxis: {
        type: 'category', data: labels, boundaryGap: kind === 'models',
        axisLine: { show: false }, axisTick: { show: false },
        axisLabel: { color: palette.muted, fontSize: 11, margin: 12, hideOverlap: true },
      },
      yAxis: [{
        type: 'value', min: 0, minInterval: duration ? undefined : 1, splitNumber: 4,
        name: duration ? '秒' : '次', nameTextStyle: { color: axisColor, fontSize: 11, align: 'left' },
        axisLabel: { color: axisColor, fontSize: 11, formatter: compact },
        axisLine: { show: false }, axisTick: { show: false },
        splitLine: { lineStyle: { color: palette.grid, type: 'dashed' } },
      }, ...(kind === 'outcomes' ? [{
        type: 'value', min: 0, max: 100, interval: 25,
        axisLabel: { color: palette.colors[3], fontSize: 11, formatter: '{value}%' },
        axisLine: { show: false }, axisTick: { show: false }, splitLine: { show: false },
      }] : [])],
      series: series.map(item => kind === 'models' ? {
        name: item.name, type: 'bar', stack: 'requests', barMaxWidth: 24,
        data: item.values, itemStyle: { color: item.color, borderRadius: [3, 3, 0, 0] },
        emphasis: { focus: 'series' },
      } : {
        name: item.name, type: 'line', data: item.values, yAxisIndex: item.percent ? 1 : 0,
        smooth: .22, connectNulls: false, showSymbol: labels.length <= 30, symbol: 'circle', symbolSize: 5,
        lineStyle: { width: 2.4, color: item.color, type: item.dashed ? 'dashed' : 'solid' },
        itemStyle: { color: item.color, borderColor: palette.surface, borderWidth: 1.5 },
        areaStyle: item.dashed ? undefined : { color: gradient(item.color) },
        emphasis: { focus: 'series', scale: 1.5 },
      }),
    },
  }
}

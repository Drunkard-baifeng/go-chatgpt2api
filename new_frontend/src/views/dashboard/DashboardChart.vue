<template>
  <div class="cpa-data-chart" :data-chart="kind">
    <div ref="element" class="cpa-data-chart__canvas" role="img" :aria-label="title" />
    <p v-if="loadError || !model.hasData" class="cpa-data-chart__empty" role="status">{{ loadError || '当前范围内暂无可统计数据' }}</p>
    <div v-if="model.hasData" class="cpa-data-chart__legend" :aria-label="`${title}图例`">
      <button v-for="item in model.series" :key="item.name" type="button" :aria-pressed="selected[item.name] !== false"
        :class="{ 'is-muted': selected[item.name] === false }" :title="item.name" @click="toggle(item.name)">
        <span :style="{ background: item.color }" aria-hidden="true" />{{ item.name }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onActivated, onBeforeUnmount, onDeactivated, onMounted, ref, watch } from 'vue'
import type { DashboardRangeStats } from '@/types/api'
import { buildChartModel, type ChartKind } from './chartOptions'

const props = defineProps<{ kind: ChartKind; title: string; range?: DashboardRangeStats; modelNames: string[] }>()
const element = ref<HTMLDivElement | null>(null)
const dark = ref(document.documentElement.dataset.theme === 'dark')
const reducedMotion = window.matchMedia('(prefers-reduced-motion: reduce)')
const selected = ref<Record<string, boolean>>({})
const loadError = ref('')
const model = computed(() => buildChartModel(props.kind, props.range, props.modelNames, dark.value))
type Chart = { setOption: (option: object, options: object) => void; resize: () => void; dispose: () => void }
let chart: Chart | undefined
let frame = 0
let signature = ''
let active = false
const resizeObserver = new ResizeObserver(schedule)
const themeObserver = new MutationObserver(() => {
  dark.value = document.documentElement.dataset.theme === 'dark'
})

function toggle(name: string) { selected.value = { ...selected.value, [name]: selected.value[name] === false } }
function schedule() {
  cancelAnimationFrame(frame)
  if (!active || document.hidden) return
  frame = requestAnimationFrame(render)
}
function render() {
  const el = element.value
  if (!el || !active || document.hidden || !el.clientWidth) return
  const echarts = (window as Window & { echarts?: { init: (el: HTMLElement) => Chart } }).echarts
  if (!echarts) { loadError.value = '图表组件未能加载，请刷新页面重试'; return }
  chart ??= echarts.init(el)
  chart.resize()
  const next = JSON.stringify([props.range, props.modelNames, dark.value, selected.value, reducedMotion.matches])
  if (next === signature) return
  signature = next
  chart.setOption({ ...model.value.option, animation: !reducedMotion.matches,
    legend: { ...model.value.option.legend, selected: selected.value },
    series: model.value.hasData ? model.value.option.series : [],
  }, { notMerge: true })
}
function start() {
  if (document.hidden) return
  if (element.value) resizeObserver.observe(element.value)
  themeObserver.observe(document.documentElement, { attributes: true, attributeFilter: ['data-theme'] })
  dark.value = document.documentElement.dataset.theme === 'dark'
  reducedMotion.addEventListener('change', schedule)
  schedule()
}
function stop() {
  cancelAnimationFrame(frame)
  resizeObserver.disconnect()
  themeObserver.disconnect()
  reducedMotion.removeEventListener('change', schedule)
  chart?.dispose()
  chart = undefined
  signature = ''
}
watch([model, selected], schedule, { flush: 'post' })
// This child mounts after async dashboard data, possibly after its cached parent
// has already activated. Both mount and activation must be safe to run directly.
function activate() { active = true; start() }
function deactivate() { active = false; stop() }
function visibilityChanged() { if (active && !document.hidden) start(); else stop() }
onMounted(() => { document.addEventListener('visibilitychange', visibilityChanged); activate() })
onActivated(activate)
onDeactivated(deactivate)
onBeforeUnmount(() => { document.removeEventListener('visibilitychange', visibilityChanged); deactivate() })
</script>

<style scoped>
.cpa-data-chart { position:relative; min-width:0; }
.cpa-data-chart__canvas { width:100%; height:260px; }
.cpa-data-chart__empty { position:absolute; top:45%; left:0; right:0; padding:8px; text-align:center; font-size:13px; color:var(--console-text-regular); background:var(--console-glass); }
.cpa-data-chart__legend { display:flex; flex-wrap:wrap; justify-content:center; gap:4px 16px; padding:12px 4px 0; }
.cpa-data-chart__legend button { display:inline-flex; align-items:center; gap:7px; min-width:0; max-width:100%; padding:4px 0; font-size:12px; color:var(--console-text-regular); overflow-wrap:anywhere; text-align:left; transition:opacity .18s; }
.cpa-data-chart__legend button span { width:8px; height:8px; border-radius:50%; flex-shrink:0; }
.cpa-data-chart__legend button.is-muted { opacity:.45; text-decoration:line-through; }
.cpa-data-chart__legend button:focus-visible { outline:2px solid hsl(var(--ring)); outline-offset:4px; border-radius:3px; }
@media(min-width:1920px) { .cpa-data-chart__canvas { height:300px; } }
@media(max-width:640px) { .cpa-data-chart__canvas { height:230px; } }
@media(prefers-reduced-motion:reduce) { .cpa-data-chart__legend button { transition:none; } }
</style>

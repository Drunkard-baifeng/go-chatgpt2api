<template>
  <article class="cpa-metric" :class="`cpa-metric--${iconTone}`">
    <div class="cpa-metric-top">
      <span class="cpa-metric-icon"><Icon :icon="icon" aria-hidden="true" /></span>
      <span class="cpa-metric-label" :title="label">{{ label }}</span>
    </div>
    <strong :title="String(value)">{{ value }}</strong>
    <p v-if="description" class="cpa-metric-description">{{ description }}</p>
    <svg v-if="decoration" class="cpa-metric-wave" viewBox="0 0 100 30" preserveAspectRatio="none" aria-hidden="true"><path d="M0,25 Q15,5 30,20 T60,10 T100,25" fill="none" stroke="currentColor" stroke-width="2" /></svg>
  </article>
</template>

<script setup lang="ts">
import { Icon } from '@iconify/vue'
withDefaults(defineProps<{
  label: string
  value: string | number
  icon: string
  iconTone?: 'info' | 'neutral' | 'success' | 'warning' | 'error'
  description?: string
  decoration?: boolean
}>(), { iconTone: 'info', description: '', decoration: true })
</script>

<style scoped>
.cpa-metric { --metric-accent: hsl(var(--primary)); position: relative; display: flex; flex-direction: column; gap: 12px; min-width: 0; overflow: hidden; padding: 16px; background: var(--console-glass); border: 1px solid var(--console-glass-border); border-radius: 12px; transition: border-color .2s, transform .2s, box-shadow .2s; }
.cpa-metric:hover { border-color: var(--metric-accent); transform: translateY(-2px); box-shadow: 0 8px 16px rgb(0 0 0 / .1); }
.cpa-metric-top { display: flex; align-items: center; gap: 8px; color: hsl(var(--muted-foreground)); font-size: 12px; font-weight: 500; z-index: 1; }
.cpa-metric-label { min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cpa-metric .cpa-metric-icon { width: 32px; height: 32px; display: grid; place-items: center; border-radius: 8px; flex-shrink: 0; background: color-mix(in srgb, var(--metric-accent) 10%, transparent); color: var(--metric-accent); }
.cpa-metric-icon svg { width: 16px; height: 16px; }
.cpa-metric strong { display: block; position: relative; z-index: 1; color: hsl(var(--foreground)); font-size: 24px; line-height: 1.2; font-weight: 700; font-variant-numeric: tabular-nums; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.cpa-metric-wave { position: absolute; left: 0; bottom: -5px; width: 100%; height: 30px; color: var(--metric-accent); opacity: .1; pointer-events: none; }
.cpa-metric-description { position:relative; z-index:1; margin:0; font-size:11px; line-height:1.5; color:hsl(var(--muted-foreground)); overflow-wrap:anywhere; }
.cpa-metric--success { --metric-accent: hsl(var(--tone-success-strong)); }
.cpa-metric--warning { --metric-accent: hsl(var(--tone-warning-strong)); }
.cpa-metric--error { --metric-accent: hsl(var(--tone-error-strong)); }
.cpa-metric--neutral { --metric-accent: hsl(var(--muted-foreground)); }
@media (max-width: 560px) { .cpa-metric { padding: 12px; } .cpa-metric strong { font-size: 22px; } }
@media (prefers-reduced-motion: reduce) { .cpa-metric:hover { transform: none; } }
</style>

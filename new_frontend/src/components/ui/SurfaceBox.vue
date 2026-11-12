<template>
  <component :is="tag" class="cpa-surface-box" :class="[`tone-${tone}`, `density-${density}`, { dashed, scroll, mono, wrap }]"><slot /></component>
</template>
<script setup lang="ts">
withDefaults(defineProps<{ tag?: 'div' | 'p' | 'label'; tone?: 'card' | 'background' | 'muted' | 'danger'; density?: 'compact' | 'normal'; dashed?: boolean; scroll?: boolean; mono?: boolean; wrap?: boolean }>(), { tag:'div', tone:'card', density:'normal', dashed:false, scroll:false, mono:false, wrap:false })
</script>
<style scoped>
.cpa-surface-box { min-width:0; padding:12px; border:1px solid var(--app-border); border-radius:var(--app-radius-sm); background:var(--app-surface-strong); color:var(--app-text-primary); font-size:12px; }
.cpa-surface-box.tone-background,.cpa-surface-box.tone-muted { background:var(--app-surface-muted); }
.cpa-surface-box.tone-muted { color:var(--app-text-regular); }
.cpa-surface-box.tone-danger { background:hsl(var(--tone-error-bg)); border-color:hsl(var(--tone-error-border)); color:hsl(var(--tone-error-foreground)); }
.cpa-surface-box.density-compact { padding:8px 12px; }
.cpa-surface-box.dashed { border-style:dashed; }
.cpa-surface-box.scroll { max-height:6rem; overflow-y:auto; overscroll-behavior:contain; }
.cpa-surface-box.mono { font-family:ui-monospace,monospace; font-size:11px; }
.cpa-surface-box.wrap { overflow-wrap:anywhere; }
</style>

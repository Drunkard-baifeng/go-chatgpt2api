<template>
  <div class="cpa-filter-toolbar" :class="[`gap-${gap}`, `mobile-${mobileMode}`, { bordered, start: align === 'start' }]"><slot /></div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ bordered?: boolean; gap?: 'tight' | 'normal' | 'loose'; mobileMode?: 'wrap' | 'stack' | 'none'; align?: 'center' | 'start' }>(), { bordered:true, gap:'normal', mobileMode:'wrap', align:'center' })
</script>

<style scoped>
.cpa-filter-toolbar { display:flex; min-width:0; flex-wrap:wrap; align-items:center; }
.cpa-filter-toolbar.bordered { padding-top:12px; border-top:1px solid var(--app-border); }
.cpa-filter-toolbar.start { align-items:flex-start; }
.cpa-filter-toolbar.gap-tight { gap:8px; } .cpa-filter-toolbar.gap-normal { gap:10px; } .cpa-filter-toolbar.gap-loose { gap:12px; }
@media(max-width:640px) { .cpa-filter-toolbar.mobile-wrap { width:100%; } .cpa-filter-toolbar.mobile-wrap :slotted(*) { flex:1 1 auto; } .cpa-filter-toolbar.mobile-stack { width:100%; flex-direction:column; align-items:stretch; } .cpa-filter-toolbar.mobile-stack :slotted(*) { width:100%; min-width:0; } }
</style>

<template>
  <section class="cpa-form-section" :class="[`density-${density}`, `surface-${muted ? 'muted' : surface}`]">
    <header v-if="title || subtitle || $slots.actions" class="cpa-form-section__header">
      <div class="min-w-0"><h3 v-if="title">{{ title }}</h3><p v-if="subtitle">{{ subtitle }}</p></div>
      <div v-if="$slots.actions" class="cpa-form-section__actions"><slot name="actions" /></div>
    </header>
    <slot />
  </section>
</template>
<script setup lang="ts">
withDefaults(defineProps<{
  title?: string; subtitle?: string; density?: 'compact' | 'normal' | 'roomy'
  surface?: 'card' | 'background' | 'muted' | 'plain'; muted?: boolean
}>(), { title: '', subtitle: '', density: 'normal', surface: 'card', muted: false })
</script>
<style scoped>
.cpa-form-section { min-width:0; padding:16px; border:1px solid var(--app-border); border-radius:var(--app-radius-md); background:var(--app-surface-strong); }
.cpa-form-section.density-compact { padding:12px; }
.cpa-form-section.density-roomy { padding:20px; }
.cpa-form-section.surface-background,.cpa-form-section.surface-muted { background:var(--app-surface-muted); }
.cpa-form-section.surface-plain { padding:0; border:0; background:transparent; }
.cpa-form-section__header { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; margin-bottom:16px; }
.cpa-form-section__header h3 { color:var(--app-text-primary); font-size:14px; font-weight:600; }
.cpa-form-section__header p { margin-top:4px; color:var(--app-text-muted); font-size:12px; line-height:1.6; }
.cpa-form-section__actions { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:8px; }
</style>

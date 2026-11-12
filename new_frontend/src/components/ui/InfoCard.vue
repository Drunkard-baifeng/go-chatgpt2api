<template>
  <component :is="tag" class="cpa-info-card" :class="[`tone-${tone}`, `density-${density}`]">
    <header v-if="title || description || $slots.actions">
      <div class="min-w-0"><h3 v-if="title">{{ title }}</h3><p v-if="description">{{ description }}</p></div>
      <div v-if="$slots.actions" class="cpa-info-card__actions"><slot name="actions" /></div>
    </header>
    <div v-if="$slots.default" class="cpa-info-card__body"><slot /></div>
  </component>
</template>
<script setup lang="ts">
withDefaults(defineProps<{ tag?: 'article' | 'div' | 'section'; title?: string; description?: string; tone?: 'card' | 'muted'; density?: 'compact' | 'normal' | 'roomy' }>(), { tag:'section', title:'', description:'', tone:'card', density:'normal' })
</script>
<style scoped>
.cpa-info-card { min-width:0; padding:16px; border:1px solid var(--app-border); border-radius:var(--app-radius-md); background:var(--app-surface-strong); }
.cpa-info-card.tone-muted { background:var(--app-surface-muted); }
.cpa-info-card.density-compact { padding:12px; } .cpa-info-card.density-roomy { padding:20px; }
header { display:flex; align-items:flex-start; justify-content:space-between; gap:12px; }
h3 { color:var(--app-text-primary); font-size:14px; font-weight:600; line-height:1.4; }
p { margin-top:4px; color:var(--app-text-muted); font-size:12px; line-height:1.6; }
.cpa-info-card__actions { display:flex; flex-wrap:wrap; justify-content:flex-end; gap:8px; }
header + .cpa-info-card__body { margin-top:12px; }
</style>

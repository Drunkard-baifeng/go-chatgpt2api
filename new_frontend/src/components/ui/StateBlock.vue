<template>
  <component :is="tag" class="cpa-state-block" :class="{ compact, dashed, plain }">
    <div v-if="$slots.media" class="cpa-state-block__media"><slot name="media" /></div>
    <p v-if="title" class="cpa-state-block__title">{{ title }}</p>
    <p v-if="description" class="cpa-state-block__description">{{ description }}</p>
    <slot />
  </component>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ tag?: 'div' | 'p'; title?: string; description?: string; compact?: boolean; dashed?: boolean; plain?: boolean }>(), { tag:'div', title:'', description:'', compact:false, dashed:false, plain:false })
</script>

<style scoped>
.cpa-state-block { border:1px solid var(--app-border-strong); border-radius:12px; background:var(--app-surface); padding:30px 16px; text-align:center; color:var(--app-text-regular); font-size:14px; }
.cpa-state-block.compact { padding:20px 12px; font-size:12px; }
.cpa-state-block.dashed { border-style:dashed; }
.cpa-state-block.plain { min-height:7rem; border:0; border-radius:0; background:transparent; }
.cpa-state-block__media { display:flex; justify-content:center; margin-bottom:12px; }
.cpa-state-block__title { color:var(--app-text-primary); font-size:14px; font-weight:600; }
.cpa-state-block__description { margin-top:8px; color:var(--app-text-muted); font-size:12px; }
</style>

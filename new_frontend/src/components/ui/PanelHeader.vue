<template>
  <div class="cpa-panel-header" :class="{ 'cpa-panel-header--start': align === 'start' }">
    <div class="cpa-panel-header__copy">
      <h2 v-if="title" class="ui-section-title">{{ title }}</h2>
      <slot name="copy" />
    </div>
    <div v-if="$slots.actions" class="cpa-panel-header__actions"><slot name="actions" /></div>
  </div>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ title?: string; align?: 'center' | 'start' }>(), { title: '', align: 'center' })
</script>

<style scoped>
.cpa-panel-header { display:flex; min-width:0; flex-wrap:wrap; align-items:center; justify-content:space-between; gap:12px; }
.cpa-panel-header--start { align-items:flex-start; }
.cpa-panel-header__copy { min-width:0; flex:var(--panel-header-copy-flex, 1 1 auto); }
.cpa-panel-header__actions { display:flex; flex-wrap:wrap; align-items:center; justify-content:flex-end; gap:var(--panel-header-action-gap, 8px); }
@media(max-width:640px) { .cpa-panel-header__actions { width:100%; } .cpa-panel-header__actions :slotted(*) { flex:1 1 auto; } }
</style>

<template>
  <section class="cpa-panel" :class="{ 'cpa-panel--flush': flush }"><slot /></section>
</template>

<script setup lang="ts">
withDefaults(defineProps<{ flush?: boolean }>(), { flush: false })
</script>

<style scoped>
.cpa-panel {
  min-width: 0;
  border: 1px solid var(--app-border);
  border-radius: var(--app-radius-md, 12px);
  background: var(--app-surface-strong);
  padding: var(--app-card-padding, 20px);
  box-shadow: var(--app-shadow-soft, 0 1px 2px rgb(31 50 79 / 5%));
}
.cpa-panel--flush { overflow: hidden; padding: 0; }
</style>

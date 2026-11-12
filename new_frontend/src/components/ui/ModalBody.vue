<template><div class="cpa-modal-body" :class="`density-${density}`"><slot /></div></template>
<script setup lang="ts">
withDefaults(defineProps<{ density?:'compact'|'normal'|'roomy' }>(), { density:'normal' })
</script>
<style scoped>
.cpa-modal-body { min-width:0; min-height:0; flex:1 1 auto; overflow-y:auto; overscroll-behavior:contain; }
.cpa-modal-body.density-compact { padding:12px 16px; } .cpa-modal-body.density-normal { padding:16px 20px; } .cpa-modal-body.density-roomy { padding:20px; }
</style>

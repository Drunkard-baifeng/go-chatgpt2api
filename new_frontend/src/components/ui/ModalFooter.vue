<template><footer class="cpa-modal-footer" :class="[`align-${align}`, { bordered, compact, flush, wrap }]"><slot /></footer></template>
<script setup lang="ts">
withDefaults(defineProps<{ align?:'start'|'center'|'end'|'between'; bordered?:boolean; compact?:boolean; flush?:boolean; wrap?:boolean }>(), { align:'end', bordered:true, compact:false, flush:false, wrap:true })
</script>
<style scoped>
.cpa-modal-footer { display:flex; flex:0 0 auto; align-items:center; gap:8px; padding:12px 20px; }
.cpa-modal-footer.bordered { border-top:1px solid var(--app-border); } .cpa-modal-footer.compact { padding-block:10px; } .cpa-modal-footer.flush { padding:0; } .cpa-modal-footer.wrap { flex-wrap:wrap; }
.cpa-modal-footer.align-start { justify-content:flex-start; } .cpa-modal-footer.align-center { justify-content:center; } .cpa-modal-footer.align-end { justify-content:flex-end; } .cpa-modal-footer.align-between { justify-content:space-between; }
</style>

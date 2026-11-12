<template>
  <header class="cpa-modal-header" :class="{ bordered, compact, flush }">
    <div class="cpa-modal-header__copy">
      <component :is="headingTag" v-if="title" :class="titleClass">{{ title }}</component>
      <p v-if="subtitle" class="cpa-modal-header__subtitle">{{ subtitle }}</p><slot name="copy" />
    </div>
    <div v-if="showClose || $slots.actions" class="cpa-modal-header__actions"><slot name="actions" /><CloseButton v-if="showClose" :label="closeText" :disabled="closeDisabled" @click="$emit('close')" /></div>
  </header>
</template>

<script setup lang="ts">
import { CloseButton } from 'nanocat-ui'
withDefaults(defineProps<{ title?:string; subtitle?:string; titleClass?:string; headingTag?:'h3'|'h4'|'p'; closeText?:string; closeDisabled?:boolean; showClose?:boolean; bordered?:boolean; compact?:boolean; flush?:boolean }>(), { title:'', subtitle:'', titleClass:'ui-section-title', headingTag:'p', closeText:'关闭', closeDisabled:false, showClose:true, bordered:true, compact:false, flush:false })
defineEmits<{ close:[] }>()
</script>

<style scoped>
.cpa-modal-header { display:flex; min-width:0; flex:0 0 auto; align-items:flex-start; justify-content:space-between; gap:16px; padding:18px 20px 14px; }
.cpa-modal-header.bordered { border-bottom:1px solid var(--app-border); }
.cpa-modal-header.compact { padding-block:12px; } .cpa-modal-header.flush { padding:0; }
.cpa-modal-header__copy { min-width:0; } .cpa-modal-header__subtitle { margin-top:4px; color:var(--app-text-muted); font-size:12px; }
.cpa-modal-header__actions { display:flex; flex-shrink:0; align-items:center; gap:8px; }
</style>

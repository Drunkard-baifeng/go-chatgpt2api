<template>
  <div class="cpa-switch-card" :class="{ 'is-disabled': disabled, 'cpa-switch-card--inline': inline }">
    <div class="cpa-switch-card__row">
      <div class="cpa-switch-card__copy">
        <label :id="`${id}-label`" :for="id">{{ label }}</label>
        <p v-if="description" :id="`${id}-description`">{{ description }}</p>
      </div>
      <span class="cpa-switch">
        <input :id="id" type="checkbox" role="switch" :checked="modelValue" :disabled="disabled"
          :aria-labelledby="`${id}-label`" :aria-describedby="description ? `${id}-description` : undefined"
          @change="handleChange" />
        <span class="cpa-switch__track" aria-hidden="true"><span /></span>
      </span>
    </div>
    <div v-if="$slots.default" class="cpa-switch-card__detail"><slot /></div>
  </div>
</template>
<script setup lang="ts">
import { useId } from 'vue'
const props = defineProps<{ modelValue: boolean; label: string; description?: string; disabled?: boolean; inline?: boolean }>()
const emit = defineEmits<{ 'update:modelValue': [value: boolean] }>()
const id = useId()
function handleChange(event: Event) {
  const input = event.target as HTMLInputElement
  const nextValue = input.checked
  // The owner may wait for a server response; keep the committed state until it updates.
  input.checked = props.modelValue
  emit('update:modelValue', nextValue)
}
</script>
<style scoped>
/* VisualConfigEditor.toggleRow adapted to native keyboard-accessible switches. */
.cpa-switch-card { min-width:0; padding:16px 18px; border:1px solid var(--console-border); border-radius:18px; background:hsl(var(--secondary) / .45); transition:border-color .18s,background-color .18s; }
.cpa-switch-card__row { display:grid; grid-template-columns:minmax(0,1fr) auto; align-items:center; gap:16px; }
.cpa-switch-card__copy { display:flex; flex-direction:column; gap:6px; min-width:0; }
.cpa-switch-card__copy label { color:var(--app-text-primary); font-size:15px; font-weight:700; line-height:1.4; cursor:pointer; }
.cpa-switch-card__copy p { color:var(--console-text-regular); font-size:13px; line-height:1.6; overflow-wrap:anywhere; }
.cpa-switch-card__detail { margin-top:16px; padding-top:16px; border-top:1px solid var(--console-border); }
.cpa-switch { position:relative; display:grid; align-items:center; width:44px; height:32px; flex-shrink:0; }
.cpa-switch input { position:absolute; inset:0; width:100%; height:100%; opacity:0; cursor:pointer; margin:0; z-index:1; }
.cpa-switch__track { display:block; width:44px; height:24px; border:1px solid hsl(var(--input)); border-radius:999px; background:hsl(var(--muted-foreground) / .25); transition:background-color .18s,border-color .18s; }
.cpa-switch__track > span { display:block; width:18px; height:18px; margin:2px; border-radius:50%; background:white; box-shadow:0 1px 3px rgb(0 0 0 / .16); transition:transform .18s; }
.cpa-switch input:checked + .cpa-switch__track { background:var(--console-primary-solid); border-color:var(--console-primary-solid); }
.cpa-switch input:checked + .cpa-switch__track > span { transform:translateX(20px); }
.cpa-switch input:focus-visible + .cpa-switch__track { outline:2px solid hsl(var(--ring)); outline-offset:3px; }
.cpa-switch-card:focus-within { border-color:hsl(var(--primary) / .5); }
.is-disabled .cpa-switch { opacity:.45; }
.is-disabled label,.is-disabled input { cursor:not-allowed; }
@media(hover:hover) { .cpa-switch-card:not(.is-disabled):hover { background:hsl(var(--secondary) / .8); border-color:hsl(var(--primary) / .25); } }
@media(max-width:640px) { .cpa-switch-card { padding:14px; } .cpa-switch-card__row { gap:12px; } }
@media(prefers-reduced-motion:reduce) { .cpa-switch-card,.cpa-switch__track,.cpa-switch__track > span { transition:none; } }
.cpa-switch-card.cpa-switch-card--inline { padding:0; border:0; border-radius:0; background:transparent; }
.cpa-switch-card--inline .cpa-switch-card__row { gap:10px; }
.cpa-switch-card--inline .cpa-switch-card__copy label { font-size:12px; font-weight:500; color:var(--console-text-regular); }
</style>

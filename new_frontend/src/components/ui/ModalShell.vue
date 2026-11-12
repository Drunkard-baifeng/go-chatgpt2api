<template>
  <NanocatModalShell
    :open="open"
    :max-width="maxWidth"
    :z-index="zIndex"
    :align="placement === 'end' ? 'start' : align"
    :placement="placement"
    :motion="placement === 'end' ? 'drawer' : 'none'"
    :overlay-class="placement === 'end' ? 'cpa-drawer-overlay' : undefined"
    :root-class="resolvedPanelClass"
    :aria-label="ariaLabel"
    :close-on-overlay="closeOnBackdrop"
    :close-on-escape="closeOnEscape"
    bare
    @close="emit('close')"
  ><slot /></NanocatModalShell>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { ModalShell as NanocatModalShell, OVERLAY_LAYER } from 'nanocat-ui'

const props = withDefaults(defineProps<{
  open: boolean
  maxWidth?: string
  zIndex?: number
  closeOnBackdrop?: boolean
  closeOnEscape?: boolean
  ariaLabel?: string
  align?: 'center' | 'start'
  placement?: 'center' | 'end'
  panelClass?: string
  scrollable?: boolean
}>(), {
  maxWidth: 'min(56rem, 100vw)',
  zIndex: OVERLAY_LAYER.modal,
  closeOnBackdrop: false,
  closeOnEscape: undefined,
  ariaLabel: '',
  align: 'center',
  placement: 'end',
  panelClass: '',
  scrollable: false,
})
const emit = defineEmits<{ close: [] }>()
const resolvedPanelClass = computed(() => [
  'cpa-dialog',
  props.placement === 'end' ? 'cpa-drawer-panel' : '',
  props.scrollable ? 'cpa-dialog--body-scroll' : '',
  props.panelClass,
].filter(Boolean).join(' '))
</script>

<style>
/* Teleported overlays keep Nanocat's focus lifecycle, with CPA-owned geometry. */
.ui-modal-panel.cpa-dialog:not(.cpa-drawer-panel) {
  min-width: 0;
  min-height: 0;
  max-height: calc(100dvh - 2rem);
  overflow-y: auto;
  overscroll-behavior: contain;
}
.ui-modal-panel.cpa-dialog--body-scroll {
  display: flex;
  flex-direction: column;
  overflow: hidden;
}
.cpa-dialog:not(.cpa-drawer-panel) > .cpa-modal-header {
  position: sticky;
  top: 0;
  z-index: 2;
  background: hsl(var(--card));
}
</style>

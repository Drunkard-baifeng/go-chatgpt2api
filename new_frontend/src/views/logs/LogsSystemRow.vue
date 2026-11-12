<template>
  <tr
    v-memo="[signature]"
    class="cpa-log-row"
    :class="{ 'bg-primary/5': selected }"
    :aria-selected="selected || undefined"
  >
    <td class="py-4 pl-4 pr-2 align-middle">
      <Checkbox
        :model-value="selected"
        @update:model-value="handleSelect"
      >
        <span class="sr-only">选择日志 {{ item.time || item.id }}</span>
      </Checkbox>
    </td>
    <td class="log-time-cell py-4 pr-5 align-middle text-xs">
      <p class="whitespace-nowrap text-foreground">{{ timeDisplay.primary }}</p>
      <p v-if="timeDisplay.secondary" class="mt-1 whitespace-nowrap text-muted-foreground">
        {{ timeDisplay.secondary }}
      </p>
    </td>
    <td class="log-request-cell py-4 pr-5 align-middle" data-label="请求">
      <p
        class="flex min-w-0 items-baseline gap-1.5 truncate text-xs text-foreground"
        :title="[request.kind, request.primary].filter(Boolean).join(' ')"
      >
        <span v-if="request.kind" class="shrink-0 text-muted-foreground">{{ request.kind }}</span>
        <span class="truncate font-medium">{{ request.primary || '-' }}</span>
      </p>
      <p
        v-if="request.secondary"
        class="mt-1 truncate text-[11px] text-muted-foreground"
        :title="request.secondary"
      >
        {{ request.secondary }}
      </p>
    </td>
    <td class="log-execution-cell py-4 pr-5 align-middle" data-label="执行">
      <p class="truncate text-xs text-foreground" :title="execution.primary">
        {{ execution.primary || '-' }}
      </p>
      <div
        v-if="item.accountSwitchCount"
        class="mt-1 flex min-w-0 items-center gap-1.5"
      >
        <MetaChip
          v-if="item.accountSwitchCount"
          size="xs"
          tone="warning"
          chip-class="shrink-0"
        >
          切换 {{ item.accountSwitchCount }} 次
        </MetaChip>
      </div>
    </td>
    <td class="log-key-cell py-4 pr-5 align-middle" data-label="密钥">
      <p class="truncate text-xs font-medium text-foreground" :title="item.presentation.key?.primary">{{ item.presentation.key?.primary || '-' }}</p>
    </td>
    <td class="log-duration-cell py-4 pr-5 align-middle text-xs text-muted-foreground" data-label="耗时">
      <div :title="[durationDisplay.total, durationDisplay.breakdown].filter(Boolean).join(' ')">
        <MetaChip
          v-if="durationDisplay.total"
          size="xs"
          :tone="durationTone"
          chip-class="font-mono tabular-nums"
        >
          <Icon icon="lucide:clock-3" class="mr-1 h-3 w-3" />
          {{ durationDisplay.total }}
        </MetaChip>
        <span v-else>-</span>
        <p
          v-if="durationDisplay.breakdown"
          class="mt-0.5 max-w-[14rem] truncate whitespace-nowrap text-[10px] text-muted-foreground/75"
        >
          {{ durationDisplay.breakdown }}
        </p>
      </div>
    </td>
    <td class="log-status-cell" data-label="状态">
      <StateBadge :tone="statusTone(item)" shape="rounded" size="xs">{{ statusLabel(item) }}</StateBadge>
    </td>
    <td class="log-result-cell" data-label="结果说明">
      <p class="log-outcome text-xs font-medium text-foreground" :class="{ 'text-rose-600': failed }" :title="outcome">{{ outcome }}</p>
    </td>
    <td class="log-http-cell" data-label="HTTP">
      <div class="flex flex-col gap-1 font-mono text-[11px] text-foreground">
        <span v-if="item.raw.api_status_code">API {{ item.raw.api_status_code }}</span>
        <span v-if="item.raw.status_code" class="text-muted-foreground">上游 {{ item.raw.status_code }}</span>
        <span v-if="!item.raw.api_status_code && !item.raw.status_code">-</span>
      </div>
    </td>
    <td class="log-error-cell" data-label="错误码">
      <p class="truncate text-xs font-mono text-muted-foreground" :title="item.raw.error_code">{{ item.raw.error_code || '-' }}</p>
    </td>
    <td class="log-image-cell" data-label="图片">
      <LogImagePreviewCell v-if="item.imageUrls.length"
        :image-urls="item.imageUrls" :first-image-broken="firstImageBroken"
        :alt="item.preview || '日志结果图片'" @preview-click="handleOpenDetail"
        @image-error="handleImageError" />
      <span v-else class="text-xs text-muted-foreground">-</span>
    </td>
    <td class="log-resolution-cell" data-label="分辨率">
      <span class="log-resolution">{{ item.presentation.result.resolution || '-' }}</span>
    </td>
    <td class="log-action-cell py-4 pr-4 text-right align-middle">
      <div class="flex justify-end gap-1.5">
        <Button
          size="xs"
          variant="outline"
          icon-only aria-label="查看详情" title="查看详情"
          @click="handleOpenDetail"
        >
          <Icon icon="lucide:panel-right-open" aria-hidden="true" />
        </Button>
        <Button
          size="xs"
          variant="ghost"
          root-class="log-delete-button" icon-only aria-label="删除日志" title="删除日志"
          @click="handleRequestDelete"
        >
          <Icon icon="lucide:trash-2" aria-hidden="true" />
        </Button>
      </div>
    </td>
  </tr>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Icon } from '@iconify/vue'
import { Button, Checkbox } from 'nanocat-ui'
import LogImagePreviewCell from '@/components/ai/LogImagePreviewCell.vue'
import MetaChip from '@/components/ai/MetaChip.vue'
import StateBadge from '@/components/ai/StateBadge.vue'
import { type SystemLogRow } from '@/api/logs'
import {
  executionDisplay,
  logDurationDisplay,
  logDurationTone,
  outcomeText,
  requestDisplay,
  statusLabel,
  statusTone,
} from '@/views/logs/logsView'

const props = defineProps<{
  item: SystemLogRow
  signature: string
  selected: boolean
  firstImageBroken: boolean
}>()

const emit = defineEmits<{
  (e: 'toggle-selection', id: string, checked: boolean): void
  (e: 'open-detail', item: SystemLogRow): void
  (e: 'request-delete-log', item: SystemLogRow): void
  (e: 'image-error', event: Event, url: string): void
}>()

const request = computed(() => requestDisplay(props.item))
const execution = computed(() => executionDisplay(props.item))
const outcome = computed(() => outcomeText(props.item))
const failed = computed(() => props.item.presentation.is_failure)
const durationDisplay = computed(() => logDurationDisplay(props.item))
const durationTone = computed(() => logDurationTone(props.item))
const timeDisplay = computed(() => {
  const value = props.item.time.trim()
  const match = value.match(/^(\d{4}-\d{2}-\d{2})[ T](\d{2}:\d{2}:\d{2})/)
  if (!match) return { primary: value || '-', secondary: '' }
  return { primary: match[2], secondary: match[1] }
})

function handleSelect(checked: boolean | string | number) {
  emit('toggle-selection', props.item.id, Boolean(checked))
}

function handleOpenDetail() {
  emit('open-detail', props.item)
}

function handleRequestDelete() {
  emit('request-delete-log', props.item)
}

function handleImageError(event: Event, url: string) {
  emit('image-error', event, url)
}
</script>

<style scoped>
.cpa-log-row { border-bottom:1px solid var(--console-border); transition:background .15s; }
.cpa-log-row > td { height:72px; padding:10px 12px; vertical-align:middle; }
.cpa-log-row > td:first-child { padding-left:16px; }
.cpa-log-row > td:last-child { padding-right:16px; }
.cpa-log-row:hover { background:color-mix(in srgb,var(--console-primary-solid) 6%,transparent); }
.cpa-log-row[aria-selected="true"] { background:var(--app-accent-soft); }
.log-outcome { display:-webkit-box; -webkit-box-orient:vertical; -webkit-line-clamp:2; overflow:hidden; overflow-wrap:anywhere; line-height:1.6; }
.log-image-cell :deep(.log-image-preview-cell__button) { width:40px; height:40px; flex-basis:40px; border-radius:8px; }
.log-resolution { font-size:12px; font-weight:600; color:var(--console-text-primary); font-variant-numeric:tabular-nums; overflow-wrap:anywhere; }
.cpa-log-row :deep(.ui-btn) { width:28px; height:28px; min-height:28px; padding:0; border-radius:8px; }
.cpa-log-row :deep(.ui-btn svg) { width:15px; height:15px; }
.cpa-log-row :deep(.log-delete-button) { color:var(--data-red-base); }
.cpa-log-row :deep(.log-delete-button:hover) { background:color-mix(in srgb,var(--data-red-base) 10%,transparent); }
@media(min-width:641px) {
  .log-action-cell { position:sticky; right:0; z-index:1; background:var(--app-surface-strong); }
  .cpa-log-row:hover > .log-action-cell { background:color-mix(in srgb,var(--console-primary-solid) 6%,var(--app-surface-strong)); }
  .cpa-log-row[aria-selected="true"] > .log-action-cell { background:var(--app-accent-soft); }
}
@media(max-width:640px) {
  .cpa-log-row { display:grid; grid-template-columns:28px minmax(0,1fr); gap:12px 8px; padding:14px; }
  .cpa-log-row > td { height:auto; padding:0; min-width:0; }
  .cpa-log-row > td:first-child,.cpa-log-row > td:last-child { padding:0; }
  .log-time-cell { grid-column:2; display:flex; align-items:baseline; gap:8px; }
  .cpa-log-row > td[data-label] { grid-column:1 / -1; display:grid; grid-template-columns:58px minmax(0,1fr); gap:10px; align-items:center; }
  .cpa-log-row > td[data-label]::before { content:attr(data-label); font-size:12px; font-weight:600; color:var(--console-text-regular); }
  .log-request-cell > p + p,.log-execution-cell > div { grid-column:2; }
  .log-action-cell { grid-column:1 / -1; }
}
</style>

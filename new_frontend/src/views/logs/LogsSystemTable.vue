<template>
  <TableShell
    class="logs-system-table"
    :scroll-mode="layoutMode === 'workspace' ? 'contained' : 'page'"
    hover-rows
    sticky-header
    unframed
    :loading="isFetching && visibleLogs.length === 0"
    loading-title="正在加载日志"
    loading-description="正在获取最新日志数据。"
    :show-empty="!isFetching && visibleLogs.length === 0"
    :empty-colspan="13"
    :empty-title="logsLoadError ? '日志加载失败' : '暂无日志'"
    :empty-description="logsLoadError || '换个筛选条件或刷新后再看。'"
    :scroll-class="layoutMode === 'workspace' ? 'max-h-[min(36rem,60dvh)] lg:max-h-none' : ''"
    table-class="cpa-log-table"
    head-class="normal-case tracking-normal"
    style="--table-shell-footer-padding: 0"
  >
    <template #head>
      <tr>
        <th class="log-selection-column">
          <Checkbox
            :model-value="allVisibleLogsSelected"
            :indeterminate="someVisibleLogsSelected"
            :disabled="visibleLogs.length === 0"
            @update:model-value="emit('toggle-select-all-visible', $event)"
          >
            <span class="sr-only">全选当前页日志</span>
          </Checkbox>
        </th>
        <th class="log-time-column">时间</th>
        <th class="log-request-column">请求</th>
        <th class="log-execution-column">执行</th>
        <th class="log-key-column">密钥</th>
        <th class="log-duration-column">耗时</th>
        <th class="log-status-column">状态</th>
        <th class="log-result-column">结果说明</th>
        <th class="log-http-column">HTTP</th>
        <th class="log-error-column">错误码</th>
        <th class="log-image-column">图片</th>
        <th class="log-resolution-column">分辨率</th>
        <th class="log-action-column">操作</th>
      </tr>
    </template>

    <LogsSystemRow
      v-for="item in visibleLogs"
      :key="item.id"
      :item="item"
      :signature="rowSignature(item)"
      :selected="isLogSelected(item.id)"
      :first-image-broken="isPreviewBroken(item.imageUrls[0] || '')"
      @toggle-selection="handleToggleLogSelection"
      @open-detail="emit('open-detail', $event)"
      @request-delete-log="emit('request-delete-log', $event)"
      @image-error="(event, url) => emit('image-error', event, url)"
    />

    <template #footer>
      <ListPagination
        :page="page"
        :page-size="pageSize"
        :layout-mode="layoutMode"
        :total-count="totalCount"
        :page-size-options="systemLogPageSizeOptions"
        unit="条日志"
        :disabled="isFetching"
        @update:page="emit('update:page', $event)"
        @update:page-size="emit('update:pageSize', $event)"
        @update:layout-mode="emit('update:layoutMode', $event)"
      />
    </template>
  </TableShell>
</template>

<script setup lang="ts">
import { Checkbox, TableShell } from 'nanocat-ui'

import ListPagination from '@/components/ai/ListPagination.vue'
import type { SystemLogRow } from '@/api/logs'
import type { ListLayoutMode } from '@/composables/useListLayoutPreference'
import LogsSystemRow from './LogsSystemRow.vue'
import {
  systemLogPageSizeOptions,
  systemLogRowSignature,
} from '@/views/logs/logsView'

const props = defineProps<{
  visibleLogs: SystemLogRow[]
  isFetching: boolean
  logsLoadError: string
  allVisibleLogsSelected: boolean
  someVisibleLogsSelected: boolean
  page: number
  pageSize: number
  layoutMode: ListLayoutMode
  totalCount: number
  isLogSelected: (id: string) => boolean
  isPreviewBroken: (url: string) => boolean
}>()

const emit = defineEmits<{
  (e: 'update:page', value: number): void
  (e: 'update:pageSize', value: number): void
  (e: 'update:layoutMode', value: ListLayoutMode): void
  (e: 'toggle-select-all-visible', checked: boolean): void
  (e: 'toggle-log-selection', id: string, checked: boolean): void
  (e: 'open-detail', item: SystemLogRow): void
  (e: 'request-delete-log', item: SystemLogRow): void
  (e: 'image-error', event: Event, url: string): void
}>()

function rowSignature(item: SystemLogRow) {
  return systemLogRowSignature(item, {
    selected: props.isLogSelected(item.id),
    firstImageBroken: props.isPreviewBroken(item.imageUrls[0] || ''),
  })
}

function handleToggleLogSelection(id: string, checked: boolean) {
  emit('toggle-log-selection', id, checked)
}
</script>

<style scoped>
:deep(.cpa-log-table) { width:100%; min-width:1500px; table-layout:fixed; border-collapse:collapse; }
:deep(.cpa-log-table th) { padding:10px 12px; font-size:11px; font-weight:600; color:var(--console-text-regular); background:var(--app-surface-muted); border-bottom:1px solid var(--console-border); text-align:left; white-space:nowrap; }
:deep(.cpa-log-table .log-selection-column) { width:3%; padding-left:16px; }
:deep(.log-time-column) { width:6%; }
:deep(.log-request-column) { width:13%; }
:deep(.log-execution-column) { width:10%; }
:deep(.log-key-column) { width:7%; }
:deep(.log-duration-column) { width:7%; }
:deep(.log-status-column) { width:5%; }
:deep(.log-result-column) { width:17%; }
:deep(.log-http-column) { width:5%; }
:deep(.log-error-column) { width:9%; }
:deep(.log-image-column) { width:4%; }
:deep(.log-resolution-column) { width:8%; }
:deep(.cpa-log-table .log-action-column) { width:6%; text-align:right; padding-right:16px; }
:deep(.table-shell__footer) { border-top:1px solid var(--console-border); padding:12px 16px; }
:deep(.table-shell__footer > div) { padding-top:0; border-top:0; }
:deep(.table-shell__scroll) { overscroll-behavior:contain; }
.logs-system-table.table-shell--state :deep(.cpa-log-table) { min-width:0; }
.logs-system-table.table-shell--state :deep(thead) { display:none; }
@media(min-width:641px) {
  :deep(.cpa-log-table .log-action-column) { position:sticky; right:0; z-index:2; }
}
@media(max-width:640px) {
  :deep(.cpa-log-table) { display:block; min-width:0; }
  :deep(.cpa-log-table thead) { display:block; border-bottom:1px solid var(--console-border); }
  :deep(.cpa-log-table thead tr) { display:block; }
  :deep(.cpa-log-table th:not(:first-child)) { display:none; }
  :deep(.cpa-log-table .log-selection-column) { display:block; width:auto; border:0; }
  :deep(.log-selection-column span.sr-only) { position:static; width:auto; height:auto; clip:auto; clip-path:none; white-space:nowrap; }
  :deep(.cpa-log-table tbody) { display:block; }
  :deep(.table-shell__state-row),:deep(.table-shell__state-row td) { display:block; width:100%; }
}
</style>

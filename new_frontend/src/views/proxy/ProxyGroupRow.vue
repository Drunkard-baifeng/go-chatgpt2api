<template>
  <section class="proxy-group" :class="{ 'is-disabled': !group.enabled }" :aria-label="group.name || group.id">
    <header class="proxy-group-header">
      <div class="proxy-group-info">
        <div class="proxy-group-title"><h3>{{ group.name || group.id }}</h3><StateBadge :tone="group.enabled ? 'success' : 'muted'" size="xs" shape="rounded">{{ group.enabled ? '启用' : '停用' }}</StateBadge><span>{{ group.nodes.length }} 个节点</span></div>
        <p v-if="group.notes" class="proxy-group-notes">{{ group.notes }}</p>
        <div class="proxy-group-reference"><button type="button" :title="`复制 ${group.reference_text}`" @click="emit('copy-reference', group)"><Icon icon="lucide:copy" aria-hidden="true" />{{ group.reference_text }}</button><span>{{ group.references.join('、') || '暂无引用' }}</span></div>
      </div>
      <div class="proxy-group-actions">
        <Button size="xs" variant="outline" :disabled="Boolean(testingKey) || !group.nodes.some(node => node.enabled)" @click="emit('action', group, 'test-all')">{{ groupTesting ? '测试中…' : '批量测出口' }}</Button>
        <Button size="xs" variant="outline" :disabled="Boolean(testingKey)" @click="emit('add-node', group)">添加节点</Button>
        <Button size="xs" variant="outline" :disabled="Boolean(testingKey)" @click="emit('edit', group)">编辑组</Button>
        <span><ActionMenu label="更多" :items="actionItems" align="right" size="sm" :trigger-min-width="80" :disabled="Boolean(testingKey)" @select="emit('action', group, $event)" /></span>
      </div>
    </header>
    <p v-if="testSummary" class="proxy-group-summary"><StateBadge :tone="testSummary.tone" size="xs" shape="rounded">{{ testSummary.label }}</StateBadge><span>{{ testSummary.message }}</span></p>
    <table class="proxy-nodes-table">
      <colgroup><col class="col-name" /><col class="col-status" /><col class="col-url" /><col class="col-ip" /><col class="col-country" /><col class="col-http" /><col class="col-latency" /><col class="col-limit" /><col class="col-action" /></colgroup>
      <thead><tr><th>节点</th><th>状态</th><th>代理地址</th><th>出口 IP</th><th>地区</th><th>HTTP</th><th>耗时</th><th>图片并发</th><th class="align-right">操作</th></tr></thead>
      <tbody>
        <tr v-for="node in group.nodes" :key="node.id" :class="{ 'is-disabled': !node.enabled }">
          <td data-label="节点"><strong>{{ node.name || node.id }}</strong><p v-if="node.notes" class="node-notes">{{ node.notes }}</p></td>
          <td data-label="状态"><StateBadge :tone="!node.enabled ? 'muted' : isProxyNodeTesting(group, node, testingKey) ? 'info' : nodeTestResult(group, node)?.ok ? 'success' : nodeTestResult(group, node) ? 'danger' : 'muted'" size="xs" shape="rounded">{{ !node.enabled ? '停用' : isProxyNodeTesting(group, node, testingKey) ? '测试中' : nodeTestResult(group, node)?.ok ? '可用' : nodeTestResult(group, node) ? '失败' : '未测试' }}</StateBadge></td>
          <td data-label="代理地址"><span class="node-address"><span class="node-url">{{ node.url }}</span><span v-if="node.has_credentials" class="node-auth" role="img" aria-label="已配置认证" title="已配置认证"><Icon icon="lucide:key-round" aria-hidden="true" /></span></span><p v-if="nodeTestResult(group, node)?.error" class="node-error">{{ nodeTestResult(group, node)?.error }}</p></td>
          <td data-label="出口 IP" class="node-ip">{{ nodeTestResult(group, node)?.exit_ip || '—' }}</td>
          <td data-label="地区">{{ nodeTestResult(group, node)?.country || '—' }}</td>
          <td data-label="HTTP">{{ nodeTestResult(group, node)?.status || '—' }}</td>
          <td data-label="耗时" class="node-latency">{{ nodeTestResult(group, node) ? nodeTestResult(group, node)?.latency_ms + ' ms' : '—' }}</td>
          <td data-label="图片并发">{{ node.image_concurrency_limit > 0 ? node.image_concurrency_limit : '不限' }}</td>
          <td class="node-actions"><Button size="xs" variant="ghost" :disabled="Boolean(testingKey) || !node.enabled" @click="emit('test-node', group, node)">测出口</Button><Button size="xs" variant="ghost" :disabled="Boolean(testingKey)" @click="emit('edit-node', group, node)">编辑</Button></td>
        </tr>
        <tr v-if="!group.nodes.length"><td colspan="9" class="node-empty">暂无节点，点击“添加节点”配置代理。</td></tr>
      </tbody>
    </table>
  </section>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { Icon } from '@iconify/vue'
import { ActionMenu, Button } from 'nanocat-ui'
import type { ProxyGroup, ProxyNode, ProxyTestResult, ProxyTestSummary } from '@/api/proxy'
import StateBadge from '@/components/ai/StateBadge.vue'
import { isProxyNodeTesting, proxyGroupActionItems } from './proxyView'
const props = defineProps<{
  group: ProxyGroup; testingKey: string; savingGroupId: string; deletingGroupId: string
  testSummary?: ProxyTestSummary
  nodeTestResult: (group: ProxyGroup, node: ProxyNode) => ProxyTestResult | undefined
}>()
const emit = defineEmits<{
  (e: 'copy-reference' | 'edit' | 'add-node', group: ProxyGroup): void
  (e: 'test-node' | 'edit-node', group: ProxyGroup, node: ProxyNode): void
  (e: 'action', group: ProxyGroup, action: string): void
}>()
const groupTesting = computed(() => props.testingKey === `group:${props.group.id}:all`)
const actionItems = computed(() => proxyGroupActionItems(props.group, props.testingKey, props.savingGroupId, props.deletingGroupId).filter(item => item.key !== 'test-all'))
</script>

<style scoped>
.proxy-group {
  --proxy-body-size:12px; --proxy-label-size:12px; --proxy-title-size:14px;
  --proxy-meta-size:11px; --proxy-control-size:12px;
  min-width:0; border-top:1px solid var(--console-border);
}
.proxy-group-header { display:flex; flex-wrap:wrap; justify-content:space-between; align-items:center; gap:12px; padding:16px 20px; }
.proxy-group-info { min-width:0; }
.proxy-group-title,.proxy-group-reference,.proxy-group-actions,.proxy-group-summary { display:flex; align-items:center; flex-wrap:wrap; gap:8px; }
.proxy-group-title h3 { font-size:var(--proxy-title-size); font-weight:700; overflow-wrap:anywhere; }
.proxy-group-title > span,.proxy-group-reference,.proxy-group-notes { font-size:var(--proxy-meta-size); color:var(--console-text-regular); }
.proxy-group-reference { margin-top:6px; }
.proxy-group-reference button { display:inline-flex; align-items:center; gap:5px; color:var(--console-primary-solid); }
.proxy-group-reference svg { width:1em; height:1em; }
.proxy-group-notes { margin-top:5px; overflow-wrap:anywhere; }
.proxy-group-summary { padding:0 20px 12px; font-size:var(--proxy-meta-size); }
.proxy-nodes-table { width:100%; table-layout:fixed; border-collapse:collapse; font-size:var(--proxy-body-size); line-height:1.5; }
.proxy-nodes-table th { text-align:left; color:var(--console-text-regular); background:var(--app-surface-muted); font-size:var(--proxy-label-size); font-weight:650; }
.proxy-nodes-table th,.proxy-nodes-table td { padding:10px 14px; border-bottom:1px solid var(--console-border); overflow-wrap:anywhere; }
.proxy-nodes-table tbody tr:last-child td { border-bottom:0; }
.proxy-nodes-table td { vertical-align:middle; font-variant-numeric:tabular-nums; }
.proxy-nodes-table tbody tr:hover { background:var(--app-surface-muted); }
.proxy-nodes-table strong { font-size:var(--proxy-title-size); font-weight:650; }
.col-name { width:10%; }.col-status { width:8%; }.col-url { width:24%; }.col-ip { width:17%; }.col-country { width:6%; }.col-http { width:6%; }.col-latency { width:9%; }.col-limit { width:8%; }.col-action { width:12%; }
.node-address { display:inline-flex; align-items:baseline; gap:8px; max-width:100%; }
.node-url { min-width:0; color:inherit; }
.node-ip { font-family:var(--font-mono,monospace); font-size:inherit; font-weight:550; }
.node-auth { flex:none; align-self:center; color:var(--console-text-regular); }.node-auth svg { width:1em; height:1em; }
.node-error { margin-top:4px; color:var(--data-red-base); font-size:var(--proxy-meta-size); }
.node-notes { font-size:var(--proxy-meta-size); color:var(--console-text-regular); margin-top:3px; }
.node-actions { text-align:right; white-space:nowrap; }.align-right { text-align:right !important; }
.proxy-group :deep(button) { font-size:var(--proxy-control-size); }
.proxy-group :deep(.state-badge) { font-size:var(--proxy-control-size) !important; font-weight:650; }
.node-empty { text-align:center; color:var(--console-text-regular); padding:24px !important; }
.is-disabled .proxy-group-title h3 { color:var(--console-text-regular); }
@media(max-width:1350px) {
  .proxy-nodes-table colgroup,.proxy-nodes-table thead { display:none; }
  .proxy-nodes-table,.proxy-nodes-table tbody { display:block; }
  .proxy-nodes-table tr { display:grid; grid-template-columns:repeat(4,minmax(0,1fr)); gap:10px 16px; padding:14px 20px; border-bottom:1px solid var(--console-border); }
  .proxy-nodes-table td { display:block; padding:0; border:0; min-width:0; }
  .proxy-nodes-table td::before { content:attr(data-label); display:block; margin-bottom:5px; font:11px var(--font-sans,sans-serif); color:var(--console-text-regular); }
  .proxy-nodes-table td[data-label="代理地址"] { grid-column:span 2; }
  .node-actions { grid-column:span 2; align-self:end; }
}
@media(max-width:640px) {
  .proxy-group-header { padding:14px 16px; align-items:flex-start; }.proxy-group-actions { width:100%; }
  .proxy-nodes-table tr { grid-template-columns:repeat(2,minmax(0,1fr)); padding:14px 16px; }
  .node-actions { border-top:1px solid var(--console-border) !important; padding-top:8px !important; }
}
</style>

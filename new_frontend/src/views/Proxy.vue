<template>
  <div class="cpa-proxy">
    <PagePanel class="proxy-routing-panel">
      <PanelHeader title="出口配置" align="start">
        <template #copy><p class="proxy-section-description">账号个人代理优先，其次是账号组代理，最后使用默认出口。</p></template>
        <template #actions>
          <StateBadge v-if="isDefaultProxyDirty" tone="warning" size="xs" shape="rounded">有未保存的修改</StateBadge>
          <Button size="sm" variant="outline" :disabled="loading || Boolean(testingKey)" @click="loadData"><Icon icon="lucide:refresh-cw" aria-hidden="true" />{{ loading ? '刷新中...' : '刷新' }}</Button>
          <Button size="sm" variant="primary" :disabled="savingDefaultProxy || loading || Boolean(testingKey)" @click="saveDefaultProxy">{{ savingDefaultProxy ? '保存中...' : '保存出口配置' }}</Button>
        </template>
      </PanelHeader>
      <div class="proxy-routing-grid">
        <section class="proxy-route-card">
          <div class="proxy-route-title"><span><Icon icon="lucide:network" aria-hidden="true" /></span><div><h3>默认出口</h3><p>未指定个人或账号组代理时使用</p></div></div>
          <div class="proxy-route-fields">
            <label class="block text-xs">
              <span class="ui-field-label">默认出口模式</span>
              <GroupedSelectMenu
                :model-value="defaultProxyMode"
                :options="defaultProxyModeOptions"
                aria-label="默认出口模式"
                :disabled="Boolean(testingKey)"
                selected-indicator="none"
                block
                @update:model-value="setDefaultProxyMode"
              />
            </label>

            <label v-if="defaultProxyMode === 'group'" class="block text-xs">
              <span class="ui-field-label">默认出口代理组</span>
              <GroupedSelectMenu
                :model-value="selectedDefaultProxyGroupId"
                :options="defaultProxyGroupOptions"
                :disabled="loading || Boolean(testingKey)"
                aria-label="默认出口代理组"
                selected-indicator="none"
                block
                @update:model-value="selectDefaultProxyGroup"
              />
            </label>

            <label v-else-if="defaultProxyMode === 'custom'" class="block text-xs">
              <span class="ui-field-label">自定义代理 URL</span>
              <Input
                :model-value="defaultCustomProxyInput"
                :disabled="Boolean(testingKey)"
                block
                root-class="font-mono"
                placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:7890"
                @update:model-value="setDefaultCustomProxyInput"
              />
            </label>

            <div v-else class="flex min-h-[2.5rem] items-center rounded-lg border border-dashed border-border bg-muted/20 px-3 text-xs text-muted-foreground">
              未指定账号或账号组代理时直连。
            </div>
          </div>
          <ActionRow class="proxy-route-actions" gap="tight">
            <Button size="xs" variant="outline" :disabled="Boolean(testingKey) || !canTestDefaultProxy" @click="testDefaultProxy">
              {{ testingKey === DEFAULT_TEST_KEY ? '测试中...' : '测试默认出口' }}
            </Button>
            <Button size="xs" variant="outline" :disabled="savingDefaultProxy || Boolean(testingKey)" @click="setDefaultProxyDirect">
              设为直连
            </Button>
          </ActionRow>

        </section>
        <section class="proxy-route-card">
          <div class="proxy-route-title"><span><Icon icon="lucide:repeat" aria-hidden="true" /></span><div><h3>备用出口</h3><p>满足条件时重试一次</p></div></div>
            <div class="proxy-route-fields">
              <label class="block text-xs">
                <span class="ui-field-label">备用出口模式</span>
                <GroupedSelectMenu
                  :model-value="fallbackProxyMode"
                  :disabled="Boolean(testingKey)"
                  :options="fallbackProxyModeOptions"
                  aria-label="备用出口模式"
                  selected-indicator="none"
                  block
                  @update:model-value="setFallbackProxyMode"
                />
              </label>

              <label v-if="fallbackProxyMode === 'group'" class="block text-xs">
                <span class="ui-field-label">备用出口代理组</span>
                <GroupedSelectMenu
                  :model-value="selectedFallbackProxyGroupId"
                  :options="fallbackProxyGroupOptions"
                  :disabled="loading || Boolean(testingKey)"
                  aria-label="备用出口代理组"
                  selected-indicator="none"
                  block
                  @update:model-value="selectFallbackProxyGroup"
                />
              </label>

              <label v-else-if="fallbackProxyMode === 'custom'" class="block text-xs">
                <span class="ui-field-label">备用代理 URL</span>
                <Input
                  :model-value="fallbackCustomProxyInput"
                  :disabled="Boolean(testingKey)"
                  block
                  root-class="font-mono"
                  placeholder="http://127.0.0.1:7890 或 socks5://127.0.0.1:7890"
                  @update:model-value="setFallbackCustomProxyInput"
                />
              </label>

              <div v-else class="flex min-h-[2.5rem] items-center rounded-lg border border-dashed border-border bg-muted/20 px-3 text-xs text-muted-foreground">
                {{ fallbackProxyMode === 'direct' ? '早期连接失败时重试直连一次。' : '未启用备用出口。' }}
              </div>
            </div>
            <p class="mt-2 text-xs text-muted-foreground">
              仅图片请求在早期 TLS / 连接超时且尚未收到上游事件时重试一次；生成中断和轮询超时不会切换。
            </p>

        </section>
        <section class="proxy-route-card proxy-route-status">
          <div class="proxy-route-title"><span><Icon icon="lucide:activity" aria-hidden="true" /></span><div><h3>出口状态</h3><p>已保存的配置与最近测试</p></div></div>
          <dl class="proxy-status-facts"><div><dt>当前默认出口</dt><dd>{{ effectiveDefault.label }}</dd></div></dl>
          <p v-if="isDefaultProxyDirty" class="proxy-section-description">测试使用当前选择；保存出口配置后生效。</p>
          <div v-if="defaultTestResult" class="proxy-test-result" aria-live="polite">
            <template v-if="defaultTestResult.summary">
              <StateBadge :tone="defaultTestResult.summary.tone" shape="rounded">{{ defaultTestResult.summary.label }}</StateBadge>
              <dl class="proxy-test-metrics"><div><dt>可用节点</dt><dd>{{ defaultTestResult.summary.succeeded }}<small> / {{ defaultTestResult.summary.total }}</small></dd></div><div><dt>失败节点</dt><dd>{{ defaultTestResult.summary.failed }}</dd></div></dl>
              <p class="proxy-section-description">{{ defaultTestResult.summary.message }}，各节点结果见下方列表。</p>
            </template>
            <template v-else-if="defaultTestResult.result">
              <StateBadge :tone="defaultTestResult.result.ok ? 'success' : 'danger'" shape="rounded">{{ defaultTestResult.result.ok ? '可用' : '失败' }}</StateBadge>
              <dl class="proxy-test-metrics"><div><dt>HTTP 状态</dt><dd>{{ defaultTestResult.result.status || '—' }}</dd></div><div><dt>响应耗时</dt><dd>{{ defaultTestResult.result.latency_ms }}<small> ms</small></dd></div></dl>
              <p v-if="defaultTestResult.result.exit_ip" class="proxy-section-description">出口 IP：{{ defaultTestResult.result.exit_ip }} {{ defaultTestResult.result.country }}</p>
              <p v-if="defaultTestResult.result.error" class="proxy-test-error">{{ defaultTestResult.result.error }}</p>
            </template>
          </div>
          <div v-else class="proxy-test-empty"><Icon icon="lucide:activity" aria-hidden="true" /><strong>尚未测试</strong><p>选择代理出口后，可检测连接是否可用。</p></div>
        </section>
      </div>
    </PagePanel>

    <PagePanel flush class="proxy-groups-panel">
      <PanelHeader title="代理组 / 多出口" class="proxy-groups-header">
        <template #copy>
          <p class="mt-1 text-xs text-muted-foreground">
            一个代理组就是一组多出口节点；图片请求会从未满的节点里随机选择一个，请求结束前固定该出口，出口满了会等待，不会自动绕到直连。
          </p>
        </template>
        <template #actions>
          <Input
            :model-value="groupKeyword"
            block
            root-class="proxy-search"
            placeholder="搜索代理组 / 节点 / 地址"
            @update:model-value="groupKeyword = $event.trim()"
          />
          <Button size="sm" variant="primary" @click="openCreateGroupModal">新建代理组</Button>
        </template>
      </PanelHeader>
      <div v-if="loading && !groups.length" class="proxy-list-state" role="status">正在加载代理组和节点…</div>
      <div v-else-if="!filteredGroups.length" class="proxy-list-state">{{ groupKeyword ? '没有匹配的代理组或节点' : '暂无代理组，点击“新建代理组”添加代理。' }}</div>
      <div v-else class="proxy-group-list">
        <ProxyGroupRow
          v-for="group in filteredGroups"
          :key="group.id"
          :group="group"
          :testing-key="testingKey"
          :saving-group-id="savingGroupId"
          :deleting-group-id="deletingGroupId"
          :test-summary="groupTestSummaries[group.id]"
          :node-test-result="nodeTestResult"
          @copy-reference="copyProxyGroupReference"
          @edit="openEditGroupModal"
          @add-node="openAddNode"
          @edit-node="openEditNode"
          @test-node="testProxyGroupNode"
          @action="handleProxyGroupAction"
        />
      </div>
      <div class="proxy-list-footer"><span>{{ filteredGroups.length }} / {{ groups.length }} 个代理组</span><span>按组展示全部节点</span></div>
    </PagePanel>

    <ModalShell
      :open="showGroupModal"
      panel-class="proxy-editor"
      max-width="min(72rem, 100vw)"
      :aria-label="editingGroupId ? '编辑代理组' : '新建代理组'"
      :z-index="120"
      :close-on-escape="!showNodeImportModal"
      scrollable
      @close="closeGroupModal"
    >
      <ModalHeader
        :title="editingGroupId ? '编辑代理组' : '新建代理组'"
        :close-disabled="savingGroupId === FORM_TEST_KEY || closingGroupModal"
        :subtitle="editingGroupId ? '修改后点击保存，整组配置一起生效' : '填写名称，添加一个或多个代理节点'"
        :bordered="false"
        compact
        @close="closeGroupModal"
      />

      <ModalBody class="proxy-editor-body">
        <div class="proxy-editor-basics">
          <label><span class="ui-field-label">代理组名称</span><Input v-model="groupForm.name" block placeholder="例如：日本代理池" /></label>
          <label><span class="ui-field-label">备注</span><Input v-model="groupForm.notes" block placeholder="可选" /></label>
          <SettingSwitch v-model="groupForm.enabled" inline label="启用代理组" />
        </div>
        <details class="proxy-editor-id">
          <summary>{{ editingGroupId ? '代理组 ID：' + editingGroupId : '自定义代理组 ID（可选，留空自动生成）' }}</summary>
          <label><span class="ui-field-label">代理组 ID</span><Input :model-value="groupForm.id" block :disabled="Boolean(editingGroupId)" placeholder="留空自动生成" @update:model-value="groupForm.id = cleanProxyGroupDraftId($event)" /></label>
        </details>
        <section class="proxy-editor-nodes">
          <div class="proxy-editor-toolbar"><h3>代理节点 <span>{{ groupForm.nodes.length }} 个</span></h3><div><Button size="xs" variant="outline" :disabled="Boolean(testingKey)" @click="openNodeImportModal">批量粘贴</Button><Button size="xs" variant="outline" :disabled="Boolean(testingKey)" @click="addGroupNode">添加一行</Button></div></div>
          <p class="proxy-section-description">支持 HTTP、HTTPS、SOCKS5。已保存的认证不会显示，地址不变时保留；替换认证请填写完整 URL。图片并发 0 表示不限。</p>
          <table class="proxy-editor-table">
            <colgroup><col class="editor-name" /><col class="editor-url" /><col class="editor-limit" /><col class="editor-enabled" /><col class="editor-actions" /></colgroup>
            <thead><tr><th>节点名称</th><th>代理地址</th><th>图片并发</th><th>启用</th><th class="text-right">操作</th></tr></thead>
            <tbody>
              <tr v-for="(node, index) in groupForm.nodes" :id="'proxy-editor-' + node.id" :key="node.id">
                <td data-label="节点名称"><Input v-model="node.name" block :aria-label="'节点 ' + (index + 1) + ' 名称'" /><Input v-model="node.notes" block root-class="proxy-editor-note" placeholder="备注（可选）" :aria-label="'节点 ' + (index + 1) + ' 备注'" /></td>
                <td data-label="代理地址"><Input v-model="node.url" block placeholder="socks5://user:password@host:port" :aria-label="'节点 ' + (index + 1) + ' 代理地址'" autocomplete="off" /><p v-if="draftNodeResult(node)" class="proxy-draft-test" :class="draftNodeResult(node)?.ok ? 'is-success' : 'is-error'">{{ draftNodeResult(node)?.ok ? '可用' : '失败' }} · {{ draftNodeResult(node)?.exit_ip || draftNodeResult(node)?.error || '未返回出口 IP' }}</p></td>
                <td data-label="图片并发"><Input :model-value="String(node.image_concurrency_limit)" block type="number" min="0" max="10000" :aria-label="'节点 ' + (index + 1) + ' 图片并发'" @update:model-value="node.image_concurrency_limit = normalizeImageConcurrencyLimit($event)" /></td>
                <td data-label="启用"><input v-model="node.enabled" type="checkbox" class="proxy-node-enabled" :aria-label="'启用节点 ' + (index + 1)" /></td>
                <td class="proxy-editor-row-actions"><Button size="xs" variant="ghost" :disabled="Boolean(testingKey) || !node.enabled || !node.url" @click="testProxyGroupNode({ id: editingGroupId || groupForm.id || FORM_TEST_KEY, name: groupForm.name }, node)">测出口</Button><Button size="xs" variant="ghost" root-class="text-rose-600" :disabled="Boolean(testingKey)" @click="removeGroupNode(index)">删除</Button></td>
              </tr>
            </tbody>
          </table>
        </section>
      </ModalBody>

      <ModalFooter :bordered="false">
        <Button size="xs" variant="outline" root-class="min-w-14 justify-center" :disabled="savingGroupId === FORM_TEST_KEY || closingGroupModal" @click="closeGroupModal">
          取消
        </Button>
        <Button size="xs" variant="primary" root-class="min-w-14 justify-center" :disabled="savingGroupId === FORM_TEST_KEY || Boolean(testingKey)" @click="saveProxyGroup">
          {{ savingGroupId === FORM_TEST_KEY ? '保存中...' : editingGroupId ? '保存修改' : '创建代理组' }}
        </Button>
      </ModalFooter>
    </ModalShell>

    <ProxyNodeImportModal
      :open="showNodeImportModal"
      :group-name="groupForm.name || groupForm.id"
      :existing-urls="groupNodeImportExistingUrls"
      @close="closeNodeImportModal"
      @apply="applyNodeImport"
    />

  </div>
</template>

<script setup lang="ts">
import { nextTick } from 'vue'
import type { ProxyGroup, ProxyNode } from '@/api/proxy'
import SettingSwitch from './settings/SettingSwitch.vue'
import StateBadge from '@/components/ai/StateBadge.vue'
import { Icon } from '@iconify/vue'
import {
  Button,
  GroupedSelectMenu,
  Input,
} from 'nanocat-ui'
import ActionRow from '@/components/ai/ActionRow.vue'
import ModalBody from '~/components/ui/ModalBody.vue'
import ModalFooter from '~/components/ui/ModalFooter.vue'
import ModalHeader from '~/components/ui/ModalHeader.vue'
import ModalShell from '@/components/ui/ModalShell.vue'
import PagePanel from '~/components/ui/PagePanel.vue'
import PanelHeader from '~/components/ui/PanelHeader.vue'
import { usePageVisibilityReload } from '@/composables/usePageQuery'
import { usePageRuntime } from '@/composables/usePageRuntime'
import {
  defaultProxyModeOptions,
  fallbackProxyModeOptions,
} from '@/views/proxy/proxyView'
import { DEFAULT_TEST_KEY, useProxyDefaultRuntime } from '@/views/proxy/proxyDefaultRuntime'
import {
  FORM_TEST_KEY,
  type ProxyGroupNodeForm,
  cleanProxyGroupDraftId,
  normalizeImageConcurrencyLimit,
  useProxyGroupRuntime,
} from '@/views/proxy/proxyGroupRuntime'
import ProxyGroupRow from './proxy/ProxyGroupRow.vue'
import ProxyNodeImportModal from '@/views/proxy/ProxyNodeImportModal.vue'

defineOptions({ name: 'Proxy' })

const proxyGroupsRuntime = useProxyGroupRuntime()
const savingGroupId = proxyGroupsRuntime.savingGroupId
const deletingGroupId = proxyGroupsRuntime.deletingGroupId
const testingKey = proxyGroupsRuntime.testingKey
const groupTestSummaries = proxyGroupsRuntime.groupTestSummaries
const nodeTestResult = proxyGroupsRuntime.nodeTestResult
const groupKeyword = proxyGroupsRuntime.groupKeyword
const showGroupModal = proxyGroupsRuntime.showGroupModal
const showNodeImportModal = proxyGroupsRuntime.showNodeImportModal
const closingGroupModal = proxyGroupsRuntime.closingGroupModal
const editingGroupId = proxyGroupsRuntime.editingGroupId
const groups = proxyGroupsRuntime.groups
const groupForm = proxyGroupsRuntime.groupForm
const groupNodeImportExistingUrls = proxyGroupsRuntime.groupNodeImportExistingUrls
const filteredGroups = proxyGroupsRuntime.filteredGroups
const updateGroups = proxyGroupsRuntime.updateGroups
const copyProxyGroupReference = proxyGroupsRuntime.copyProxyGroupReference
const openCreateGroupModal = proxyGroupsRuntime.openCreateGroupModal
const openEditGroupModal = proxyGroupsRuntime.openEditGroupModal
const closeGroupModal = proxyGroupsRuntime.closeGroupModal
const openNodeImportModal = proxyGroupsRuntime.openNodeImportModal
const closeNodeImportModal = proxyGroupsRuntime.closeNodeImportModal
async function focusNode(nodeId: string) {
  await nextTick()
  const row = document.getElementById('proxy-editor-' + nodeId)
  row?.scrollIntoView({ block: 'nearest' })
  row?.querySelector<HTMLInputElement>('input')?.focus({ preventScroll: true })
}
function addGroupNode() {
  proxyGroupsRuntime.addGroupNode()
  void focusNode(groupForm.nodes[groupForm.nodes.length - 1].id)
}
function openAddNode(group: ProxyGroup) {
  openEditGroupModal(group)
  addGroupNode()
}
function openEditNode(group: ProxyGroup, node: ProxyNode) {
  openEditGroupModal(group)
  void focusNode(node.id)
}
function draftNodeResult(node: ProxyGroupNodeForm) {
  return proxyGroupsRuntime.draftNodeTestResult(editingGroupId.value || groupForm.id || FORM_TEST_KEY, node)
}
const removeGroupNode = proxyGroupsRuntime.removeGroupNode
const applyNodeImport = proxyGroupsRuntime.applyNodeImport
const saveProxyGroup = proxyGroupsRuntime.saveProxyGroup
const handleProxyGroupAction = proxyGroupsRuntime.handleProxyGroupAction
const testProxyGroupNode = proxyGroupsRuntime.testProxyGroupNode
const pageRuntime = usePageRuntime('proxy')
const PROXY_DATA_REQUEST_KEY = 'proxy:data'
const proxyDefaultRuntime = useProxyDefaultRuntime({
  runtime: pageRuntime,
  requestKey: PROXY_DATA_REQUEST_KEY,
  groups,
  testingKey,
  groupTestSummaries,
  updateGroups,
  applyGroupTestResponse: proxyGroupsRuntime.applyGroupTestResponse,
})
const loading = proxyDefaultRuntime.loading
const savingDefaultProxy = proxyDefaultRuntime.savingDefaultProxy
const defaultProxyMode = proxyDefaultRuntime.defaultProxyMode
const selectedDefaultProxyGroupId = proxyDefaultRuntime.selectedDefaultProxyGroupId
const defaultCustomProxyInput = proxyDefaultRuntime.defaultCustomProxyInput
const fallbackProxyMode = proxyDefaultRuntime.fallbackProxyMode
const selectedFallbackProxyGroupId = proxyDefaultRuntime.selectedFallbackProxyGroupId
const fallbackCustomProxyInput = proxyDefaultRuntime.fallbackCustomProxyInput
const defaultTestResult = proxyDefaultRuntime.defaultTestResult
const defaultProxyGroupOptions = proxyDefaultRuntime.defaultProxyGroupOptions
const fallbackProxyGroupOptions = proxyDefaultRuntime.fallbackProxyGroupOptions
const effectiveDefault = proxyDefaultRuntime.effectiveDefault
const canTestDefaultProxy = proxyDefaultRuntime.canTestDefaultProxy
const isDefaultProxyDirty = proxyDefaultRuntime.isDefaultProxyDirty
const setDefaultProxyMode = proxyDefaultRuntime.setDefaultProxyMode
const setFallbackProxyMode = proxyDefaultRuntime.setFallbackProxyMode
const selectDefaultProxyGroup = proxyDefaultRuntime.selectDefaultProxyGroup
const selectFallbackProxyGroup = proxyDefaultRuntime.selectFallbackProxyGroup
const setDefaultCustomProxyInput = proxyDefaultRuntime.setDefaultCustomProxyInput
const setFallbackCustomProxyInput = proxyDefaultRuntime.setFallbackCustomProxyInput
const loadData = proxyDefaultRuntime.loadData
const saveDefaultProxy = proxyDefaultRuntime.saveDefaultProxy
const setDefaultProxyDirect = proxyDefaultRuntime.setDefaultProxyDirect
const testDefaultProxy = proxyDefaultRuntime.testDefaultProxy

function deactivateProxyView() {
  proxyDefaultRuntime.invalidate()
}

function shouldSkipProxyRefresh() {
  return Boolean(
    showGroupModal.value
    || savingDefaultProxy.value
    || savingGroupId.value
    || testingKey.value
    || isDefaultProxyDirty.value,
  )
}

usePageVisibilityReload({
  runtime: pageRuntime,
  invalidate: deactivateProxyView,
  reload: loadData,
  shouldReload: () => !shouldSkipProxyRefresh(),
})

pageRuntime.onActivate(({ initial }) => {
  if (initial) {
    void loadData()
    return
  }
  if (shouldSkipProxyRefresh()) return
  void loadData()
})

pageRuntime.onDeactivate(() => {
  deactivateProxyView()
})
</script>

<style scoped>
/* CPA configuration sections, compact monitoring rows and account detail groups. */
.cpa-proxy { display:flex; flex-direction:column; gap:20px; min-width:0; }
.proxy-routing-panel { display:flex; flex-direction:column; gap:20px; }
.proxy-section-description { margin-top:5px; font-size:12px; line-height:1.6; color:var(--console-text-regular); }
.proxy-routing-grid { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr) minmax(240px,.65fr); gap:16px; }
.proxy-route-card { min-width:0; display:flex; flex-direction:column; gap:16px; padding:16px; border:1px solid var(--console-border); border-radius:12px; background:var(--console-glass); }
.proxy-route-title { display:flex; align-items:center; gap:10px; }
.proxy-route-title > span { display:grid; place-items:center; width:30px; height:30px; flex:none; border:1px solid var(--console-border); border-radius:10px; background:var(--app-surface-muted); color:var(--console-primary-solid); }
.proxy-route-title svg { width:15px; height:15px; }
.proxy-route-title h3 { font-size:14px; font-weight:600; }
.proxy-route-title p { margin-top:3px; color:hsl(var(--muted-foreground)); font-size:11px; }
.proxy-route-fields { display:grid; grid-template-columns:minmax(0,1fr); gap:12px; }
.proxy-route-actions { margin-top:auto; }
.proxy-status-facts div { display:flex; flex-wrap:wrap; align-items:baseline; justify-content:space-between; gap:8px; padding-bottom:12px; border-bottom:1px solid var(--console-border); }
.proxy-status-facts dt,.proxy-test-metrics dt { font-size:11px; color:hsl(var(--muted-foreground)); }
.proxy-status-facts dd { font-size:13px; font-weight:600; overflow-wrap:anywhere; }
.proxy-test-result { display:grid; gap:12px; }
.proxy-test-metrics { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:12px; }
.proxy-test-metrics dd { margin-top:5px; font-size:22px; font-weight:600; font-variant-numeric:tabular-nums; }
.proxy-test-metrics small { font-size:11px; color:hsl(var(--muted-foreground)); }
.proxy-test-error { font-size:12px; color:var(--data-red-base); overflow-wrap:anywhere; }
.proxy-test-empty { display:flex; flex-direction:column; align-items:center; justify-content:center; gap:8px; flex:1; padding:12px 0; text-align:center; color:hsl(var(--muted-foreground)); }
.proxy-test-empty svg { width:24px; height:24px; color:var(--data-slate-base); }
.proxy-test-empty strong { font-size:12px; font-weight:500; }
.proxy-test-empty p { font-size:11px; }
.proxy-groups-header { padding:20px; }
:deep(.proxy-search) { width:320px; max-width:100%; }
.proxy-list-state { padding:32px 20px; text-align:center; color:var(--console-text-regular); font-size:13px; }
.proxy-group-list { min-width:0; }
.proxy-list-footer { display:flex; justify-content:space-between; gap:12px; padding:12px 20px; border-top:1px solid var(--console-border); font-size:12px; color:var(--console-text-regular); }
.proxy-editor-body { display:flex; flex-direction:column; gap:18px; }
.proxy-editor-basics { display:grid; grid-template-columns:minmax(0,1fr) minmax(0,1fr) auto; gap:14px; align-items:end; }
.proxy-editor-id { color:var(--console-text-regular); font-size:12px; }
.proxy-editor-id summary { cursor:pointer; }
.proxy-editor-id label { display:block; max-width:24rem; margin-top:10px; }
.proxy-editor-toolbar { display:flex; align-items:center; justify-content:space-between; gap:10px; }
.proxy-editor-toolbar h3 { font-size:14px; font-weight:650; }.proxy-editor-toolbar h3 span { font-size:12px; font-weight:400; color:var(--console-text-regular); }
.proxy-editor-toolbar > div { display:flex; gap:8px; }
.proxy-editor-table { width:100%; table-layout:fixed; border-collapse:collapse; margin-top:12px; font-size:12px; }
.proxy-editor-table th,.proxy-editor-table td { padding:10px 8px; border-bottom:1px solid var(--console-border); text-align:left; vertical-align:top; }
.proxy-editor-table th { font-size:12px; font-weight:600; color:var(--console-text-regular); background:var(--app-surface-muted); }
.editor-name { width:22%; }.editor-url { width:auto; }.editor-limit { width:90px; }.editor-enabled { width:62px; }.editor-actions { width:132px; }
.proxy-editor-row-actions { white-space:nowrap; text-align:right !important; }
.proxy-node-enabled { width:18px; height:18px; margin-top:7px; accent-color:var(--console-primary-solid); cursor:pointer; }
.proxy-editor-note { margin-top:6px; }.proxy-draft-test { margin-top:6px; font-size:12px; overflow-wrap:anywhere; }.proxy-draft-test.is-success { color:var(--data-green-base); }.proxy-draft-test.is-error { color:var(--data-red-base); }
@media(max-width:700px) {
  .proxy-editor-basics { grid-template-columns:minmax(0,1fr); }
  .proxy-editor-table colgroup,.proxy-editor-table thead { display:none; }
  .proxy-editor-table,.proxy-editor-table tbody { display:block; }
  .proxy-editor-table tr { display:grid; grid-template-columns:1fr 1fr; gap:12px; padding:14px 0; border-bottom:1px solid var(--console-border); }
  .proxy-editor-table td { display:block; padding:0; border:0; min-width:0; }
  .proxy-editor-table td::before { content:attr(data-label); display:block; margin-bottom:6px; color:var(--console-text-regular); }
  .proxy-editor-table td[data-label="节点名称"],.proxy-editor-table td[data-label="代理地址"],.proxy-editor-row-actions { grid-column:1 / -1; }
}
@media(max-width:1200px) { .proxy-routing-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } .proxy-route-status { grid-column:1 / -1; } }
@media(max-width:640px) {
  .cpa-proxy { gap:16px; }
  .proxy-routing-grid { grid-template-columns:minmax(0,1fr); }
  .proxy-groups-header { padding:16px; }
  :deep(.proxy-search) { width:100%; }
}
</style>

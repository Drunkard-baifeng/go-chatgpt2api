import { computed, reactive, ref } from 'vue'

import {
  proxyApi,
  type ProxyGroup,
  type ProxyNode,
  type ProxyNodeImportNode,
  type ProxyNodeImportResult,
  type ProxyTestResult,
  type ProxyGroupTestResponse,
  type ProxyTestSummary,
} from '@/api/proxy'
import { useConfirmDialog } from '@/composables/useConfirmDialog'
import { useToast } from '@/composables/useToast'
import { errorMessage, prefixedErrorMessage } from '@/lib/errorMessage'
import {
  proxyGroupReference,
  proxyTestToastType,
} from '@/views/proxy/proxyView'

export type ProxyGroupNodeForm = {
  id: string
  name: string
  url: string
  enabled: boolean
  image_concurrency_limit: number
  notes: string
}

export type ProxyGroupForm = {
  id: string
  name: string
  enabled: boolean
  notes: string
  nodes: ProxyGroupNodeForm[]
}

export const FORM_TEST_KEY = '__form__'

const DEFAULT_PROXY_NODE_IMAGE_CONCURRENCY = 30

function createGeneratedId(prefix: string) {
  let suffix = ''
  try {
    suffix = globalThis.crypto?.randomUUID?.().replace(/-/g, '').slice(0, 10) || ''
  } catch {
    suffix = ''
  }
  if (!suffix) {
    suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 8)}`.slice(0, 10)
  }
  return `${prefix}-${suffix}`
}

export function cleanProxyGroupDraftId(value: string) {
  return String(value || '').trim()
}

export function normalizeImageConcurrencyLimit(value: unknown) {
  const parsed = Number(value)
  if (!Number.isFinite(parsed)) return 0
  return Math.max(0, Math.min(10000, Math.floor(parsed)))
}

function createDefaultNode(index = 0): ProxyGroupNodeForm {
  return {
    id: createGeneratedId('node'),
    name: `出口 ${index + 1}`,
    url: '',
    enabled: true,
    image_concurrency_limit: DEFAULT_PROXY_NODE_IMAGE_CONCURRENCY,
    notes: '',
  }
}

function createDefaultGroupForm(): ProxyGroupForm {
  return {
    id: '',
    name: '',
    enabled: true,
    notes: '',
    nodes: [createDefaultNode(0)],
  }
}

function normalizeGroupNode(item: ProxyGroupNodeForm | ProxyNode, index: number): ProxyGroupNodeForm {
  const id = cleanProxyGroupDraftId(item.id || '') || createGeneratedId('node')
  return {
    id,
    name: String(item.name || `出口 ${index + 1}`).trim(),
    url: String(item.url || '').trim(),
    enabled: item.enabled !== false,
    image_concurrency_limit: normalizeImageConcurrencyLimit(item.image_concurrency_limit ?? DEFAULT_PROXY_NODE_IMAGE_CONCURRENCY),
    notes: String(item.notes || '').trim(),
  }
}

function groupFormSignature(form: ProxyGroupForm) {
  return JSON.stringify({
    id: cleanProxyGroupDraftId(form.id),
    name: form.name.trim(),
    enabled: form.enabled !== false,
    notes: form.notes.trim(),
    nodes: form.nodes.map((node) => ({
      id: cleanProxyGroupDraftId(node.id),
      name: node.name.trim(),
      url: node.url.trim(),
      enabled: node.enabled !== false,
      image_concurrency_limit: normalizeImageConcurrencyLimit(node.image_concurrency_limit),
      notes: node.notes.trim(),
    })),
  })
}

export function mergeImportedProxyNodes(
  currentNodes: readonly ProxyGroupNodeForm[],
  nodes: readonly ProxyNodeImportNode[],
) {
  const retainedNodes = currentNodes.length === 1 && !currentNodes[0].url.trim()
    ? []
    : [...currentNodes]
  const importedNodes = nodes.map((node, index) => ({
    ...createDefaultNode(retainedNodes.length + index),
    url: node.url,
    image_concurrency_limit: node.image_concurrency_limit,
  }))
  return [...retainedNodes, ...importedNodes]
}

export function useProxyGroupRuntime() {
  const toast = useToast()
  const confirmDialog = useConfirmDialog()
  const savingGroupId = ref('')
  const deletingGroupId = ref('')
  const testingKey = ref('')
  const groupKeyword = ref('')
  const showGroupModal = ref(false)
  const showNodeImportModal = ref(false)
  const closingGroupModal = ref(false)
  const editingGroupId = ref('')
  const groups = ref<ProxyGroup[]>([])
  const testResults = reactive<Record<string, ProxyTestResult>>({})
  const testedDraftUrls = new Map<string, string>()
  const groupTestSummaries = reactive<Record<string, ProxyTestSummary>>({})
  const groupForm = reactive<ProxyGroupForm>(createDefaultGroupForm())
  const groupFormBaseline = ref(groupFormSignature(groupForm))
  const isGroupFormDirty = computed(() => (
    showGroupModal.value && groupFormSignature(groupForm) !== groupFormBaseline.value
  ))
  const groupNodeImportExistingUrls = computed(() => (
    groupForm.nodes.map((node) => node.url.trim()).filter(Boolean)
  ))

  const filteredGroups = computed(() => {
    const query = groupKeyword.value.trim().toLowerCase()
    const rows = [...groups.value].sort((left, right) => (
      (left.name || left.id).localeCompare(right.name || right.id, 'zh-Hans-CN')
    ))
    if (!query) return rows
    return rows.filter((item) => [
      item.id,
      item.name,
      item.notes,
      ...item.nodes.flatMap((node) => [node.id, node.name, node.url, node.notes]),
    ].some((value) => String(value || '').toLowerCase().includes(query)))
  })

  function updateGroups(items: ProxyGroup[]) {
    for (const group of groups.value) clearGroupTests(group.id)
    groups.value = Array.isArray(items) ? [...items] : []
  }

  function clearGroupTests(groupId: string) {
    delete groupTestSummaries[groupId]
    for (const key of Object.keys(testResults)) {
      if (key.startsWith(`group:${groupId}:`)) {
        delete testResults[key]
        testedDraftUrls.delete(key)
      }
    }
  }

  function upsertGroup(item: ProxyGroup) {
    clearGroupTests(item.id)
    const index = groups.value.findIndex((group) => group.id === item.id)
    if (index < 0) {
      groups.value = [...groups.value, item]
      return
    }
    const next = [...groups.value]
    next[index] = item
    groups.value = next
  }

  async function copyText(value: string, message = '已复制') {
    const text = String(value || '').trim()
    if (!text) return
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(text)
      } else {
        const input = document.createElement('textarea')
        input.value = text
        input.setAttribute('readonly', 'readonly')
        input.style.position = 'fixed'
        input.style.opacity = '0'
        document.body.appendChild(input)
        input.select()
        document.execCommand('copy')
        document.body.removeChild(input)
      }
      toast.success(message)
    } catch {
      toast.error('复制失败')
    }
  }

  function copyProxyGroupReference(group: Pick<ProxyGroup, 'id' | 'reference_text'>) {
    void copyText(proxyGroupReference(group), '代理组引用已复制')
  }

  function resetGroupForm() {
    clearGroupTests(FORM_TEST_KEY)
    showNodeImportModal.value = false
    editingGroupId.value = ''
    Object.assign(groupForm, createDefaultGroupForm())
    groupFormBaseline.value = groupFormSignature(groupForm)
  }

  function openCreateGroupModal() {
    resetGroupForm()
    showGroupModal.value = true
  }

  function openEditGroupModal(group: ProxyGroup) {
    editingGroupId.value = group.id
    Object.assign(groupForm, {
      id: group.id,
      name: group.name || group.id,
      enabled: group.enabled !== false,
      notes: group.notes || '',
      nodes: group.nodes.length ? group.nodes.map((node, index) => normalizeGroupNode(node, index)) : [createDefaultNode(0)],
    })
    groupFormBaseline.value = groupFormSignature(groupForm)
    showGroupModal.value = true
  }

  function finishCloseGroupModal() {
    showGroupModal.value = false
    resetGroupForm()
  }

  async function closeGroupModal() {
    if (savingGroupId.value === FORM_TEST_KEY || closingGroupModal.value) return
    if (isGroupFormDirty.value) {
      closingGroupModal.value = true
      try {
        const confirmed = await confirmDialog.ask({
          title: '放弃代理组草稿',
          message: '当前代理组有未保存的修改，关闭后这些内容将丢失。是否继续？',
          confirmText: '放弃修改',
          cancelText: '继续编辑',
        })
        if (!confirmed) return
      } finally {
        closingGroupModal.value = false
      }
    }
    finishCloseGroupModal()
  }

  function openNodeImportModal() {
    showNodeImportModal.value = true
  }

  function closeNodeImportModal() {
    showNodeImportModal.value = false
  }

  function addGroupNode() {
    groupForm.nodes.push(createDefaultNode(groupForm.nodes.length))
  }

  function removeGroupNode(index: number) {
    if (groupForm.nodes.length <= 1) {
      groupForm.nodes = [createDefaultNode(0)]
      return
    }
    groupForm.nodes.splice(index, 1)
  }

  function applyNodeImport(result: ProxyNodeImportResult) {
    if (result.nodes.length) {
      groupForm.nodes = mergeImportedProxyNodes(groupForm.nodes, result.nodes)
    }
  }

  async function saveProxyGroup() {
    if (savingGroupId.value || testingKey.value) return
    const groupName = groupForm.name.trim()
    if (!groupName) {
      toast.warning('请填写代理组名称')
      return
    }
    const id = cleanProxyGroupDraftId(editingGroupId.value || groupForm.id) || createGeneratedId('pg')
    if (groupForm.nodes.some(node => !node.url.trim())) {
      toast.warning('请填写每个节点的代理地址，或删除空白节点')
      return
    }
    const nodes = groupForm.nodes
      .map((node, index) => normalizeGroupNode(node, index))
      .filter((node) => node.url)
    if (!nodes.length) {
      toast.warning('请至少填写一个代理节点地址')
      return
    }
    savingGroupId.value = FORM_TEST_KEY
    try {
      const wasEditing = Boolean(editingGroupId.value)
      const response = await proxyApi.saveGroup({
        id,
        name: groupName,
        enabled: groupForm.enabled,
        notes: groupForm.notes.trim(),
        nodes,
        create_only: !editingGroupId.value,
      })
      upsertGroup(response.group)
      savingGroupId.value = ''
      finishCloseGroupModal()
      toast.success(wasEditing ? '代理组已更新' : '代理组已创建')
    } catch (error) {
      toast.error(prefixedErrorMessage('保存代理组失败', error))
    } finally {
      savingGroupId.value = ''
    }
  }

  async function toggleProxyGroup(group: ProxyGroup) {
    const nextEnabled = !group.enabled
    const confirmed = await confirmDialog.ask({
      title: nextEnabled ? '确认启用代理组' : '确认停用代理组',
      message: `即将${nextEnabled ? '启用' : '停用'}代理组 ${group.name || group.id}。绑定到该组的账号组会受到影响，是否继续？`,
      confirmText: nextEnabled ? '启用' : '停用',
      cancelText: '取消',
    })
    if (!confirmed) return

    savingGroupId.value = group.id
    try {
      const response = await proxyApi.saveGroup({
        id: group.id,
        enabled: nextEnabled,
      })
      upsertGroup(response.group)
      toast.success(`代理组 ${group.name || group.id} 已${group.enabled ? '停用' : '启用'}`)
    } catch (error) {
      toast.error(prefixedErrorMessage('切换代理组失败', error))
    } finally {
      savingGroupId.value = ''
    }
  }

  async function deleteProxyGroup(group: ProxyGroup) {
    if (group.can_delete === false) return

    const confirmed = await confirmDialog.ask({
      title: '删除代理组',
      message: `确认删除代理组 ${group.name || group.id}？该代理组当前未被任何出口、账号组或账号引用，删除后无法恢复。`,
      confirmText: '确认删除',
      cancelText: '取消',
    })
    if (!confirmed) return

    deletingGroupId.value = group.id
    try {
      const response = await proxyApi.deleteGroup(group.id)
      clearGroupTests(response.deleted_id)
      groups.value = groups.value.filter((item) => item.id !== response.deleted_id)
      toast.success('代理组已删除')
    } catch (error) {
      toast.error(prefixedErrorMessage('删除代理组失败', error))
    } finally {
      deletingGroupId.value = ''
    }
  }

  function handleProxyGroupAction(group: ProxyGroup, action: string) {
    if (action === 'test-all') void testProxyGroupAll(group)
    if (action === 'toggle-enabled') void toggleProxyGroup(group)
    if (action === 'delete') void deleteProxyGroup(group)
  }

  async function testProxyGroupNode(group: Pick<ProxyGroup, 'id' | 'name'>, node: ProxyGroupNodeForm | ProxyNode) {
    if (testingKey.value) return
    const value = node.url.trim()
    if (!value) {
      toast.warning(`${node.name || node.id}：请先填写代理地址`)
      return
    }
    const savedNode = groups.value.find(item => item.id === group.id)?.nodes.find(item => item.id === node.id)
    if (!node.enabled || (savedNode?.enabled === false && savedNode.url === value)) {
      toast.warning('请先保存并启用该节点，再测出口')
      return
    }
    const groupLabel = group.name || (group.id === FORM_TEST_KEY ? '当前草稿' : group.id)
    const confirmed = await confirmDialog.ask({
      title: '确认测试代理节点',
      message: `即将使用代理组 ${groupLabel} 的节点 ${node.name || node.id} 发起外部网络测试请求。请确认当前允许测试该代理连接。`,
      confirmText: '开始测试',
      cancelText: '取消',
    })
    if (!confirmed) return

    const groupKey = group.id || FORM_TEST_KEY
    const key = `group:${groupKey}:${node.id}`
    testingKey.value = key
    delete groupTestSummaries[groupKey]
    try {
      const response = await proxyApi.testGroup(savedNode?.url === value
        ? { id: group.id, node_id: node.id }
        : { url: value })
      const result = response.result || response.results?.[0]?.result
      if (result) {
        testResults[key] = result
        testedDraftUrls.set(key, value)
      }
      if (result?.ok) toast.success(`节点检测通过，耗时 ${result.latency_ms}ms`)
      else toast.warning(result?.error || '节点检测失败')
    } catch (error) {
      const message = errorMessage(error, '节点检测失败')
      testResults[key] = {
        ok: false,
        status: 0,
        latency_ms: 0,
        error: message,
      }
      testedDraftUrls.set(key, value)
      toast.error(message)
    } finally {
      testingKey.value = ''
    }
  }

  async function testProxyGroupAll(group: ProxyGroup) {
    if (testingKey.value) return
    const enabledCount = group.nodes.filter(node => node.enabled).length
    if (!enabledCount) {
      toast.warning('该组没有启用的节点')
      return
    }
    const confirmed = await confirmDialog.ask({
      title: '确认测试代理组',
      message: `即将测试代理组 ${group.name || group.id} 内的 ${enabledCount} 个启用节点。每个节点都会发起外部网络测试请求，是否继续？`,
      confirmText: '开始测试',
      cancelText: '取消',
    })
    if (!confirmed) return

    const key = `group:${group.id}:all`
    testingKey.value = key
    delete groupTestSummaries[group.id]
    try {
      const response = await proxyApi.testGroup({ id: group.id })
      applyGroupTestResponse(group.id, response)
      toast[proxyTestToastType(response.summary.tone)](response.summary.message)
    } catch (error) {
      toast.error(errorMessage(error, '代理组检测失败'))
    } finally {
      testingKey.value = ''
    }
  }

  function applyGroupTestResponse(groupId: string, response: ProxyGroupTestResponse) {
    groupTestSummaries[groupId] = response.summary
    for (const item of response.results) {
      if (item.node_id) {
        const key = `group:${groupId}:${item.node_id}`
        testResults[key] = item.result
        testedDraftUrls.delete(key)
      }
    }
  }

  function nodeTestResult(group: Pick<ProxyGroup, 'id'>, node: ProxyNode): ProxyTestResult | undefined {
    const key = `group:${group.id}:${node.id}`
    const result = testResults[key]
    if (result && (!testedDraftUrls.has(key) || testedDraftUrls.get(key) === node.url.trim())) return result
    if (node.health.state === 'unknown') return undefined
    return {
      ok: node.health.state === 'healthy', status: node.health.status || 0,
      latency_ms: node.health.latency_ms || 0, error: node.health.error,
      exit_ip: node.health.exit_ip, country: node.health.country,
      checked_at: node.health.checked_at || undefined,
    }
  }

  function draftNodeTestResult(groupId: string, node: ProxyGroupNodeForm) {
    const key = `group:${groupId}:${node.id}`
    return testedDraftUrls.get(key) === node.url.trim() ? testResults[key] : undefined
  }

  return {
    savingGroupId,
    deletingGroupId,
    testingKey,
    groupTestSummaries,
    applyGroupTestResponse,
    nodeTestResult,
    draftNodeTestResult,
    groupKeyword,
    showGroupModal,
    showNodeImportModal,
    closingGroupModal,
    editingGroupId,
    groups,
    groupForm,
    groupNodeImportExistingUrls,
    filteredGroups,
    updateGroups,
    copyProxyGroupReference,
    openCreateGroupModal,
    openEditGroupModal,
    closeGroupModal,
    openNodeImportModal,
    closeNodeImportModal,
    addGroupNode,
    removeGroupNode,
    applyNodeImport,
    saveProxyGroup,
    handleProxyGroupAction,
    testProxyGroupNode,
  }
}

<template>
  <div class="app-shell cpa-shell min-h-screen">
    <Transition name="route-progress">
      <div
        v-if="routeProgressPhase !== 'idle'"
        class="shell-route-progress pointer-events-none fixed inset-x-0 top-0 z-[70] h-0.5 overflow-hidden"
        role="progressbar"
        aria-label="页面加载中"
      >
        <div
          class="shell-route-progress-bar h-full w-full bg-primary"
          :class="{ 'shell-route-progress-bar--finishing': routeProgressPhase === 'finishing' }"
        ></div>
      </div>
    </Transition>
    <div class="flex min-h-screen flex-col lg:flex-row">
      <div
        v-if="isSidebarOpen && isMobileViewport"
        class="fixed inset-0 z-30 bg-black/20 lg:hidden"
        aria-hidden="true"
        @click="closeSidebar"
      ></div>
      <aside
        ref="sidebarRef"
        class="shell-sidebar fixed inset-y-0 left-0 z-40 w-64 -translate-x-full overflow-hidden bg-card border-r border-border
               transition-[transform,width] duration-200 ease-out will-change-[transform,width] transform-gpu flex flex-col lg:static lg:translate-x-0 lg:w-[var(--sidebar-width)] lg:bg-card
               lg:border-b-0 lg:border-r lg:sticky lg:top-0 lg:h-screen"
        :class="[isSidebarOpen ? 'translate-x-0' : '', { 'sidebar--rail': isSidebarRail }]"
        :style="sidebarStyle"
        :aria-hidden="isMobileViewport && !isSidebarOpen ? 'true' : undefined"
        :inert="isMobileViewport && !isSidebarOpen"
        tabindex="-1"
        @keydown="handleSidebarKeydown"
      >
        <div class="cpa-brand">
          <RouterLink :to="authStore.homeRoute" aria-label="ChatGPT2API 首页" class="cpa-brand-link">
            <svg viewBox="0 0 32 32" class="cpa-brand-mark" fill="none" aria-hidden="true">
              <path d="M16 3 28 10v12L16 29 4 22V10L16 3Z" stroke="currentColor" stroke-width="2.5" />
              <path d="m10 12 6-3 6 3v8l-6 3-6-3v-8Z" fill="currentColor" opacity=".16" />
              <path d="m12 12-4 4 4 4m8-8 4 4-4 4" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <span class="sidebar-label cpa-brand-copy"><strong>ChatGPT2API</strong></span>
          </RouterLink>
        </div>

        <nav id="app-sidebar-navigation" class="sidebar-nav-scroll min-h-0 flex-1 overflow-x-hidden overflow-y-auto" aria-label="主导航">
          <div v-for="(group, groupIndex) in visibleMenuGroups" :key="groupIndex" class="cpa-nav-group">
            <div v-if="groupIndex > 0" class="cpa-nav-divider" aria-hidden="true"></div>
            <RouterLink
              v-for="item in group"
              :key="item.path"
              :to="item.path"
              class="shell-nav-item"
              :class="{ 'cpa-nav-active': isNavVisuallyActive(item.path) }"
              :aria-label="item.label"
              :aria-current="isNavActive(item.path) ? 'page' : undefined"
              :aria-busy="isNavPending(item.path) ? 'true' : undefined"
              @mouseenter="prefetchRouteView(item.path)"
              @focus="prefetchRouteView(item.path)"
              @click="handleNavClick(item.path)"
            >
              <Tooltip v-if="isSidebarRail" :text="item.label" placement="right">
                <span class="shell-nav-icon"><Icon :icon="item.icon" aria-hidden="true" /></span>
              </Tooltip>
              <span v-else class="shell-nav-icon"><Icon :icon="item.icon" aria-hidden="true" /></span>
              <span class="sidebar-label">{{ item.label }}</span>
            </RouterLink>
          </div>
        </nav>
      </aside>

      <main
        class="cpa-main relative min-w-0 flex-1 lg:ml-0"
        :class="[
          { 'flex h-dvh min-h-0 flex-col overflow-hidden': isWorkspacePage },
          { 'lg:flex lg:h-dvh lg:min-h-0 lg:flex-col lg:overflow-hidden': isContainedManagementPage },
        ]"
        :aria-hidden="isMobileSidebarActive ? 'true' : undefined"
        :inert="isMobileSidebarActive"
      >
        <header v-if="!isImmersivePage" class="shell-header" :class="{ 'shrink-0': usesViewportLayout }">
          <div class="cpa-navbar-left">
            <Button
              ref="sidebarToggleRef" size="sm" variant="outline" icon-only root-class="cpa-sidebar-toggle"
              :aria-label="isMobileViewport ? '打开导航' : isSidebarCollapsed ? '展开侧边栏' : '收起侧边栏'"
              :aria-expanded="isMobileViewport ? isSidebarOpen : !isSidebarCollapsed"
              aria-controls="app-sidebar-navigation"
              @click="isMobileViewport ? openSidebar() : (isSidebarCollapsed = !isSidebarCollapsed)"
            >
              <Icon :icon="isMobileViewport ? 'lucide:menu' : isSidebarCollapsed ? 'lucide:panel-left-open' : 'lucide:panel-left-close'" aria-hidden="true" />
            </Button>
            <nav aria-label="当前位置" class="cpa-breadcrumb"><h2>{{ currentPageTitle }}</h2></nav>
          </div>
          <div class="cpa-navbar-actions">
            <Tooltip text="刷新当前页面" placement="bottom">
              <Button size="sm" variant="outline" icon-only aria-label="刷新当前页面" @click="refreshPage"><Icon icon="lucide:refresh-cw" aria-hidden="true" /></Button>
            </Tooltip>
            <Tooltip v-if="canvasHref" text="打开无限画布" placement="bottom">
              <Button size="sm" variant="outline" icon-only root-class="hidden lg:inline-flex" aria-label="打开无限画布" @click="openInfiniteCanvas"><Icon icon="lucide:panels-top-left" aria-hidden="true" /></Button>
            </Tooltip>
            <Tooltip :text="themeButtonTitle" placement="bottom">
              <Button size="sm" variant="outline" icon-only :aria-label="themeButtonTitle" @click="cycleThemeMode"><Icon :icon="themeMode === 'dark' ? 'lucide:moon' : themeMode === 'light' ? 'lucide:sun' : 'lucide:monitor'" aria-hidden="true" /></Button>
            </Tooltip>
            <Tooltip v-if="authStore.isAdmin" text="查看接口信息" placement="bottom">
              <Button size="sm" variant="outline" icon-only root-class="hidden lg:inline-flex" aria-label="查看接口信息" @click="openApiInfo"><Icon icon="lucide:plug" aria-hidden="true" /></Button>
            </Tooltip>
            <Tooltip text="交流与服务" placement="bottom">
              <Button size="sm" variant="outline" icon-only root-class="hidden lg:inline-flex" aria-label="交流与服务" @click="isServiceDialogOpen = true"><Icon icon="lucide:messages-square" aria-hidden="true" /></Button>
            </Tooltip>
            <Tooltip v-if="authStore.isAdmin" :text="`查看版本更新，当前 ${currentVersionLabel || '版本未知'}`" placement="bottom">
              <Button size="sm" variant="outline" icon-only root-class="cpa-update-button hidden lg:inline-flex" :class="{ 'cpa-update-available': hasNewVersion }" aria-label="查看版本更新" @click="openUpdateDialog"><Icon icon="lucide:download" aria-hidden="true" /><span v-if="hasNewVersion" class="cpa-update-dot" aria-hidden="true"></span></Button>
            </Tooltip>
            <span v-if="mobileHeaderMenuItems.length" class="inline-flex lg:hidden">
              <ActionMenu label="更多" :items="mobileHeaderMenuItems" size="sm" placement="bottom" align="right" trigger-class="shell-header-more" content-class="min-w-36" @select="handleHeaderMenuSelect" />
            </span>
            <Tooltip text="退出登录" placement="bottom">
              <Button size="sm" variant="outline" icon-only aria-label="退出登录" @click="handleLogout"><Icon icon="lucide:log-out" aria-hidden="true" /></Button>
            </Tooltip>
          </div>
        </header>

        <div
          class="cpa-content relative min-w-0"
          :class="[
            isWorkspacePage ? 'flex min-h-0 flex-1 flex-col overflow-hidden' : '',
            isContainedManagementPage ? 'lg:flex lg:min-h-0 lg:flex-1 lg:flex-col lg:overflow-hidden' : '',
            isImmersivePage ? 'p-0' : 'cpa-content--padded',
          ]"
        >
          <h1 class="sr-only">{{ currentPageTitle }}</h1>
          <RouterView v-slot="{ Component, route: currentRoute }">
            <Suspense :timeout="120">
              <template #default>
                <div
                  class="route-view-content"
                  :class="[
                    isWorkspacePage ? 'flex min-h-0 flex-1 flex-col' : '',
                    isContainedManagementPage ? 'lg:flex lg:min-h-0 lg:flex-1 lg:flex-col' : '',
                    { 'h-full': isImmersivePage },
                  ]"
                >
                  <Transition name="route-view">
                    <KeepAlive :include="cachedRouteNames" :max="cachedRouteMax">
                      <component
                        :is="Component"
                        :key="String(currentRoute.name || currentRoute.path)"
                      />
                    </KeepAlive>
                  </Transition>
                </div>
              </template>
              <template #fallback>
                <PageLoadingState
                  :title="routePendingText"
                  description="正在准备页面内容..."
                  compact
                />
              </template>
            </Suspense>
          </RouterView>
        </div>
      </main>
    </div>
    <ConfirmDialog
      :open="confirmDialog.open.value"
      :title="confirmDialog.title.value"
      :message="confirmDialog.message.value"
      :confirm-text="confirmDialog.confirmText.value"
      :cancel-text="confirmDialog.cancelText.value"
      @confirm="confirmDialog.confirm"
      @cancel="confirmDialog.cancel"
    />
    <ModalShell placement="center"
      :open="isServiceDialogOpen"
      max-width="min(22rem, calc(100vw - 24px))"
      :z-index="100"
      panel-class="w-full p-5"
      close-on-backdrop
      aria-label="交流与服务"
      @close="isServiceDialogOpen = false"
    >
      <ModalHeader
        title="交流与服务"
        title-class="ui-subsection-title"
        :bordered="false"
        flush
        @close="isServiceDialogOpen = false"
      />

      <div class="mt-4 grid gap-2">
        <a
          v-for="item in headerServiceItems"
          :key="item.key"
          :href="item.href"
          target="_blank"
          rel="noopener noreferrer"
          class="shell-service-link group flex min-w-0 items-center gap-3 rounded-lg border border-border px-3 py-2.5 text-left transition-colors hover:border-[hsl(var(--foreground)_/_0.24)] hover:bg-muted/50 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          @click="isServiceDialogOpen = false"
        >
          <span class="inline-flex h-8 w-8 shrink-0 items-center justify-center rounded-full bg-muted text-muted-foreground transition-colors group-hover:text-foreground">
            <Icon :icon="item.icon" class="h-4 w-4" />
          </span>
          <span class="min-w-0 flex-1">
            <span class="block text-sm font-medium text-foreground">{{ item.label }}</span>
            <span v-if="item.detail" class="mt-0.5 block text-xs leading-5 text-muted-foreground">{{ item.detail }}</span>
          </span>
          <Icon icon="lucide:external-link" class="h-3.5 w-3.5 shrink-0 text-muted-foreground transition-colors group-hover:text-foreground" />
        </a>
      </div>
    </ModalShell>
    <ModalShell placement="center"
      :open="isApiInfoOpen"
      :z-index="100"
      panel-class="p-6"
      close-on-backdrop
      @close="isApiInfoOpen = false"
    >
          <ModalHeader
            title="API 接口"
            subtitle="根据客户端选择对应接口"
            title-class="ui-subsection-title"
            :bordered="false"
            flush
            @close="isApiInfoOpen = false"
          />

          <div class="mt-4 space-y-3 text-sm">
            <div>
              <p class="text-xs text-muted-foreground">基础端点</p>
              <div class="mt-1 flex items-start gap-2">
                <ValueSurface
                  tag="p"
                  mono
                  break-mode="all"
                  root-class="min-w-0 flex-1"
                >
                  {{ apiBaseUrl }}
                </ValueSurface>
                <Button
                  size="sm"
                  variant="outline"
                  root-class="shrink-0 text-[11px] text-muted-foreground"
                  @click="copyText(apiBaseUrl)"
                >
                  复制
                </Button>
              </div>
            </div>
            <div>
              <p class="text-xs text-muted-foreground">SDK 接口</p>
              <div class="mt-1 flex items-start gap-2">
                <ValueSurface
                  tag="p"
                  mono
                  break-mode="all"
                  root-class="min-w-0 flex-1"
                >
                  {{ apiSdkUrl }}
                </ValueSurface>
                <Button
                  size="sm"
                  variant="outline"
                  root-class="shrink-0 text-[11px] text-muted-foreground"
                  @click="copyText(apiSdkUrl)"
                >
                  复制
                </Button>
              </div>
            </div>
            <div>
              <p class="text-xs text-muted-foreground">支持模型</p>
              <div class="mt-1 space-y-3 rounded-2xl border border-border bg-background px-3 py-2 text-xs text-muted-foreground">
                <div>
                  <p class="mb-1 text-[11px] text-muted-foreground">聊天模型</p>
                  <div class="flex flex-wrap gap-2 text-foreground">
                    <MetaChip
                      v-for="model in supportedChatModels"
                      :key="`chat-${model}`"
                      size="xs"
                    >
                      {{ model }}
                    </MetaChip>
                  </div>
                </div>
                <div>
                  <p class="mb-1 text-[11px] text-muted-foreground">图片模型</p>
                <div class="flex flex-wrap gap-2 text-foreground">
                  <MetaChip
                    v-for="model in supportedImageModels"
                    :key="`image-${model}`"
                    size="xs"
                  >
                    {{ model }}
                  </MetaChip>
                </div>
                </div>
              </div>
            </div>
            <div>
              <p class="text-xs text-muted-foreground">当前调用密钥</p>
              <div class="mt-1 flex items-start gap-2">
                <ValueSurface
                  tag="p"
                  mono
                  break-mode="all"
                  root-class="min-w-0 flex-1"
                >
                  {{ apiKeyDisplay }}
                </ValueSurface>
                <Button
                  size="sm"
                  variant="outline"
                  root-class="shrink-0 text-[11px] text-muted-foreground"
                  :disabled="!currentAuthToken"
                  @click="copyText(apiKeyDisplay)"
                >
                  复制
                </Button>
              </div>
              <p class="mt-1 text-[11px] text-muted-foreground">
                请求头使用 Authorization: Bearer &lt;当前调用密钥&gt;。
              </p>
            </div>
          </div>

          <ModalFooter class="mt-6" :bordered="false" flush>
            <Button
              size="xs"
              variant="primary"
              root-class="min-w-14 justify-center"
              @click="isApiInfoOpen = false"
            >
              知道了
            </Button>
          </ModalFooter>
    </ModalShell>
    <ModalShell placement="center"
      :open="isUpdateDialogOpen"
      :z-index="100"
      panel-class="flex min-h-0 max-h-[80dvh] flex-col overflow-hidden p-6"
      close-on-backdrop
      @close="closeUpdateDialog"
    >
      <ModalHeader
        title="版本更新"
        subtitle="查看当前版本和更新日志"
        title-class="ui-subsection-title"
        :bordered="false"
        flush
        @close="closeUpdateDialog"
      />

      <div class="mt-4 grid gap-3 sm:grid-cols-2">
        <div class="rounded-2xl border border-border bg-background px-4 py-3">
          <p class="text-xs text-muted-foreground">当前版本</p>
          <p class="mt-1 text-base font-semibold text-foreground">{{ currentVersionLabel }}</p>
        </div>
        <div class="rounded-2xl border border-border bg-background px-4 py-3">
          <div class="flex items-center justify-between gap-3">
            <p class="text-xs text-muted-foreground">最新版本</p>
            <button
              type="button"
              class="text-xs text-muted-foreground underline-offset-2 hover:text-foreground hover:underline disabled:cursor-not-allowed disabled:opacity-60"
              :disabled="isCheckingUpdate"
              @click="checkForUpdates(true)"
            >
              {{ isCheckingUpdate ? '检查中...' : '检查更新' }}
            </button>
          </div>
          <p
            class="mt-1 text-base font-semibold"
            :class="hasNewVersion ? 'text-amber-800 dark:text-amber-300' : 'text-foreground'"
          >
            {{ latestVersionLabel }}
          </p>
        </div>
      </div>

      <div
        v-if="updateCheckMessage"
        class="mt-3 flex flex-wrap items-center justify-between gap-2 rounded-2xl border px-4 py-3 text-sm"
        :class="updateCheckMessageClass"
      >
        <span class="min-w-0 flex-1 font-medium">{{ updateCheckMessage }}</span>
        <MetaChip
          size="xs"
          :tone="updateCheckBadgeTone"
          strong
          chip-class="shrink-0"
        >
          {{ updateCheckBadgeText }}
        </MetaChip>
      </div>

      <div class="scrollbar-slim mt-5 min-h-0 flex-1 space-y-5 overflow-y-auto pr-2">
        <div
          v-for="release in releaseEntries"
          :key="`${release.version}-${release.date}`"
          class="border-l border-border pl-4"
        >
          <div class="flex flex-wrap items-center gap-2">
            <span class="text-sm font-semibold text-foreground">
              {{ release.version === 'Unreleased' ? '未发布' : release.version }}
            </span>
            <span v-if="release.date" class="text-xs text-muted-foreground">{{ release.date }}</span>
            <MetaChip
              v-if="normalizeVersionTag(release.version) === latestVersionLabel"
              size="xs"
              tone="success"
              strong
            >
              最新
            </MetaChip>
            <MetaChip
              v-if="normalizeVersionTag(release.version) === currentVersionLabel"
              size="xs"
              tone="muted"
            >
              当前
            </MetaChip>
          </div>
          <div class="mt-2 space-y-1.5">
            <div
              v-for="(item, index) in release.items"
              :key="`${release.version}-${index}`"
              class="flex items-start gap-2 text-sm leading-6 text-muted-foreground"
            >
              <MetaChip
                size="xs"
                :tone="releaseItemTone(item.type)"
                strong
                chip-class="mt-0.5 shrink-0"
              >
                {{ item.type }}
              </MetaChip>
              <span class="min-w-0 flex-1 text-foreground/85">
                <template
                  v-for="(segment, segmentIndex) in splitReleaseInlineCode(item.content)"
                  :key="`${release.version}-${index}-${segmentIndex}`"
                >
                  <code
                    v-if="segment.kind === 'code'"
                    class="rounded bg-muted px-1 py-0.5 font-mono text-[0.9em] text-foreground"
                  >{{ segment.content }}</code>
                  <span v-else>{{ segment.content }}</span>
                </template>
              </span>
            </div>
          </div>
        </div>
        <div v-if="!releaseEntries.length" class="rounded-2xl border border-dashed border-border bg-muted/30 px-4 py-6 text-center text-sm text-muted-foreground">
          暂无更新日志。
        </div>
      </div>

      <ModalFooter class="mt-6" :bordered="false" flush>
        <Button
          size="xs"
          variant="outline"
          @click="openReleasePage"
        >
          打开发布页
        </Button>
        <Button
          size="xs"
          variant="primary"
          root-class="min-w-14 justify-center"
          @click="closeUpdateDialog"
        >
          知道了
        </Button>
        <Button
          v-if="canStartUpdate"
          size="xs"
          variant="primary"
          :disabled="updateProgressState.busy || isUpdateConfirming"
          @click="startUpdate"
        >
          {{ isUpdateConfirming ? '等待确认' : '立即更新' }}
        </Button>
      </ModalFooter>
    </ModalShell>

    <OperationProgressDrawer
      v-if="updateProgressState.open"
      :open="updateProgressState.open"
      :title="updateProgressState.title"
      :subtitle="updateProgressState.subtitle"
      :total="updateProgressState.total"
      :current="updateProgressState.current"
      :status-label="updateProgressState.statusLabel"
      :message="updateProgressState.message"
      :error="updateProgressState.error"
      :busy="updateProgressState.busy"
      :close-disabled="updateProgressState.busy"
      :tone="updateProgressState.tone"
      :events="updateProgressState.events"
      :summary-items="updateProgressSummary"
      @close="closeUpdateProgress"
    />
  </div>
</template>

<script setup lang="ts">
import { Icon } from '@iconify/vue'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch, type ComponentPublicInstance } from 'vue'
import { RouterLink, RouterView, useRoute, useRouter } from 'vue-router'
import {
  PUBLIC_SETTINGS_CHANGED_EVENT,
} from '@/api/settings'
import { versionApi } from '@/api/version'
import { getAuthToken } from '@/api/client'
import type { AuthCapability } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import { useModelCatalog } from '@/composables/useModelCatalog'
import { usePublicRuntimeConfig } from '@/composables/usePublicRuntimeConfig'
import { useListLayoutPreference } from '@/composables/useListLayoutPreference'
import { useOperationProgressRuntime } from '@/composables/useOperationProgressRuntime'
import { ActionMenu, Button, Tooltip, ValueSurface, type ActionMenuItem } from 'nanocat-ui'
import ConfirmDialog from '@/components/ui/AppConfirmDialog.vue'
import MetaChip from '@/components/ai/MetaChip.vue'
import ModalFooter from '~/components/ui/ModalFooter.vue'
import ModalHeader from '~/components/ui/ModalHeader.vue'
import ModalShell from '@/components/ui/ModalShell.vue'
import OperationProgressDrawer from '@/components/ai/OperationProgressDrawer.vue'
import PageLoadingState from '@/components/ai/PageLoadingState.vue'
import { useConfirmDialog } from '@/composables/useConfirmDialog'
import { useToast } from '@/composables/useToast'
import {
  getBooleanPreference,
  getStringPreference,
  preferenceKeys,
  removePreference,
  setBooleanPreference,
  setStringPreference,
} from '@/lib/preferences'
import { writeClipboardText } from '@/lib/clipboard'
import { focusFirstWithin, focusRefTarget, trapFocusWithin } from '@/lib/focusLoop'
import { applyThemeMode, getStoredThemeMode, setStoredThemeMode, type ThemeMode } from '@/lib/theme'
import {
  normalizeVersionTag,
  parseChangelog,
  splitReleaseInlineCode,
  type ReleaseInfo,
} from '@/lib/release'
import type { UpdateTaskResponse, VersionCheckResponse } from '@/types/api'
import { version as localVersion } from '../../package.json'

const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const toast = useToast()
const isSidebarOpen = ref(false)
const isSidebarCollapsed = ref(false)
const isMobileViewport = ref(typeof window !== 'undefined'
  ? !window.matchMedia('(min-width: 1024px)').matches
  : false)
const sidebarToggleRef = ref<ComponentPublicInstance | null>(null)
const sidebarRef = ref<HTMLElement | null>(null)
const confirmDialog = useConfirmDialog()
const isApiInfoOpen = ref(false)
const isServiceDialogOpen = ref(false)
const isUpdateDialogOpen = ref(false)
const isCheckingUpdate = ref(false)
const isUpdateConfirming = ref(false)
const currentVersionTag = ref(normalizeVersionTag(localVersion))
const updateStatus = ref<VersionCheckResponse | null>(null)
const updateRequestError = ref('')
const updateTargetTag = ref('')
const updateProgressRuntime = useOperationProgressRuntime()
const updateProgressState = updateProgressRuntime.state
const releaseEntries = ref<ReleaseInfo[]>([])
const currentAuthToken = ref('')
const themeMode = ref<ThemeMode>(getStoredThemeMode())
type RouteProgressPhase = 'idle' | 'running' | 'finishing'
const pendingNavigationPath = ref('')
const routeProgressPhase = ref<RouteProgressPhase>('idle')
const cachedRouteNames = ['Dashboard', 'Studio', 'Accounts', 'Logs', 'Monitor', 'Gallery', 'Proxy', 'UserKeys', 'Settings']
const cachedRouteMax = cachedRouteNames.length
const themeOptions: { label: string; value: ThemeMode }[] = [
  { label: '浅色', value: 'light' },
  { label: '深色', value: 'dark' },
  { label: '系统', value: 'system' },
]
const {
  chatModels: supportedChatModels,
  imageModels: supportedImageModels,
  loadModelCatalog,
} = useModelCatalog()
const {
  apiBaseUrl,
  thirdPartyApps,
  loadPublicRuntimeConfig,
} = usePublicRuntimeConfig()

type NavigationItem = {
  path: string
  label: string
  icon: string
  capability: AuthCapability
}

const menuItems: NavigationItem[] = [
  {
    path: '/',
    label: '概览中心',
    icon: 'lucide:layout-dashboard',
    capability: 'admin_console',
  },
  {
    path: '/monitor',
    label: '实时监控',
    icon: 'lucide:activity',
    capability: 'admin_console',
  },
  {
    path: '/studio',
    label: '对话画图',
    icon: 'lucide:messages-square',
    capability: 'studio',
  },
  {
    path: '/accounts',
    label: '账号管理',
    icon: 'lucide:users',
    capability: 'admin_console',
  },
  {
    path: '/logs',
    label: '日志管理',
    icon: 'lucide:scroll-text',
    capability: 'admin_console',
  },
  {
    path: '/gallery',
    label: '图片管理',
    icon: 'lucide:images',
    capability: 'admin_console',
  },
  {
    path: '/proxy',
    label: '代理管理',
    icon: 'lucide:network',
    capability: 'admin_console',
  },
  {
    path: '/user-keys',
    label: '用户密钥',
    icon: 'lucide:key-round',
    capability: 'admin_console',
  },
  {
    path: '/settings',
    label: '系统设置',
    icon: 'lucide:settings-2',
    capability: 'admin_console',
  },
]


const routeTitleMap: Record<string, string> = {
  dashboard: '概览中心',
  accounts: '账号管理',
  logs: '日志管理',
  gallery: '图片管理',
  proxy: '代理管理',
  'user-keys': '用户密钥',
  settings: '系统设置',
  monitor: '实时监控',
  studio: '对话画图',
}

const visibleMenuItems = computed(() => {
  return menuItems.filter(item => authStore.hasCapability(item.capability))
})

const visibleMenuGroups = computed(() => {
  const groups = [['/', '/monitor'], ['/studio', '/gallery'], ['/accounts', '/proxy', '/user-keys'], ['/logs', '/settings']]
  return groups.map(paths => visibleMenuItems.value.filter(item => paths.includes(item.path))).filter(group => group.length > 0)
})

const currentPageTitle = computed(() => {
  const routeName = String(route.name || '')
  const item = visibleMenuItems.value.find(item => isNavActive(item.path))
  return item?.label || routeTitleMap[routeName] || '概览中心'
})

const isImmersivePage = computed(() => Boolean(route.meta.immersive))
const isWorkspacePage = computed(() => Boolean(route.meta.workspace))
const isManagementPage = computed(() => Boolean(route.meta.management))
const { isWorkspaceLayout } = useListLayoutPreference()
const isContainedManagementPage = computed(() => isManagementPage.value && isWorkspaceLayout.value)
const usesViewportLayout = computed(() => isWorkspacePage.value || isContainedManagementPage.value)
const isMobileSidebarActive = computed(() => isMobileViewport.value && isSidebarOpen.value)
const isSidebarRail = computed(() => (
  !isMobileViewport.value && (isSidebarCollapsed.value || isImmersivePage.value)
))
const sidebarStyle = computed(() => ({
  '--sidebar-width': isSidebarRail.value ? 'var(--console-sidebar-rail-width)' : 'var(--console-sidebar-width)',
}))

const activeNavPathSet = computed(() => {
  const name = String(route.name || '')
  const currentPath = route.path
  return new Set(
    visibleMenuItems.value
      .filter((item) => isRoutePathActive(item.path, name, currentPath))
      .map((item) => item.path),
  )
})

function isRoutePathActive(path: string, name: string, currentPath: string) {
  const normalized = path.replace(/^\/+/, '')
  if (!normalized) return name === 'dashboard' || currentPath === '/'
  return currentPath === path || name === normalized
}

const isNavActive = (path: string) => {
  return activeNavPathSet.value.has(path)
}

function isNavPending(path: string) {
  return Boolean(pendingNavigationPath.value)
    && normalizedRoutePath(path) === pendingNavigationPath.value
}

function isNavVisuallyActive(path: string) {
  if (pendingNavigationPath.value) return isNavPending(path)
  return isNavActive(path)
}

const apiSdkUrl = computed(() => `${apiBaseUrl.value}/v1`)
const apiKeyDisplay = computed(() => currentAuthToken.value || '未登录')
const currentVersionLabel = computed(() => normalizeVersionTag(
  updateStatus.value?.current_tag || currentVersionTag.value || '',
))
const latestVersionLabel = computed(() => normalizeVersionTag(
  updateStatus.value?.latest_tag || currentVersionTag.value || '',
))
const hasNewVersion = computed(() => updateStatus.value?.update_available === true)
const canStartUpdate = computed(() => updateStatus.value?.can_update === true && !updateProgressState.busy)
const updateProgressSummary = computed(() => [
  {
    key: 'current-version',
    label: '当前版本',
    value: currentVersionLabel.value || '未知',
  },
  {
    key: 'target-version',
    label: '目标版本',
    value: updateTargetTag.value || latestVersionLabel.value || '未知',
  },
])
const updateCheckMessage = computed(() => {
  if (isCheckingUpdate.value) return updateCheckingMessage
  return updateRequestError.value || updateStatus.value?.status_message || ''
})
const updateCheckMessageClass = computed(() => {
  if (isCheckingUpdate.value) return 'border-cyan-500/35 bg-cyan-500/10 text-cyan-700'
  if (updateRequestError.value || updateStatus.value?.tone === 'warning') return 'border-amber-500/40 bg-amber-500/10 text-amber-700'
  if (updateStatus.value?.tone === 'success') return 'border-emerald-500/40 bg-emerald-500/10 text-emerald-700'
  return 'border-border bg-muted/40 text-muted-foreground'
})
const updateCheckBadgeText = computed(() => {
  if (isCheckingUpdate.value) return '检查中'
  if (updateRequestError.value) return '请求失败'
  return updateStatus.value?.status_label || '未检查'
})
const updateCheckBadgeTone = computed(() => {
  if (isCheckingUpdate.value) return 'info'
  if (updateRequestError.value || updateStatus.value?.tone === 'warning') return 'warning'
  if (updateStatus.value?.tone === 'success') return 'success'
  return 'muted'
})
function releaseItemTone(type: string): 'default' | 'muted' | 'success' | 'warning' | 'danger' | 'info' {
  const value = String(type || '').trim()
  if (['新增', '添加', 'Added'].includes(value)) return 'success'
  if (['优化', '改进', 'Changed', 'Improved'].includes(value)) return 'info'
  if (['修复', '修正', 'Fixed'].includes(value)) return 'warning'
  if (['移除', '删除', '废弃', 'Removed', 'Deprecated'].includes(value)) return 'danger'
  return 'muted'
}
const canvasHref = computed(() => {
  const canvas = thirdPartyApps.value?.infinite_canvas
  const token = getAuthToken()
  if (!canvas?.enabled || !canvas.url.trim() || !token) return ''
  return buildThirdPartyHref(canvas.url, apiBaseUrl.value, token)
})
const themeButtonText = computed(() => themeOptions.find(option => option.value === themeMode.value)?.label || '系统')
const themeButtonTitle = computed(() => `当前主题：${themeButtonText.value}，点击切换`)
type HeaderServiceItem = {
  key: string
  label: string
  detail?: string
  href: string
  icon: string
}

const headerServiceItems: HeaderServiceItem[] = [
  {
    key: 'service-qq',
    label: 'QQ 交流群：1057583658',
    href: 'https://qm.qq.com/cgi-bin/qm/qr?group_code=1057583658',
    icon: 'lucide:messages-square',
  },
  {
    key: 'service-api',
    label: '生图 API',
    detail: 'jisuai.net',
    href: 'https://jisuai.net',
    icon: 'lucide:badge-dollar-sign',
  },
]

const mobileHeaderMenuItems = computed<ActionMenuItem[]>(() => {
  const items: ActionMenuItem[] = []
  if (canvasHref.value) items.push({ key: 'canvas', label: '无限画布' })
  if (authStore.isAdmin) {
    items.push(
      { key: 'api-info', label: '接口信息' },
      { key: 'updates', label: '版本更新' },
    )
  }
  items.push({ key: 'services', label: '交流与服务', dividerBefore: items.length > 0 })
  return items
})
const routePendingText = computed(() => `正在加载${currentPageTitle.value}`)
let systemThemeMedia: MediaQueryList | null = null
let viewportMedia: MediaQueryList | null = null
const prefetchedRoutePaths = new Set<string>()
const routeProgressDelayMs = 140
const routeProgressFinishMs = 140
let routeProgressDelayTimer: number | null = null
let routeProgressFinishTimer: number | null = null
const defaultReleasePageUrl = 'https://github.com/Drunkard-baifeng/go-chatgpt2api/releases'
const releasePageUrl = computed(() => updateStatus.value?.release_url || defaultReleasePageUrl)
const updateCheckingMessage = '正在检查云端版本...'
const updateTaskPollIntervalMs = 1000
let updateTaskPollTimer: number | null = null
let updateReloadScheduled = false
let updateReloadTimer: number | null = null
let isUpdateTaskPolling = false
let isUpdateSubmitting = false
let unconfirmedUpdatePreviousId: string | null = null
let updateDisposed = false
let updatePollRevision = 0
const routeViewLoaders: Record<string, () => Promise<unknown>> = {
  '/': () => import('~/views/Dashboard.vue'),
  '/accounts': () => import('~/views/Accounts.vue'),
  '/logs': () => import('~/views/Logs.vue'),
  '/gallery': () => import('~/views/Gallery.vue'),
  '/monitor': () => import('~/views/Monitor.vue'),
  '/proxy': () => import('~/views/Proxy.vue'),
  '/user-keys': () => import('~/views/UserKeys.vue'),
  '/settings': () => import('~/views/Settings.vue'),
  '/studio': () => import('~/views/Studio.vue'),
}

function setMobileSidebarScrollLock(locked: boolean) {
  if (typeof document === 'undefined') return
  document.documentElement.classList.toggle('app-mobile-sidebar-open', locked)
  document.body.classList.toggle('app-mobile-sidebar-open', locked)
}

function focusSidebarToggle() {
  void nextTick(() => {
    if (!isMobileViewport.value) return
    focusRefTarget(sidebarToggleRef.value)
  })
}

function focusSidebarNavigation() {
  void nextTick(() => {
    if (!isMobileViewport.value || !isSidebarOpen.value) return
    focusFirstWithin(sidebarRef.value, '#app-sidebar-navigation a[href]')
  })
}

function handleSidebarKeydown(event: KeyboardEvent) {
  if (!isMobileSidebarActive.value) return
  trapFocusWithin(sidebarRef.value, event)
}

function openSidebar() {
  if (!isMobileViewport.value) return
  isSidebarOpen.value = true
  focusSidebarNavigation()
}

function closeSidebar(options: { restoreFocus?: boolean } = {}) {
  const wasOpen = isSidebarOpen.value
  isSidebarOpen.value = false
  if (wasOpen && options.restoreFocus !== false) focusSidebarToggle()
}

function handleViewportChange(event: MediaQueryListEvent) {
  isMobileViewport.value = !event.matches
  if (event.matches) closeSidebar({ restoreFocus: false })
}

function setupViewportListener() {
  if (typeof window === 'undefined') return
  viewportMedia = window.matchMedia('(min-width: 1024px)')
  isMobileViewport.value = !viewportMedia.matches
  viewportMedia.addEventListener('change', handleViewportChange)
}

function teardownViewportListener() {
  viewportMedia?.removeEventListener('change', handleViewportChange)
  viewportMedia = null
  setMobileSidebarScrollLock(false)
}

function handleWindowKeydown(event: KeyboardEvent) {
  if (event.key !== 'Escape' || !isMobileViewport.value || !isSidebarOpen.value) return
  event.preventDefault()
  closeSidebar()
}

watch([isMobileViewport, isSidebarOpen], ([isMobile, isOpen]) => {
  setMobileSidebarScrollLock(isMobile && isOpen)
})

watch(
  () => route.path,
  () => {
    closeSidebar()
    void loadPublicRuntimeConfig()
  }
)

isSidebarCollapsed.value = getBooleanPreference(preferenceKeys.sidebarCollapsed, false)

watch(isSidebarCollapsed, (value) => {
  setBooleanPreference(preferenceKeys.sidebarCollapsed, value)
})

function buildThirdPartyHref(appUrl: string, baseUrl: string, apiKey: string) {
  const url = appUrl.trim()
  try {
    const target = new URL(url)
    target.searchParams.set('apiKey', apiKey)
    target.searchParams.set('baseUrl', baseUrl)
    return target.toString()
  } catch {
    return `${url}${url.includes('?') ? '&' : '?'}apiKey=${encodeURIComponent(apiKey)}&baseUrl=${encodeURIComponent(baseUrl)}`
  }
}

function refreshPage() {
  window.location.reload()
}

async function handleLogout() {
  closeSidebar()
  await authStore.logout()
  await router.replace({ name: 'login' })
}

async function openApiInfo() {
  currentAuthToken.value = getAuthToken()
  isApiInfoOpen.value = true
  await Promise.all([loadModelCatalog(), loadPublicRuntimeConfig()])
}

async function copyText(value: string) {
  const text = String(value || '').trim()
  if (!text) return
  try {
    await writeClipboardText(text)
    toast.success('已复制')
  } catch (error) {
    console.error('Copy failed', error)
    toast.error('复制失败，请手动复制')
  }
}

async function openInfiniteCanvas() {
  if (!canvasHref.value) return
  const ok = await confirmDialog.ask({
    title: '打开无限画布',
    message: '即将打开外部画布，并附带当前接口地址和当前调用密钥。是否继续？',
    confirmText: '打开',
    cancelText: '取消',
  })
  if (ok) {
    window.open(canvasHref.value, '_blank', 'noopener,noreferrer')
  }
}

function handleHeaderMenuSelect(key: string) {
  if (key === 'services') {
    isServiceDialogOpen.value = true
    return
  }
  if (key === 'canvas') {
    void openInfiniteCanvas()
    return
  }
  if (key === 'updates') {
    openUpdateDialog()
    return
  }
  if (key === 'api-info') {
    void openApiInfo()
  }
}

function setThemeMode(mode: ThemeMode) {
  themeMode.value = mode
  setStoredThemeMode(mode)
}

function cycleThemeMode() {
  const index = themeOptions.findIndex(option => option.value === themeMode.value)
  const next = themeOptions[(index + 1) % themeOptions.length]
  setThemeMode(next.value)
}

function openUpdateDialog() {
  isUpdateDialogOpen.value = true
}

function openReleasePage() {
  window.open(releasePageUrl.value, '_blank', 'noopener,noreferrer')
  closeUpdateDialog()
}

function closeUpdateDialog() {
  const latestTag = updateStatus.value?.latest_tag || ''
  if (updateStatus.value?.update_available && latestTag) {
    setStringPreference(preferenceKeys.updateDismissedTag, latestTag)
  }
  isUpdateDialogOpen.value = false
}

function clearUpdateTaskPollTimer() {
  if (updateTaskPollTimer === null) return
  window.clearTimeout(updateTaskPollTimer)
  updateTaskPollTimer = null
}

function applyUpdateTask(task: UpdateTaskResponse, open: boolean) {
  if (task.state === 'idle') return
  updateTargetTag.value = normalizeVersionTag(task.latest_tag || updateTargetTag.value)
  currentVersionTag.value = normalizeVersionTag(task.current_tag || currentVersionTag.value)
  updateProgressState.open = open
  updateProgressState.title = task.busy ? '正在更新 ChatGPT2API' : 'ChatGPT2API 更新'
  updateProgressState.subtitle = task.latest_tag ? `目标版本 ${normalizeVersionTag(task.latest_tag)}` : ''
  updateProgressState.total = task.total
  updateProgressState.current = task.current
  updateProgressState.statusLabel = task.status_label
  updateProgressState.message = task.message
  updateProgressState.error = task.error
  updateProgressState.busy = task.busy
  updateProgressState.tone = task.tone
  updateProgressState.events = task.events.map(event => ({
    key: event.id,
    timestamp: event.timestamp,
    label: event.label,
    message: event.message,
    tone: event.tone,
  }))
}

function scheduleUpdateTaskPoll() {
  clearUpdateTaskPollTimer()
  if (updateDisposed || document.hidden || !authStore.isAdmin) return
  updateTaskPollTimer = window.setTimeout(() => {
    void pollUpdateTask()
  }, updateTaskPollIntervalMs)
}

async function startUpdate() {
  if (isUpdateSubmitting || isUpdateConfirming.value || !canStartUpdate.value) return
  const targetTag = normalizeVersionTag(updateStatus.value?.latest_tag || '')
  if (!targetTag) return

  isUpdateConfirming.value = true
  let confirmed = false
  try {
    confirmed = await confirmDialog.ask({
      title: '确认更新',
      message: `检测到新版本 ${targetTag}，当前版本为 ${currentVersionLabel.value || '版本未知'}。确认后将下载并校验更新，安装时服务会自动重启，页面会重新连接。配置和数据将保留；新版本启动失败时会尝试恢复旧程序。`,
      confirmText: '立即更新',
      cancelText: '取消',
    })
  } finally {
    isUpdateConfirming.value = false
  }
  if (!confirmed) return

  clearUpdateTaskPollTimer()
  updatePollRevision += 1
  updateTargetTag.value = targetTag
  isUpdateDialogOpen.value = false

  isUpdateSubmitting = true
  await updateProgressRuntime.start({ title: '正在提交更新', subtitle: `目标版本 ${targetTag}`, total: 6, message: '正在向服务提交更新请求，请稍候。' })
  let submitted = false
  try {
    const previous = await versionApi.updateTask()
    if (previous.busy) {
      setStringPreference(preferenceKeys.updateActiveTaskId, previous.task_id)
      applyUpdateTask(previous, true)
      scheduleUpdateTaskPoll()
      return
    }
    unconfirmedUpdatePreviousId = previous.task_id
    submitted = true
    const task = await versionApi.startUpdate()
    unconfirmedUpdatePreviousId = null
    setStringPreference(preferenceKeys.updateActiveTaskId, task.task_id)
    applyUpdateTask(task, true)
    if (task.busy) scheduleUpdateTaskPoll()
    else await checkForUpdates(false)
  } catch (error) {
    const message = error instanceof Error ? error.message : '在线更新失败，请稍后重试。'
    if (submitted) {
      updateProgressState.message = `尚未确认请求是否受理：${message}。正在查询服务中的实际更新任务，请勿重复提交。`
      scheduleUpdateTaskPoll()
    } else {
      unconfirmedUpdatePreviousId = null
      updateProgressRuntime.fail(`未提交更新：${message}。请检查网络后重试。`)
    }
  } finally {
    isUpdateSubmitting = false
  }
}

async function pollUpdateTask() {
  if (updateDisposed || document.hidden || !authStore.isAdmin) return
  if (isUpdateTaskPolling || isUpdateSubmitting) {
    if (updateProgressState.busy) scheduleUpdateTaskPoll()
    return
  }
  isUpdateTaskPolling = true
  const revision = updatePollRevision
  try {
    const task = await versionApi.updateTask()
    if (updateDisposed || revision !== updatePollRevision) return
    if (unconfirmedUpdatePreviousId !== null && !task.busy && (task.state === 'idle' || task.task_id === unconfirmedUpdatePreviousId)) {
      unconfirmedUpdatePreviousId = null
      updateProgressRuntime.fail('服务中未发现本次提交的新任务，请检查网络后重新提交。')
      clearUpdateTaskPollTimer()
      return
    }
    unconfirmedUpdatePreviousId = null
    const activeTaskId = getStringPreference(preferenceKeys.updateActiveTaskId)
    const shouldOpen = updateProgressState.open || task.busy || Boolean(task.task_id && task.task_id === activeTaskId)
    if (task.state === 'idle' && updateProgressState.busy) {
      updateProgressRuntime.fail('服务未记录到已受理的更新任务，请检查网络后重新提交。')
    }
    if (task.busy && task.task_id) setStringPreference(preferenceKeys.updateActiveTaskId, task.task_id)
    applyUpdateTask(task, shouldOpen)
    if (task.busy) {
      return
    }
    clearUpdateTaskPollTimer()
    await checkForUpdates(false)
    if (updateDisposed || revision !== updatePollRevision) return
    const targetTag = normalizeVersionTag(task.latest_tag)
    if (
      !updateReloadScheduled
      && task.state === 'succeeded'
      && targetTag
      && normalizeVersionTag(localVersion) !== targetTag
    ) {
      updateReloadScheduled = true
      updateReloadTimer = window.setTimeout(() => window.location.reload(), 400)
    }
  } catch {
    if (updateDisposed || revision !== updatePollRevision) return
    if (updateProgressState.busy) {
      updateProgressState.message = '暂时无法连接服务，正在重新连接。请勿重复提交更新；持续无法连接时请检查服务日志。'
    }
  } finally {
    isUpdateTaskPolling = false
    if (updateProgressState.busy) scheduleUpdateTaskPoll()
  }
}

function handleUpdateVisibilityChange() {
  if (document.hidden) clearUpdateTaskPollTimer()
  else if (authStore.isAdmin) void pollUpdateTask()
}

function closeUpdateProgress() {
  if (!updateProgressRuntime.close()) return
  clearUpdateTaskPollTimer()
  removePreference(preferenceKeys.updateActiveTaskId)
}

async function checkForUpdates(showMessage = true, openAvailable = false) {
  if (isCheckingUpdate.value) return
  isCheckingUpdate.value = true
  updateRequestError.value = ''
  try {
    const result = await versionApi.check(showMessage)
    updateStatus.value = result
    currentVersionTag.value = result.current_tag
    releaseEntries.value = parseChangelog(result.changelog)
    if (
      openAvailable
      && result.update_available
      && getStringPreference(preferenceKeys.updateDismissedTag) !== result.latest_tag
    ) {
      isUpdateDialogOpen.value = true
    }
    if (showMessage) {
      if (result.tone === 'warning') toast.warning(result.status_message)
      else if (result.update_available) toast.info(result.status_message)
      else toast.success(result.status_message)
    }
  } catch {
    updateRequestError.value = '无法连接后端检查更新，请稍后重试。'
    if (showMessage) toast.warning(updateRequestError.value)
  } finally {
    isCheckingUpdate.value = false
  }
}

async function loadCurrentVersion() {
  try {
    const result = await versionApi.current()
    const runtimeVersion = String(result.tag || '').trim()
    if (runtimeVersion) currentVersionTag.value = runtimeVersion
  } catch {}
}

function handleSystemThemeChange() {
  if (themeMode.value === 'system') {
    applyThemeMode(themeMode.value)
  }
}

function setupSystemThemeListener() {
  if (typeof window === 'undefined') return
  systemThemeMedia = window.matchMedia('(prefers-color-scheme: dark)')
  systemThemeMedia.addEventListener('change', handleSystemThemeChange)
}

function handlePublicSettingsChanged() {
  void loadPublicRuntimeConfig(true)
}

function normalizedRoutePath(path: string) {
  if (!path || path === '/') return '/'
  return `/${path.replace(/^\/+/, '').split(/[?#]/)[0]}`
}

function clearRouteProgressDelayTimer() {
  if (routeProgressDelayTimer === null) return
  window.clearTimeout(routeProgressDelayTimer)
  routeProgressDelayTimer = null
}

function clearRouteProgressFinishTimer() {
  if (routeProgressFinishTimer === null) return
  window.clearTimeout(routeProgressFinishTimer)
  routeProgressFinishTimer = null
}

function beginRouteNavigation(path: string) {
  const normalizedPath = normalizedRoutePath(path)
  if (normalizedPath === normalizedRoutePath(route.path)) return
  if (pendingNavigationPath.value === normalizedPath && routeProgressPhase.value !== 'finishing') return

  pendingNavigationPath.value = normalizedPath
  clearRouteProgressDelayTimer()
  clearRouteProgressFinishTimer()
  routeProgressPhase.value = 'idle'
  routeProgressDelayTimer = window.setTimeout(() => {
    routeProgressDelayTimer = null
    if (pendingNavigationPath.value !== normalizedPath) return
    routeProgressPhase.value = 'running'
  }, routeProgressDelayMs)
}

function finishRouteNavigation(path?: string) {
  const normalizedPath = path ? normalizedRoutePath(path) : ''
  if (
    normalizedPath
    && pendingNavigationPath.value
    && normalizedPath !== pendingNavigationPath.value
  ) return

  pendingNavigationPath.value = ''
  clearRouteProgressDelayTimer()
  clearRouteProgressFinishTimer()
  if (routeProgressPhase.value !== 'running') {
    routeProgressPhase.value = 'idle'
    return
  }

  routeProgressPhase.value = 'finishing'
  routeProgressFinishTimer = window.setTimeout(() => {
    routeProgressFinishTimer = null
    routeProgressPhase.value = 'idle'
  }, routeProgressFinishMs)
}

function prefetchRouteView(path: string) {
  const normalizedPath = normalizedRoutePath(path)
  const loader = routeViewLoaders[normalizedPath]
  if (!loader || prefetchedRoutePaths.has(normalizedPath)) return
  prefetchedRoutePaths.add(normalizedPath)
  void loader().catch(() => {
    prefetchedRoutePaths.delete(normalizedPath)
  })
}

function handleNavClick(path: string) {
  beginRouteNavigation(path)
  closeSidebar()
}

const removeRouteBeforeGuard = router.beforeEach((to, from) => {
  if (to.fullPath !== from.fullPath) beginRouteNavigation(to.path)
})
const removeRouteAfterHook = router.afterEach((to) => {
  void nextTick(() => finishRouteNavigation(to.path))
})
const removeRouteErrorHook = router.onError((_error, to) => {
  finishRouteNavigation(to.path)
})

onMounted(() => {
  applyThemeMode(themeMode.value)
  setupSystemThemeListener()
  setupViewportListener()
  window.addEventListener(PUBLIC_SETTINGS_CHANGED_EVENT, handlePublicSettingsChanged)
  window.addEventListener('keydown', handleWindowKeydown)
  document.addEventListener('visibilitychange', handleUpdateVisibilityChange)
  void loadCurrentVersion()
  if (authStore.isAdmin) {
    void checkForUpdates(false, true)
    void pollUpdateTask()
  }
  void loadPublicRuntimeConfig()
})

onBeforeUnmount(() => {
  updateDisposed = true
  if (updateReloadTimer !== null) window.clearTimeout(updateReloadTimer)
  window.removeEventListener(PUBLIC_SETTINGS_CHANGED_EVENT, handlePublicSettingsChanged)
  window.removeEventListener('keydown', handleWindowKeydown)
  document.removeEventListener('visibilitychange', handleUpdateVisibilityChange)
  systemThemeMedia?.removeEventListener('change', handleSystemThemeChange)
  systemThemeMedia = null
  teardownViewportListener()
  clearUpdateTaskPollTimer()
  clearRouteProgressDelayTimer()
  clearRouteProgressFinishTimer()
  removeRouteBeforeGuard()
  removeRouteAfterHook()
  removeRouteErrorHook()
})

</script>

<style scoped>
:global(html.app-mobile-sidebar-open),
:global(body.app-mobile-sidebar-open) {
  overflow: hidden;
}

.route-view-content {
  min-width: 0;
}

.route-view-enter-active {
  transition: opacity 140ms ease-out;
}

.route-view-enter-from {
  opacity: 0.88;
}

.route-progress-enter-active,
.route-progress-leave-active {
  transition: opacity 100ms ease-out;
}

.route-progress-enter-from,
.route-progress-leave-to {
  opacity: 0;
}

.shell-route-progress {
  background: hsl(var(--primary) / 0.12);
}

.shell-route-progress-bar {
  transform: scaleX(0.08);
  transform-origin: left center;
  animation: shell-route-progress-running 1.4s cubic-bezier(0.22, 0.61, 0.36, 1) both;
}

.shell-route-progress-bar--finishing {
  animation: none;
  transform: scaleX(1);
  transition: transform 120ms ease-out;
}

@keyframes shell-route-progress-running {
  0% {
    transform: scaleX(0.08);
  }

  60% {
    transform: scaleX(0.62);
  }

  100% {
    transform: scaleX(0.84);
  }
}

.sidebar-nav-scroll {
  -ms-overflow-style: none;
  scrollbar-width: none;
}

.sidebar-nav-scroll::-webkit-scrollbar {
  display: none;
}

.sidebar-label {
  display: block;
  min-width: 0;
  max-width: 11rem;
  flex: 1 1 auto;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  opacity: 1;
  transform: translateX(0);
  transition:
    max-width 0.2s ease,
    opacity 0.12s ease 0.04s,
    transform 0.2s ease;
}

@media (prefers-reduced-motion: reduce) {
  .route-view-enter-active,
  .route-progress-enter-active,
  .route-progress-leave-active,
  .shell-route-progress-bar,
  .shell-route-progress-bar--finishing {
    animation: none;
    transition: none;
  }

  .shell-route-progress-bar {
    transform: scaleX(0.84);
  }

  .shell-route-progress-bar--finishing {
    transform: scaleX(1);
  }

  .sidebar-label {
    transition: none;
  }
}

</style>

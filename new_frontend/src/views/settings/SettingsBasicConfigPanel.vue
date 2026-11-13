<template>
  <div class="space-y-4">
    <FormSection v-if="startup" title="启动配置 · 环境变量" subtitle="以下是当前进程实际使用的配置。请修改 .env 或容器环境后重启后端；日常业务选项仍在此页保存。">
      <dl class="startup-config-grid">
        <div class="startup-config-path"><dt>配置来源</dt><dd>{{ startup.config_path }}</dd></div>
        <div><dt>监听地址</dt><dd>{{ startup.server_host }} : {{ startup.server_port }}</dd></div>
        <div><dt>数据库类型</dt><dd>PostgreSQL</dd></div>
        <div v-if="startup.database_host"><dt>数据库地址</dt><dd>{{ startup.database_host }} : {{ startup.database_port }}</dd></div>
        <div v-if="startup.database_name"><dt>数据库名称</dt><dd>{{ startup.database_name }}</dd></div>
      </dl>
      <p class="mt-3 text-xs text-muted-foreground">管理员密钥、数据库密码不在页面中展示。</p>
    </FormSection>

    <FormSection title="基础配置">
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
        <FormField label="后台维护间隔">
          <template #label-extra>
            <HelpTip text="单位分钟，控制待核验账号、限流账号和临期 AT 的后台处理频率。" />
          </template>
          <SettingsNumberInput :field="refreshAccountIntervalField" />
        </FormField>

        <FormField label="图片访问地址">
          <template #label-extra>
            <HelpTip text="可选的图片访问前缀。留空自动使用当前访问地址，反向代理无需填写。" />
          </template>
          <Input
            v-model.trim="settings.base_url"
            block
            :disabled="fieldReadOnly('base_url')"
            placeholder="留空自动识别访问地址"
          />
          <p class="mt-1 text-xs text-muted-foreground">留空自动识别域名和协议；仅自定义图片域名时填写。</p>
        </FormField>

        <FormField label="图片自动清理">
          <template #label-extra>
            <HelpTip text="自动删除多少小时前的本地图片。" />
          </template>
          <SettingsNumberInput :field="imageRetentionHoursField" />
        </FormField>

        <FormField label="日志自动清理">
          <template #label-extra>
            <HelpTip text="自动删除多少小时前的控制台调用日志。" />
          </template>
          <SettingsNumberInput :field="logRetentionHoursField" />
        </FormField>

        <FormField label="单账号图片并发">
          <template #label-extra>
            <HelpTip text="限制每个账号同时处理的图片请求数量。默认 1，可设置为 1–3。" />
          </template>
          <SettingsNumberInput :field="imageAccountConcurrencyField" />
        </FormField>

        <FormField label="单次图片数量上限">
          <template #label-extra>
            <HelpTip text="控制单次图片请求允许生成的最大数量。默认 4，最小 1，不限制可填写的最大值。" />
          </template>
          <SettingsNumberInput :field="imageMaxNField" />
        </FormField>

        <FormField label="账号批量任务并发">
          <template #label-extra>
            <HelpTip text="控制账号批量任务的最大并发数。刷新 AT、同步账号与额度、导入核验和后台复查按账号占用并发；启用、禁用、重置、删除和批量保存按整个批次占用一个并发。默认 30，可设置为 1–100；图片生成并发单独设置。" />
          </template>
          <SettingsNumberInput :field="accountProcessingConcurrencyField" />
        </FormField>
      </div>
    </FormSection>

    <FormSection title="请求与任务">
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
        <FormField label="控制台请求超时">
          <template #label-extra>
            <HelpTip text="单位秒，控制日志、图片、账号和设置等控制台请求等待后端响应的最长时间。" />
          </template>
          <SettingsNumberInput :field="consoleRequestTimeoutField" />
        </FormField>

      </div>
    </FormSection>

    <FormSection title="图片生成">
      <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
        <FormField label="图片流等待上限">
          <template #label-extra>
            <HelpTip text="单位秒，限制 ChatGPT 生图 SSE 流的等待时间。" />
          </template>
          <SettingsNumberInput :field="imageStreamTimeoutField" />
        </FormField>

        <FormField label="图片结果等待上限">
          <template #label-extra>
            <HelpTip text="单位秒，SSE 流结束后继续等待上游图片结果的最长时间。" />
          </template>
          <SettingsNumberInput :field="imagePollTimeoutField" />
        </FormField>

        <FormField label="图片首次轮询等待">
          <template #label-extra>
            <HelpTip text="单位秒，开始查询图片结果前先等待的时间。" />
          </template>
          <SettingsNumberInput :field="imagePollInitialWaitField" />
        </FormField>

        <FormField label="图片轮询间隔">
          <template #label-extra>
            <HelpTip text="单位秒，两次图片结果查询之间的等待时间。" />
          </template>
          <SettingsNumberInput :field="imagePollIntervalField" />
        </FormField>
      </div>
    </FormSection>
  </div>
</template>

<script setup lang="ts">
import { FormField, HelpTip, Input } from 'nanocat-ui'
import FormSection from '@/components/ui/FormSection.vue'
import type { Settings, StartupConfiguration } from '@/types/api'
import SettingsNumberInput from '@/views/settings/SettingsNumberInput.vue'
import { settingsFieldReadOnly, type SettingsFields } from '@/views/settings/settingsView'
import type { NumberSettingField } from '@/views/settings/useNumberSettingField'

const props = defineProps<{
  settings: Settings
  startup?: StartupConfiguration
  fields: SettingsFields
  refreshAccountIntervalField: NumberSettingField
  imageRetentionHoursField: NumberSettingField
  logRetentionHoursField: NumberSettingField
  consoleRequestTimeoutField: NumberSettingField
  imagePollTimeoutField: NumberSettingField
  imageStreamTimeoutField: NumberSettingField
  imagePollInitialWaitField: NumberSettingField
  imagePollIntervalField: NumberSettingField
  imageAccountConcurrencyField: NumberSettingField
  imageMaxNField: NumberSettingField
  accountProcessingConcurrencyField: NumberSettingField
}>()

const fieldReadOnly = (path: string) => settingsFieldReadOnly(props.fields, path)
</script>

<style scoped>
.startup-config-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 12px 20px; font-size: 12px; }
.startup-config-grid > div { min-width: 0; }
.startup-config-path { grid-column: 1 / -1; }
.startup-config-grid dt { color: var(--app-text-muted); margin-bottom: 4px; }
.startup-config-grid dd { margin: 0; overflow-wrap: anywhere; font-variant-numeric: tabular-nums; }
@media (max-width: 480px) { .startup-config-grid { grid-template-columns: minmax(0, 1fr); } }
</style>

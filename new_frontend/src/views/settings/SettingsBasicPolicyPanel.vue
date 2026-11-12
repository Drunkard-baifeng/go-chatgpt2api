<template>
  <div class="cpa-policy-sections">
    <FormSection title="账号策略" subtitle="设置异常账号的处理方式与图片请求重试策略。">
      <div class="cpa-policy-grid">
        <SettingSwitch v-model="settings.auto_remove_invalid_accounts" label="自动移除异常账号"
          description="确认鉴权无效后，开启则直接移除账号；关闭则保留异常状态。"
          :disabled="fieldReadOnly('auto_remove_invalid_accounts')" />
        <SettingSwitch v-model="settings.auto_remove_rate_limited_accounts" label="自动移除额度耗尽账号"
          description="仅在远程明确确认图片额度为 0 时移除；代理错误、断流或上游 429 不会删除账号。"
          :disabled="fieldReadOnly('auto_remove_rate_limited_accounts')" />
        <SettingSwitch v-model="settings.image_account_retry_enabled" label="生图失败后尝试其他账号"
          description="生成失败或结果等待超时后尝试其他账号，受最大尝试数和总超时限制；文本结果、输入错误、取消及提交结果不确定时停止。"
          :disabled="fieldReadOnly('image_account_retry_enabled')" class="cpa-policy-wide">
          <FormField label="最大尝试账号数">
            <template #label-extra><HelpTip text="包含第一次使用的账号。默认 4，最小 2，不限制可填写的最大值。" /></template>
            <SettingsNumberInput :field="imageMaxAccountAttemptsField" :disabled="!settings.image_account_retry_enabled" />
          </FormField>
        </SettingSwitch>
      </div>
    </FormSection>
    <FormSection title="图片结果处理" subtitle="管理结果复查、官网会话保留和图片尺寸。">
      <div class="cpa-policy-grid">
        <SettingSwitch v-model="settings.image_parallel_generation" label="多张图片并行生成"
          description="默认开启。一次请求多张图片时，每张分别选择账号并占用并发名额；关闭后逐张串行生成。"
          :disabled="fieldReadOnly('image_parallel_generation')" />
        <SettingSwitch v-model="settings.image_settle_enabled" label="图片二次确认机制"
          description="找到图片后等待指定秒数再复查，减少结果尚未稳定时提前返回。"
          :disabled="fieldReadOnly('image_settle_enabled')">
          <FormField label="二次确认等待（秒）"><SettingsNumberInput :field="imageSettleSecondsField" :disabled="!settings.image_settle_enabled" /></FormField>
        </SettingSwitch>
        <SettingSwitch v-model="settings.image_upscale_enabled" label="图片尺寸不足时自动放大"
          description="默认关闭。仅放大明确指定尺寸且结果不足的图片；自动尺寸和已达到目标尺寸的图片保持原样。"
          :disabled="fieldReadOnly('image_upscale_enabled')" />
        <SettingSwitch v-model="settings.image_remove_conversation_after_result" label="图片成功后删除官网会话"
          description="默认关闭，保留官网会话便于恢复和排查。开启后仅在图片保存成功时尝试隐藏 ChatGPT 官网会话；失败只记日志，不影响图片返回。"
          :disabled="fieldReadOnly('image_remove_conversation_after_result')" class="cpa-policy-wide" />
      </div>
    </FormSection>
    <FormSection title="GIF / SVG 模型" subtitle="按模型启用 GIF 和 SVG 输出。">
      <div class="cpa-policy-grid">
        <SettingSwitch v-for="model in gifModelOptions" :key="model.value"
          :model-value="settings.gif_models.includes(model.value)" :label="model.label"
          description="允许此模型请求 GIF 动画或静态 SVG。"
          :disabled="fieldReadOnly('gif_models')"
          @update:model-value="setGifModel(model.value, $event)" />
      </div>
    </FormSection>
    <FormSection title="控制台日志级别" subtitle="可同时开启多个级别；全部关闭时使用默认 info / warning / error。">
      <div class="cpa-policy-grid cpa-log-levels">
        <SettingSwitch v-for="level in logLevelOptions" :key="level.value"
          :model-value="settings.log_levels.includes(level.value)" :label="level.label"
          :description="logDescriptions[level.value] || level.value" :disabled="fieldReadOnly('log_levels')"
          @update:model-value="$emit('setLogLevel', level.value, $event)" />
      </div>
    </FormSection>
  </div>
</template>
<script setup lang="ts">
import { computed } from 'vue'
import { FormField, HelpTip } from 'nanocat-ui'
import FormSection from '~/components/ui/FormSection.vue'
import SettingSwitch from './SettingSwitch.vue'
import type { Settings } from '@/types/api'
import SettingsNumberInput from '@/views/settings/SettingsNumberInput.vue'
import { settingsFieldOptions, settingsFieldReadOnly, type SettingsFields } from '@/views/settings/settingsView'
import type { NumberSettingField } from '@/views/settings/useNumberSettingField'
const props = defineProps<{
  settings: Settings; fields: SettingsFields
  imageMaxAccountAttemptsField: NumberSettingField; imageSettleSecondsField: NumberSettingField
}>()
defineEmits<{ setLogLevel: [level: string, enabled: boolean] }>()
const fieldReadOnly = (path: string) => settingsFieldReadOnly(props.fields, path)
const logLevelOptions = computed(() => settingsFieldOptions(props.fields, 'log_levels', props.settings.log_levels))
const logDescriptions: Record<string, string> = { debug: '调试细节', info: '常规运行信息', warning: '警告信息', error: '错误信息', critical: '严重错误' }
const gifModelOptions = computed(() => settingsFieldOptions(props.fields, 'gif_models', props.settings.gif_models))

function setGifModel(model: string, enabled: boolean) {
  const next = new Set(props.settings.gif_models)
  if (enabled) next.add(model)
  else next.delete(model)
  props.settings.gif_models = Array.from(next)
}
</script>
<style scoped>
.cpa-policy-sections { min-width:0; }
.cpa-policy-sections > :deep(.cpa-form-section) { padding:24px 0; border:0; border-top:1px solid var(--console-border); border-radius:0; background:transparent; }
.cpa-policy-grid { display:grid; grid-template-columns:repeat(2,minmax(0,1fr)); gap:14px; align-items:start; }
.cpa-policy-wide { grid-column:1 / -1; }
.cpa-log-levels { grid-template-columns:repeat(auto-fit,minmax(180px,1fr)); }
@media(max-width:900px) { .cpa-policy-grid { grid-template-columns:minmax(0,1fr); } }
</style>

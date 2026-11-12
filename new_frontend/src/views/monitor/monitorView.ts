import type {
  MonitorTone,
  RealtimeMonitorRecord,
} from '@/api/monitor'

const TONE_TEXT_CLASSES: Record<MonitorTone, string> = {
  success: 'text-emerald-600 dark:text-emerald-400',
  danger: 'text-rose-600 dark:text-rose-400',
  warning: 'text-amber-600 dark:text-amber-400',
  info: 'text-sky-600 dark:text-sky-400',
  muted: 'text-foreground',
}

export function toneTextClass(tone: MonitorTone) {
  return TONE_TEXT_CLASSES[tone]
}

export function shortCallId(value: unknown) {
  const text = String(value || '')
  return text ? text.slice(0, 8) : '-'
}

const eventTimeFormatter = new Intl.DateTimeFormat('zh-CN', {
  timeZone: 'Asia/Shanghai',
  year: 'numeric', month: '2-digit', day: '2-digit',
  hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23',
})

export function formatMonitorEventTime(value: string | null | undefined): string {
  const text = value?.trim()
  if (!text) return '-'
  // Python 的无时区时间已是北京时间；Go 的 RFC3339 时间需先转换。
  const timestamp = /^\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}:\d{2}(?:\.\d+)?$/.test(text)
    ? `${text.replace(' ', 'T')}+08:00`
    : text
  const date = new Date(timestamp)
  if (Number.isNaN(date.getTime())) return '-'
  return eventTimeFormatter.format(date).replaceAll('/', '-')
}

export function activeRowSignature(row: RealtimeMonitorRecord) {
  return [
    row.call_id,
    row.endpoint,
    row.model,
    row.account_email,
    row.previous_account_email,
    row.presentation.status_label,
    row.presentation.stage_text,
    row.presentation.duration_text,
    row.presentation.metric_digest,
    row.presentation.egress_text,
    row.presentation.account_attempt_text,
  ].join('|')
}

export function recentRowSignature(row: RealtimeMonitorRecord) {
  return [
    row.call_id,
    row.ended_at,
    row.updated_at,
    row.model,
    row.presentation.status_label,
    row.presentation.status_tone,
    row.presentation.duration_text,
    row.presentation.metric_digest,
    row.presentation.account_egress_text,
  ].join('|')
}

export function slowRowSignature(row: RealtimeMonitorRecord) {
  return [
    row.call_id,
    row.ended_at,
    row.updated_at,
    row.model,
    row.endpoint,
    row.presentation.error_text,
    row.presentation.status_tone,
    row.presentation.duration_text,
    row.presentation.slow_reason_code,
    row.presentation.slow_reason,
    ...row.presentation.slow_metrics.map(item => `${item.key}:${item.value_text}:${item.important}`),
  ].join('|')
}

import apiClient from './client'
import type { CallDetailPresentation, AttemptSummary } from './logs'

export type MonitorTone = 'success' | 'danger' | 'warning' | 'info' | 'muted'

export interface MonitorSlowMetric {
  key: string
  label: string
  value_ms: number
  value_text: string
  important: boolean
}

export interface MonitorRecordPresentation {
  status_label: string
  status_tone: MonitorTone
  stage_text: string
  error_text: string
  duration_text: string
  metric_digest: string
  egress_text: string
  account_attempt_text: string
  account_egress_text: string
  tracked_duration_ms: number
  untracked_duration_ms: number
  slow_metrics: MonitorSlowMetric[]
  slow_reason_code: string
  slow_reason: string
}

export interface RealtimeMonitorRecord {
  call_id: string
  status?: string
  endpoint?: string
  model?: string
  ended_at?: string
  updated_at?: string
  account_email?: string
  previous_account_email?: string
  presentation: MonitorRecordPresentation
}

export interface RealtimeMonitorRecordDetail extends RealtimeMonitorRecord {
  events: RealtimeMonitorEvent[]
  detail_presentation?: CallDetailPresentation
  attempts?: AttemptSummary[]
  request_text?: string
  request_text_full?: string
  request_text_truncated?: boolean
  image_urls?: string[]
  public_error?: string
  upstream_error?: string
  upstream_text?: string
  raw_error?: string
  api_status_code?: number
  status_code?: number
  image_requested_count?: number
  image_succeeded_count?: number
  image_failed_count?: number
  switch_count?: number
}

export interface RealtimeMonitorEvent {
  time: string
  call_id: string
  event: string
  label: string
  detail_text: string
  model?: string
  timing_text: string
  account_email?: string
  previous_account_email?: string
  status?: string
  public_error?: string
  error?: string
  switched_account?: boolean
}

export interface MonitorStageCount {
  label: string
  count: number
}

export interface MonitorDiagnosticItem {
  key: string
  label: string
  value: string | number
  meta: string
  tone: MonitorTone
}

export interface MonitorDiagnosticGroup {
  key: string
  title: string
  meta: string
  items: MonitorDiagnosticItem[]
}

export interface RealtimeMonitorResponse {
  schema_version: 1
  updated_at: string
  threadpool: {
    tokens: number
  }
  execution_capacity?: { text: number; image: number; file: number; total: number } | null
  tracking_dropped?: number
  active: RealtimeMonitorRecord[]
  recent: RealtimeMonitorRecord[]
  slow: RealtimeMonitorRecord[]
  completed_window_text: string
  entry_queue_text: string
  active_stage_items: MonitorStageCount[]
  diagnostic_groups: MonitorDiagnosticGroup[]
}

export const monitorApi = {
  realtime() {
    return apiClient.get<never, RealtimeMonitorResponse>('/api/monitor/realtime')
  },
  detail(callId: string) {
    return apiClient.get<never, RealtimeMonitorRecordDetail>(
      `/api/monitor/realtime/${encodeURIComponent(String(callId || '').trim())}`,
    )
  },
}

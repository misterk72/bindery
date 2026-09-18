import { request } from './core'
import type { Book } from './books'
import type { AuthorMonitorMode, MediaType, MonitorNewItems } from './authors'

export type AuthorBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'refresh' | 'set_media_type'
export type AuthorBulkMonitorMode = Exclude<AuthorMonitorMode, 'series'>
export type BookBulkAction = 'monitor' | 'unmonitor' | 'delete' | 'search' | 'set_media_type' | 'exclude'
export type WantedBulkAction = 'search' | 'blocklist' | 'unmonitor'

export interface BulkResult {
  // `code` is a stable machine readable reason for a failed entry, present
  // only where the client should react to the specific cause. Today the one
  // value is 'auto_grab_disabled' (#2669); see util/autoGrabRefusal.
  results: Record<string, { ok: boolean; error?: string; code?: string }>
}

export interface BulkSetAuthorMonitorModeOptions {
  monitorLatestCount?: number
  applyMonitorModeToExisting?: boolean
  // Omit to leave each author's existing value alone. Monitor mode and
  // monitor-new-items are independent settings on the server (#2065).
  monitorNewItems?: MonitorNewItems
}

export const bulkApi = {
  // Wanted
  listWanted: (opts?: { includeExcluded?: boolean }) => {
    const qs = opts?.includeExcluded ? '?includeExcluded=true' : ''
    return request<Book[]>(`/wanted/missing${qs}`)
  },

  // Bulk actions
  bulkActionAuthors: (ids: number[], action: AuthorBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  bulkSetAuthorMonitorMode: (ids: number[], monitorMode: AuthorBulkMonitorMode, opts: BulkSetAuthorMonitorModeOptions = {}) =>
    request<BulkResult>('/author/bulk', {
      method: 'POST',
      body: JSON.stringify({
        ids,
        action: 'set_monitor_mode',
        monitorMode,
        ...(opts.monitorLatestCount !== undefined ? { monitorLatestCount: opts.monitorLatestCount } : {}),
        ...(opts.applyMonitorModeToExisting !== undefined ? { applyMonitorModeToExisting: opts.applyMonitorModeToExisting } : {}),
        ...(opts.monitorNewItems !== undefined ? { monitorNewItems: opts.monitorNewItems } : {}),
      }),
    }),
  searchAuthorWanted: (id: number) =>
    request<BulkResult>('/author/bulk', { method: 'POST', body: JSON.stringify({ ids: [id], action: 'search' }) }),
  bulkActionBooks: (ids: number[], action: BookBulkAction, mediaType?: MediaType) =>
    request<BulkResult>('/book/bulk', { method: 'POST', body: JSON.stringify({ ids, action, ...(mediaType ? { mediaType } : {}) }) }),
  bulkActionWanted: (ids: number[], action: WantedBulkAction) =>
    request<BulkResult>('/wanted/bulk', { method: 'POST', body: JSON.stringify({ ids, action }) }),
}

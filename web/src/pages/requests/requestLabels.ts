import type { TFunction } from 'i18next'
import type { LibraryRequest } from '../../api/client'

// Dispatched on window after an admin approves or declines, so the nav badge
// recounts pending requests without polling.
export const REQUESTS_CHANGED_EVENT = 'bindery:requests-changed'

// The one line status a request shows, shared by the requester's list and
// the admin queue so both say the same thing about the same row.
export function requestStatusLabel(t: TFunction, r: LibraryRequest): string {
  if (r.status === 'declined') return t('requests.status.declined')
  if (r.status === 'pending') return t('requests.status.pending')
  if (r.kind === 'author' && r.booksTotal) {
    return t('requests.status.authorProgress', { imported: r.booksImported ?? 0, total: r.booksTotal })
  }
  return r.fulfilled ? t('requests.status.available') : t('requests.status.approved')
}

export function requestStatusClass(r: LibraryRequest): string {
  if (r.status === 'declined') return 'bg-red-100 text-red-800 dark:bg-red-900/30 dark:text-red-300'
  if (r.status === 'pending') return 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300'
  return r.fulfilled
    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300'
    : 'bg-sky-100 text-sky-800 dark:bg-sky-900/30 dark:text-sky-300'
}

export function mediaTypeLabel(t: TFunction, m: string): string {
  if (m === 'ebook') return t('common.ebook')
  if (m === 'audiobook') return t('common.audiobook')
  if (m === 'both') return t('common.both')
  return t('requests.anyFormat')
}

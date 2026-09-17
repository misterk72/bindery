import { forwardRef, type KeyboardEvent } from 'react'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { AdoptionItem } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import MediaBadge from '../../components/MediaBadge'
import { formatBytes } from '../../util/format'
import { adoptionHint, scorePercent, unitDisplayName } from './adoptionHint'
import type { Outcome } from './adoptionReducer'

interface Props {
  item: AdoptionItem
  outcome: Outcome | undefined
  error: string | undefined
  expanded: boolean
  focusable: boolean
  editorId: string
  onFocusRow: () => void
  onKeyDown: (e: KeyboardEvent<HTMLTableRowElement>) => void
  onToggle: () => void
  onConfirm: () => void
  onIgnore: () => void
  onUndo: () => void
  onAddAuthor: (name: string) => void
}

// A score pill reads green when the suggestion is close, amber when it is a
// real question.
function scoreCls(percent: number): string {
  return percent >= 80
    ? 'bg-emerald-100 text-emerald-800 dark:bg-emerald-950 dark:text-emerald-300'
    : 'bg-amber-100 text-amber-800 dark:bg-amber-950 dark:text-amber-300'
}

// AdoptionRow is one book in the collapsed list: what it is, a sentence about
// why it is here, and the action that most likely settles it. With a
// suggestion that is a single Confirm. A decision replaces the sentence and
// actions with a quiet line that keeps Undo until the next fetch.
const AdoptionRow = forwardRef<HTMLTableRowElement, Props>(function AdoptionRow(
  { item, outcome, error, expanded, focusable, editorId, onFocusRow, onKeyDown, onToggle, onConfirm, onIgnore, onUndo, onAddAuthor }, ref,
) {
  const { t } = useTranslation()
  const name = unitDisplayName(item)
  const hint = adoptionHint(item, t)
  const top = item.candidates[0]
  const size = formatBytes(item.sizeBytes)
  const cell = 'block md:table-cell px-3 py-1 md:py-2.5 align-top'

  const outcomeLine = (() => {
    if (!outcome) return null
    switch (outcome.kind) {
      case 'adopting':
        return t('adoption.outcome.adopting', { title: outcome.preview?.title ?? name, defaultValue: 'Adopting as {{title}}…' })
      case 'adopted': {
        const book = outcome.item.book
        const base = t('adoption.outcome.adopted', { title: book?.title ?? name, author: book?.authorName ?? '', defaultValue: 'Adopted as {{title}} by {{author}}.' })
        return outcome.item.bookCreated ? `${base} ${t('adoption.outcome.created', 'Added to your library, unmonitored.')}` : base
      }
      case 'ignoring':
      case 'ignored':
        return t('adoption.outcome.ignored', 'Ignored. Later scans keep it out of this list.')
      case 'undoing':
        return t('adoption.outcome.undoing', 'Undoing…')
      case 'restored':
        return t('adoption.outcome.restored', 'Back in Needs a decision.')
    }
  })()
  const canUndo = outcome?.kind === 'adopted' || outcome?.kind === 'ignored'
  // When the scan could not place the author at all, adding the author is the
  // decision; choosing a book stays available beside it.
  const addAuthorFirst = item.state === 'pending' && !top && item.reason === 'author_not_in_library' && item.parsedAuthor !== ''

  return (
    <tr
      ref={ref}
      tabIndex={focusable ? 0 : -1}
      onFocus={e => { if (e.target === e.currentTarget) onFocusRow() }}
      onKeyDown={onKeyDown}
      aria-label={name}
      className={`block md:table-row mb-3 md:mb-0 rounded-lg md:rounded-none border md:border-0 border-slate-200 dark:border-zinc-800 outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-emerald-500 ${
        expanded ? 'bg-emerald-500/5' : 'bg-slate-100/50 dark:bg-zinc-900/50 hover:bg-slate-200/50 dark:hover:bg-zinc-800/50'
      }`}
    >
      <td className={`${cell} pt-3 md:pt-2.5 min-w-0 md:w-[36%]`}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-slate-800 dark:text-zinc-200">{name}</span>
          <MediaBadge type={item.format} />
          <span className="text-[11px] text-fg-muted">
            {t('adoption.row.files', { count: item.fileCount, defaultValue: '{{count}} files' })}{size ? ` · ${size}` : ''}
          </span>
        </div>
        <div className="mt-0.5 max-w-md truncate font-mono text-xs text-slate-500 dark:text-zinc-500" title={`${item.rootPath}/${item.relPath}`}>
          {item.relPath}
        </div>
      </td>

      {outcomeLine ? (
        <td className={`${cell} pb-3 md:pb-2.5`} colSpan={2}>
          <p role="status" className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-emerald-700 dark:text-emerald-400">
            <span>{outcome?.kind === 'adopted' || outcome?.kind === 'ignored' ? '✓ ' : ''}{outcomeLine}</span>
            {canUndo && (
              <button type="button" onClick={onUndo} aria-keyshortcuts="u" className="font-medium underline-offset-2 hover:underline">
                {t('adoption.undo', 'Undo')}
              </button>
            )}
          </p>
        </td>
      ) : (
        <>
          <td className={`${cell} md:w-[30%]`}>
            <p className="text-xs text-slate-600 dark:text-zinc-400" title={hint.tooltip || undefined}>
              {item.state === 'adopted' && item.book ? (
                <>
                  {t('adoption.row.adoptedAs', 'Adopted as')}{' '}
                  <Link to={`/book/${item.book.id}`} className="font-medium text-slate-800 dark:text-zinc-200 hover:text-emerald-600">{item.book.title}</Link>
                  {item.book.authorName ? ` · ${item.book.authorName}` : ''}
                </>
              ) : hint.sentence}
            </p>
            {error && <p role="alert" className="mt-1 text-xs text-red-600 dark:text-red-400">{error}</p>}
          </td>
          <td className={`${cell} pb-3 md:pb-2.5 md:text-right md:whitespace-nowrap`}>
            <div className="flex flex-wrap md:flex-nowrap items-center gap-2 md:justify-end">
              {item.state === 'pending' && top && (
                <>
                  <span className="max-w-[12rem] truncate text-xs text-slate-700 dark:text-zinc-300" title={`${top.book.title} · ${top.book.authorName}`}>
                    {top.book.title}
                  </span>
                  <span className={`px-1.5 py-0.5 rounded text-[10px] font-medium ${scoreCls(scorePercent(top.score))}`}>
                    {scorePercent(top.score)}%
                  </span>
                  <button type="button" onClick={onConfirm} className={`${btn.primary} ${btnSize.sm}`}>
                    {t('adoption.confirm', 'Confirm')}
                  </button>
                </>
              )}
              {addAuthorFirst && (
                <button type="button" onClick={() => onAddAuthor(item.parsedAuthor)} className={`${btn.secondary} ${btnSize.sm}`}>
                  {t('adoption.rail.addAuthor', 'Add author')}
                </button>
              )}
              {item.state === 'pending' && (
                <button
                  type="button"
                  onClick={onToggle}
                  aria-expanded={expanded}
                  aria-controls={editorId}
                  className={`${top || addAuthorFirst ? btn.ghost : btn.secondary} ${btnSize.sm}`}
                >
                  {top ? t('adoption.other', 'Other book') : t('adoption.choose', 'Choose book')}
                </button>
              )}
              {item.state === 'pending' && (
                <button type="button" onClick={onIgnore} aria-keyshortcuts="i" className={`${btn.ghost} ${btnSize.sm}`}>
                  {t('adoption.ignore', 'Ignore')}
                </button>
              )}
              {(item.state === 'ignored' || item.state === 'adopted') && (
                <button type="button" onClick={onUndo} aria-keyshortcuts="u" className={`${btn.secondary} ${btnSize.sm}`}>
                  {item.state === 'ignored' ? t('adoption.unignore', 'Unignore') : t('adoption.undo', 'Undo')}
                </button>
              )}
            </div>
          </td>
        </>
      )}
    </tr>
  )
})

export default AdoptionRow

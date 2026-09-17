import { Fragment, useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { useTranslation } from 'react-i18next'
import type { AdoptionItem, AdoptionSort } from '../../api/client'
import SortHeader from '../../components/SortHeader'
import AdoptionEditor from './AdoptionEditor'
import AdoptionRow from './AdoptionRow'
import type { AdoptionList } from './useAdoptionList'

interface Props {
  list: AdoptionList
  onSearchShortcut: () => void
  onAddAuthor: (name: string) => void
}

// Natural direction per column: scores, file counts and sizes read biggest
// first, names read A to Z.
const NATURAL_DESC: Record<AdoptionSort, boolean> = { score: true, files: true, size: true, seen: true, title: false, folder: false }

// AdoptionTable is the list itself: a real table on wide screens, stacked
// cards on narrow ones, operable from the keyboard. One row holds the tab stop
// (roving tabindex); arrows move it, Enter opens the row's editor in place,
// Esc closes it and returns focus to the row, i ignores, u undoes, and /
// jumps to the search box.
export default function AdoptionTable({ list, onSearchShortcut, onAddAuthor }: Props) {
  const { t } = useTranslation()
  const { state, filters } = list
  const [focusIndex, setFocusIndex] = useState(0)
  const rowRefs = useRef<(HTMLTableRowElement | null)[]>([])
  const restoreFocusTo = useRef<number | null>(null)
  const items = state.items
  const direction: 'asc' | 'desc' = filters.dir || (NATURAL_DESC[filters.sort] ? 'desc' : 'asc')

  useEffect(() => { setFocusIndex(i => Math.min(i, Math.max(0, items.length - 1))) }, [items.length])

  // After the editor closes, focus goes back to the row it belonged to.
  useEffect(() => {
    if (state.expandedId === null && restoreFocusTo.current !== null) {
      rowRefs.current[restoreFocusTo.current]?.focus()
      restoreFocusTo.current = null
    }
  }, [state.expandedId])

  const sortBy = (key: AdoptionSort) => {
    if (key === filters.sort) list.setFilter({ dir: direction === 'asc' ? 'desc' : 'asc' })
    else list.setFilter({ sort: key, dir: '' })
  }

  const closeEditor = (index: number) => {
    restoreFocusTo.current = index
    list.collapse()
  }

  const confirm = (item: AdoptionItem) => {
    const top = item.candidates[0]
    if (top) void list.adopt(item, { bookId: top.book.id }, top.book)
  }

  const onRowKey = (e: KeyboardEvent<HTMLTableRowElement>, index: number, item: AdoptionItem) => {
    if (e.target !== e.currentTarget) return
    const outcome = state.outcomes[item.id]
    const move = (to: number) => {
      e.preventDefault()
      const next = Math.max(0, Math.min(items.length - 1, to))
      setFocusIndex(next)
      rowRefs.current[next]?.focus()
    }
    switch (e.key) {
      case 'ArrowDown': return move(index + 1)
      case 'ArrowUp': return move(index - 1)
      case 'Home': return move(0)
      case 'End': return move(items.length - 1)
      case 'Enter':
        e.preventDefault()
        if (outcome || item.state !== 'pending') return
        if (state.expandedId === item.id) closeEditor(index)
        else list.expand(item.id)
        return
      case 'Escape':
        if (state.expandedId === item.id) closeEditor(index)
        return
      case 'i':
        if (!outcome && item.state === 'pending') { e.preventDefault(); void list.ignore(item) }
        return
      case 'u':
        if (outcome?.kind === 'adopted' || outcome?.kind === 'ignored' || (!outcome && item.state !== 'pending')) {
          e.preventDefault()
          void list.undo(item)
        }
        return
      case '/':
        e.preventDefault()
        onSearchShortcut()
    }
  }

  const captionKey = filters.state === 'ignored' ? 'adoption.caption.ignored' : filters.state === 'adopted' ? 'adoption.caption.adopted' : 'adoption.caption.pending'
  const captionDefault = filters.state === 'ignored' ? 'Ignored books in your library' : filters.state === 'adopted' ? 'Books you adopted from your library' : 'Books in your library that need a decision'

  return (
    <div className="md:border md:border-slate-200 md:dark:border-zinc-800 md:rounded-lg md:overflow-hidden">
      <table className="block md:table w-full text-sm">
        <caption className="sr-only">
          {t(captionKey, captionDefault)}. {t('adoption.caption.keys', 'Arrow keys move between books, Enter opens one, i ignores, u undoes.')}
        </caption>
        <thead className="hidden md:table-header-group">
          <tr className="bg-slate-100 dark:bg-zinc-900 border-b border-slate-200 dark:border-zinc-800">
            <SortHeader label={t('adoption.col.book', 'Book')} sortKey="title" activeKey={filters.sort} direction={direction} onSort={sortBy} />
            <th scope="col" className="text-left px-3 py-2 text-xs font-medium uppercase text-slate-600 dark:text-zinc-400">
              {t('adoption.col.why', 'Why it is here')}
            </th>
            <SortHeader label={t('adoption.col.match', 'Best match')} sortKey="score" activeKey={filters.sort} direction={direction} onSort={sortBy} className="text-right" />
          </tr>
        </thead>
        <tbody className="block md:table-row-group md:divide-y md:divide-slate-200 md:dark:divide-zinc-800">
          {items.map((item, index) => {
            const expanded = state.expandedId === item.id
            const editorId = `adoption-editor-${item.id}`
            return (
              <Fragment key={item.id}>
                <AdoptionRow
                  ref={el => { rowRefs.current[index] = el }}
                  item={item}
                  outcome={state.outcomes[item.id]}
                  error={state.errors[item.id]}
                  expanded={expanded}
                  focusable={index === focusIndex}
                  editorId={editorId}
                  onFocusRow={() => setFocusIndex(index)}
                  onKeyDown={e => onRowKey(e, index, item)}
                  onToggle={() => (expanded ? closeEditor(index) : list.expand(item.id))}
                  onConfirm={() => confirm(item)}
                  onIgnore={() => void list.ignore(item)}
                  onUndo={() => void list.undo(item)}
                  onAddAuthor={onAddAuthor}
                />
                {expanded && (
                  <tr className="block md:table-row -mt-3 mb-3 md:m-0 rounded-b-lg md:rounded-none border md:border-0 border-t-0 border-slate-200 dark:border-zinc-800 bg-emerald-500/5">
                    <td id={editorId} colSpan={3} className="block md:table-cell px-3 pb-4 pt-1 md:px-6">
                      <AdoptionEditor
                        item={item}
                        onAdopt={(target, preview) => list.adopt(item, target, preview)}
                        onCancel={() => closeEditor(index)}
                      />
                    </td>
                  </tr>
                )}
              </Fragment>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

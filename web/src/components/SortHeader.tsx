import { useTranslation } from 'react-i18next'

interface Props<K extends string> {
  label: string
  // The sort key this column sorts by.
  sortKey: K
  // The active sort key and direction.
  activeKey: K
  direction: 'asc' | 'desc'
  // Clicking the active column flips direction; another column starts at its
  // natural direction.
  onSort: (key: K) => void
  className?: string
}

// SortHeader is a clickable column header with an arrow for the active sort.
// BooksPage and AuthorDetailPage each carry their own copy; this is the third,
// so it lives here, and moving those two onto it is a separate change.
//
// Tailwind v4's Preflight gives <button> no pointer cursor, so the header sets
// cursor-pointer itself; without it a sortable column reads as plain text.
export default function SortHeader<K extends string>({ label, sortKey, activeKey, direction, onSort, className = '' }: Props<K>) {
  const { t } = useTranslation()
  const active = sortKey === activeKey
  return (
    <th
      scope="col"
      aria-sort={active ? (direction === 'asc' ? 'ascending' : 'descending') : 'none'}
      className={`text-left px-3 py-2 text-xs font-medium uppercase ${className}`}
    >
      <button
        type="button"
        onClick={() => onSort(sortKey)}
        title={t('books.sortByColumn', { column: label, defaultValue: 'Sort by {{column}}' })}
        className={`inline-flex items-center gap-0.5 uppercase transition-colors cursor-pointer select-none ${active ? 'text-slate-900 dark:text-white' : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'}`}
      >
        {label}
        {active
          ? <span aria-hidden="true">{direction === 'asc' ? ' ▲' : ' ▼'}</span>
          : <span aria-hidden="true" className="opacity-40">↕</span>}
      </button>
    </th>
  )
}

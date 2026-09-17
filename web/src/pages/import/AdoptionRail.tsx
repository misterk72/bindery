import { useTranslation } from 'react-i18next'
import type { AdoptionFolderFacet } from '../../api/client'

interface Props {
  folders: AdoptionFolderFacet[]
  activeFolder: string
  onShow: (folder: string) => void
  onAddAuthor: (name: string) => void
  onIgnoreFolder: (folder: string) => void
}

// The structure rail: the author folders holding the most undecided books,
// each with the one action its books call for. When every book in a folder
// failed because its author is not in the library, that is one decision for
// the whole folder (add the author, then scan), not a decision per file.
// Anything else is a folder worth looking at, so it filters the list.
//
// A column beside the list on wide screens, a row of scrolling chips on narrow
// ones.
export default function AdoptionRail({ folders, activeFolder, onShow, onAddAuthor, onIgnoreFolder }: Props) {
  const { t } = useTranslation()
  if (folders.length === 0) return null

  return (
    <nav aria-label={t('adoption.rail.label', 'Folders with the most books to decide')} className="mb-4 lg:mb-0">
      <h3 className="hidden lg:block mb-2 text-[11px] font-semibold uppercase tracking-wide text-fg-muted">
        {t('adoption.rail.heading', 'Folders')}
      </h3>
      <ul className="flex gap-2 overflow-x-auto pb-1 lg:flex-col lg:overflow-visible lg:pb-0">
        {folders.map(f => {
          const active = f.folder === activeFolder
          const wholeFolderMissing = f.notInLibrary > 0 && f.notInLibrary === f.units
          const authorName = f.author || f.folder
          return (
            <li
              key={f.folder}
              className={`flex-shrink-0 w-56 lg:w-auto rounded-lg border px-3 py-2 transition-colors ${
                active
                  ? 'border-emerald-500 bg-emerald-500/5'
                  : 'border-slate-200 dark:border-zinc-800 bg-slate-100/50 dark:bg-zinc-900/50'
              }`}
            >
              <p className="truncate text-sm font-medium text-slate-800 dark:text-zinc-200" title={f.folder}>{f.folder}</p>
              <p className="text-[11px] text-fg-muted">
                {t('adoption.rail.counts', { count: f.units, files: f.files, defaultValue: '{{count}} books, {{files}} files' })}
              </p>
              {wholeFolderMissing && (
                <p className="text-[11px] text-amber-700 dark:text-amber-400">
                  {t('adoption.rail.notInLibrary', { author: authorName, defaultValue: '{{author}} is not in your library' })}
                </p>
              )}
              <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
                {wholeFolderMissing ? (
                  <button
                    type="button"
                    onClick={() => onAddAuthor(authorName)}
                    className="font-medium text-emerald-700 dark:text-emerald-400 hover:underline"
                  >
                    {t('adoption.rail.addAuthor', 'Add author')}
                  </button>
                ) : (
                  <button
                    type="button"
                    onClick={() => onShow(active ? '' : f.folder)}
                    aria-pressed={active}
                    className="font-medium text-emerald-700 dark:text-emerald-400 hover:underline"
                  >
                    {active ? t('adoption.rail.showAll', 'Show all') : t('adoption.rail.show', 'Show')}
                  </button>
                )}
                {wholeFolderMissing && (
                  <button
                    type="button"
                    onClick={() => onShow(active ? '' : f.folder)}
                    aria-pressed={active}
                    className="text-slate-600 dark:text-zinc-400 hover:underline"
                  >
                    {active ? t('adoption.rail.showAll', 'Show all') : t('adoption.rail.show', 'Show')}
                  </button>
                )}
                <button
                  type="button"
                  onClick={() => onIgnoreFolder(f.folder)}
                  className="text-slate-500 dark:text-zinc-500 hover:text-slate-800 dark:hover:text-zinc-200 hover:underline"
                >
                  {t('adoption.rail.ignore', 'Ignore folder')}
                </button>
              </div>
            </li>
          )
        })}
      </ul>
    </nav>
  )
}

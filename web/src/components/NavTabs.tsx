import { Link, useLocation } from 'react-router'
import { useTranslation } from 'react-i18next'
import type { ReactNode } from 'react'
import { matchesPath, type NavEntry, type NavItem } from './navGroups'

// The tab strip a grouped page carries above its content. Styled like the
// segmented tabs on the Import page so it reads as the same control, but the
// tabs are real links: the URL changes, the back button works, and each page
// stays bookmarkable.
export default function NavTabs({ group, renderLabel }: { group: NavEntry; renderLabel: (item: NavItem) => ReactNode }) {
  const { t } = useTranslation()
  const { pathname } = useLocation()
  const tabs = group.children ?? []
  if (tabs.length === 0) return null

  return (
    <nav
      aria-label={t(`nav.${group.key}`)}
      className="mb-5 inline-flex gap-1 p-1 rounded-lg bg-slate-200/70 dark:bg-zinc-900 border border-slate-200 dark:border-zinc-800"
    >
      {tabs.map(tab => {
        const active = matchesPath(pathname, tab)
        return (
          <Link
            key={tab.to}
            to={tab.to}
            aria-current={active ? 'page' : undefined}
            className={`px-3 py-1.5 rounded-md text-sm font-medium transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-emerald-500 ${
              active
                ? 'bg-white dark:bg-zinc-800 text-slate-900 dark:text-white shadow-sm'
                : 'text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white'
            }`}
          >
            {renderLabel(tab)}
          </Link>
        )
      })}
    </nav>
  )
}

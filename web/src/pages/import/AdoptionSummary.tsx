import { useTranslation } from 'react-i18next'
import type { AdoptionScanStatus, AdoptionSummary as Summary } from '../../api/client'
import { btn, btnSize } from '../../components/buttons'
import { relativeTime } from './adoptionHint'

interface Props {
  summary: Summary | null
  scan: AdoptionScanStatus | null
  onScan: () => void
  // Set after an author is added from the rail: the next useful step is a scan.
  nudgeScan: boolean
}

// The strip above the list: how much is left to decide, when the library was
// last looked at, and the one control that refreshes it.
export default function AdoptionSummary({ summary, scan, onScan, nudgeScan }: Props) {
  const { t, i18n } = useTranslation()
  const running = Boolean(scan?.running)
  const pending = summary?.pending ?? 0

  let headline: string
  if (running) headline = t('adoption.summary.scanning', 'Scanning your library…')
  else if (pending > 0) {
    headline = t('adoption.summary.pending', {
      count: pending,
      files: summary?.pendingFiles ?? 0,
      defaultValue: '{{count}} books need a decision, {{files}} files',
    })
  } else headline = t('adoption.summary.none', 'Nothing needs a decision')

  const ranAt = scan?.ran ? relativeTime(scan.ranAt, i18n.language) : ''

  return (
    <section
      aria-label={t('adoption.summary.label', 'Library scan summary')}
      className="mb-5 rounded-lg border border-slate-200 dark:border-zinc-800 bg-slate-100 dark:bg-zinc-900 px-4 py-3"
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="min-w-0 flex-1" aria-live="polite">
          <p className="flex items-center gap-2 text-sm font-medium text-slate-900 dark:text-white">
            {running && (
              <span aria-hidden="true" className="relative flex h-2 w-2">
                <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
                <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-500" />
              </span>
            )}
            {headline}
          </p>
          <p className="mt-0.5 text-xs text-fg-muted">
            {ranAt
              ? t('adoption.summary.lastScan', { when: ranAt, defaultValue: 'Last scan {{when}}' })
              : t('adoption.summary.neverScanned', 'Not scanned yet')}
            {summary && summary.ignored > 0 && (
              <> · {t('adoption.summary.ignored', { count: summary.ignored, defaultValue: '{{count}} ignored' })}</>
            )}
            {summary && summary.adopted > 0 && (
              <> · {t('adoption.summary.adopted', { count: summary.adopted, defaultValue: '{{count}} adopted' })}</>
            )}
          </p>
        </div>
        {nudgeScan && !running && (
          <p className="text-xs text-emerald-700 dark:text-emerald-400">
            {t('adoption.summary.nudge', 'Author added. Scan now to match their files.')}
          </p>
        )}
        <button
          type="button"
          onClick={onScan}
          disabled={running}
          className={`${nudgeScan ? btn.primary : btn.secondary} ${btnSize.md}`}
        >
          {running ? t('adoption.summary.scanningButton', 'Scanning…') : t('adoption.summary.scanNow', 'Scan now')}
        </button>
      </div>
      {scan?.error && (
        <p role="alert" className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          {t('adoption.summary.scanError', { error: scan.error, defaultValue: 'The last scan did not finish: {{error}}' })}
        </p>
      )}
      {!scan?.error && scan?.ran && scan.noFilesFound && (
        <p className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          {t('adoption.summary.noFiles', 'The last scan found no book files. Check that the library folder is mounted, then scan again. Your decisions were kept.')}
        </p>
      )}
      {scan?.truncated && (
        <p className="mt-2 text-xs text-amber-700 dark:text-amber-400">
          {t('adoption.summary.truncated', 'This library is larger than one scan lists. Adopt or ignore some books and scan again to see the rest.')}
        </p>
      )}
    </section>
  )
}

import { useTranslation } from 'react-i18next'
import type { DiagnoseCheck, DiagnoseResult, DiagnoseStatus } from '../../api/client'
import ClipboardManualFallback from '../../components/ClipboardManualFallback'
import { useClipboardCopy } from '../../components/useClipboardCopy'
import { buildDiagnoseReport } from './helpers'

const dotCls: Record<DiagnoseStatus, string> = {
  pass: 'bg-emerald-500',
  warn: 'bg-amber-500',
  fail: 'bg-red-500',
  skipped: 'bg-slate-400 dark:bg-zinc-600',
  unknown: 'bg-slate-400 dark:bg-zinc-500',
}

const fixCls = (status: DiagnoseStatus | undefined) => status === 'fail'
  ? 'bg-red-100 text-red-800 dark:bg-red-950/30 dark:text-red-300 border-red-300 dark:border-red-900'
  : 'bg-amber-100 text-amber-800 dark:bg-amber-900/30 dark:text-amber-300 border-amber-300 dark:border-amber-900'

interface Props {
  result: DiagnoseResult
  onClose: () => void
}

export default function ClientDiagnosePanel({ result, onClose }: Props) {
  const { t } = useTranslation()
  const clipboard = useClipboardCopy()
  const firstProblem = result.checks.find(c => c.status === 'fail') ?? result.checks.find(c => c.status === 'warn')
  const labelCls = 'text-slate-600 dark:text-zinc-400'
  const checkTitle = (c: DiagnoseCheck) => t(`settings.clients.diagnose.check.${c.code}`, c.code)

  return (
    <section
      aria-label={t('settings.clients.diagnose.heading')}
      className="mt-1 p-3 border border-slate-200 dark:border-zinc-800 rounded-lg bg-slate-50 dark:bg-zinc-900/60 text-xs space-y-3"
    >
      <div className="flex items-center justify-between gap-2">
        <h5 className="font-semibold text-sm">{t('settings.clients.diagnose.heading')}</h5>
        <div className="flex items-center gap-3">
          <button
            type="button"
            onClick={() => { void clipboard.copy(buildDiagnoseReport(result)) }}
            className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white"
          >
            {clipboard.status === 'copied' ? t('settings.clients.diagnose.copied') : t('settings.clients.diagnose.copyReport')}
          </button>
          <button type="button" onClick={onClose} className="text-xs text-slate-600 dark:text-zinc-400 hover:text-slate-900 dark:hover:text-white">
            {t('settings.clients.diagnose.close')}
          </button>
        </div>
      </div>

      {clipboard.status === 'manual' && <ClipboardManualFallback text={clipboard.manualText} />}

      {result.primaryFix ? (
        <div role="alert" className={`px-3 py-2 border rounded ${fixCls(firstProblem?.status)}`}>
          <span className="font-medium">{t('settings.clients.diagnose.primaryFix')}</span> {result.primaryFix}
        </div>
      ) : (
        <div role="status" className="px-3 py-2 rounded bg-emerald-100 text-emerald-800 dark:bg-emerald-900/30 dark:text-emerald-300">
          {t('settings.clients.diagnose.allClear')}
        </div>
      )}

      <ul className="space-y-1.5" aria-label={t('settings.clients.diagnose.checksLabel')}>
        {result.checks.map(c => (
          <li key={c.code} className="flex items-start gap-2">
            <span className={`inline-block w-2 h-2 mt-1 rounded-full flex-shrink-0 ${dotCls[c.status] ?? dotCls.unknown}`} aria-hidden="true" />
            <div className="min-w-0">
              <div>
                <span className="font-medium">{checkTitle(c)}</span>
                <span className="ml-2 text-slate-500 dark:text-zinc-500">{t(`settings.clients.diagnose.status.${c.status}`, c.status)}</span>
              </div>
              <p className={`${labelCls} break-words`}>{c.message}</p>
              {c.fix && c.status !== 'pass' && c.status !== 'skipped' && (
                <p className="text-slate-500 dark:text-zinc-500 break-words">{c.fix}</p>
              )}
            </div>
          </li>
        ))}
      </ul>

      <dl className="grid grid-cols-1 sm:grid-cols-[max-content_1fr] gap-x-4 gap-y-1">
        <dt className={labelCls}>{t('settings.clients.diagnose.clientPath')}</dt>
        <dd className="font-mono break-all">{result.paths.clientPath || t('settings.clients.diagnose.none')}</dd>
        <dt className={labelCls}>{t('settings.clients.diagnose.remapRule')}</dt>
        <dd>{result.paths.remapRule ? t(`settings.clients.diagnose.remap.${result.paths.remapRule}`, result.paths.remapRule) : t('settings.clients.diagnose.none')}</dd>
        <dt className={labelCls}>{t('settings.clients.diagnose.localPath')}</dt>
        <dd className="font-mono break-all">{result.paths.localPath || t('settings.clients.diagnose.none')}</dd>
      </dl>

      {result.hardlinks.length > 0 && (
        <div className="overflow-x-auto">
          <table className="w-full text-left">
            <caption className="sr-only">{t('settings.clients.diagnose.hardlinksCaption')}</caption>
            <thead>
              <tr className={labelCls}>
                <th scope="col" className="py-1 pr-3 font-medium">{t('settings.clients.diagnose.libraryFolder')}</th>
                <th scope="col" className="py-1 pr-3 font-medium">{t('settings.clients.diagnose.hardlinks')}</th>
                <th scope="col" className="py-1 font-medium">{t('settings.clients.diagnose.reason')}</th>
              </tr>
            </thead>
            <tbody>
              {result.hardlinks.map(h => (
                <tr key={h.root} className="border-t border-slate-200 dark:border-zinc-800">
                  <td className="py-1 pr-3 font-mono break-all">{h.root}</td>
                  <td className="py-1 pr-3">{h.linkable ? t('settings.clients.diagnose.yes') : t('settings.clients.diagnose.no')}</td>
                  <td className={`py-1 ${labelCls}`}>{h.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  )
}

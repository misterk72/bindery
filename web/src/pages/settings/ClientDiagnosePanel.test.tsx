import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react'
import ClientDiagnosePanel from './ClientDiagnosePanel'
import type { DiagnoseResult } from '../../api/client'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string, fallback?: unknown) => typeof fallback === 'string' ? fallback : key,
    i18n: { changeLanguage: vi.fn() },
  }),
}))

// A fixture with every status, split ebook and audiobook folders, and the
// host and username of the client quoted inside the server's sentences, which
// is exactly what the copy report must not carry.
function fixture(): DiagnoseResult {
  return {
    clientType: 'qbittorrent',
    checks: [
      { code: 'config', status: 'pass', message: 'The saved settings are usable.' },
      { code: 'connect', status: 'pass', message: 'Connected to qBittorrent at http://qbit.lan:8080 as admin-user.' },
      { code: 'client_path', mediaType: 'ebook', status: 'warn', message: 'Ebook note.', fix: 'Check the label.' },
      { code: 'local_path', mediaType: 'audiobook', status: 'fail', message: 'Audiobook folder missing.', fix: 'Add a path remap.' },
      { code: 'hardlinks', status: 'skipped', message: 'Skipped because an earlier check failed.' },
      { code: 'indexer_reach', status: 'unknown', message: 'Bindery cannot test indexer reach.', fix: 'Check the client network.' },
    ],
    paths: [
      { mediaType: 'ebook', clientPath: '/remote/books', source: 'the category save path', remapRule: 'client', localPath: '/downloads/books' },
      { mediaType: 'audiobook', clientPath: '/remote/audio', source: 'the category save path', remapRule: 'client', localPath: '/downloads/audio' },
    ],
    hardlinks: [
      { mediaType: 'ebook', downloadPath: '/downloads/books', root: '/library', result: 'no', linkable: false, reason: 'EXDEV' },
      { mediaType: 'audiobook', downloadPath: '/downloads/books', root: '/library', result: 'missing', linkable: false, reason: 'the library folder does not exist' },
    ],
    primaryFix: 'Add a path remap.',
  }
}

describe('ClientDiagnosePanel', () => {
  let written = ''
  beforeEach(() => {
    written = ''
    Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true })
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn(async (text: string) => { written = text }) },
      configurable: true,
    })
  })

  it('shows the primary fix, every status and both folders', () => {
    render(<ClientDiagnosePanel result={fixture()} onClose={() => {}} />)

    expect(screen.getByRole('alert')).toHaveTextContent('Add a path remap.')
    const checks = screen.getByRole('list', { name: 'settings.clients.diagnose.checksLabel' })
    for (const status of ['pass', 'warn', 'fail', 'skipped', 'unknown']) {
      expect(within(checks).getAllByText(status).length).toBeGreaterThan(0)
    }
    expect(checks).toHaveTextContent('client_path (settings.clients.diagnose.media.ebook)')
    expect(checks).toHaveTextContent('local_path (settings.clients.diagnose.media.audiobook)')
    expect(screen.getByText('/remote/audio')).toBeInTheDocument()
    expect(screen.getAllByText('the category save path')).toHaveLength(2)
    const table = screen.getByRole('table', { name: 'settings.clients.diagnose.hardlinksCaption' })
    expect(table).toHaveTextContent('/downloads/books')
    expect(table).toHaveTextContent('EXDEV')
    // Two rows share a download folder and root; both render (distinct keys).
    expect(within(table).getAllByRole('row')).toHaveLength(3)
    expect(table).toHaveTextContent('settings.clients.diagnose.linkResult.missing')
  })

  it('copies a report without the host or the username', async () => {
    render(<ClientDiagnosePanel result={fixture()} onClose={() => {}} />)
    fireEvent.click(screen.getByRole('button', { name: 'settings.clients.diagnose.copyReport' }))

    await waitFor(() => expect(written).not.toBe(''))
    expect(written).toContain('client type: qbittorrent')
    expect(written).toContain('local_path (audiobook): fail')
    expect(written).toContain('client path (ebook): /remote/books from the category save path')
    expect(written).not.toContain('qbit.lan')
    expect(written).not.toContain('admin-user')
    expect(written).not.toContain('8080')
  })

  it('says all clear when there is nothing to fix', () => {
    render(<ClientDiagnosePanel result={{ ...fixture(), checks: [{ code: 'connect', status: 'pass', message: 'ok' }], primaryFix: '' }} onClose={() => {}} />)
    expect(screen.getByRole('status')).toHaveTextContent('settings.clients.diagnose.allClear')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})

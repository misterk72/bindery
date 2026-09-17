import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import { MemoryRouter } from 'react-router'
import { apiUrl, server } from '../../test/msw'
import type { AdoptionItem, AdoptionListResponse } from '../../api/client'
import AdoptionView from './AdoptionView'

// AdoptionView talks to the server through the real api client, mocked at the
// network with MSW (T1), so a fetch spy sees exactly what the page requests.
// Strings render from en.json, so the assertions read like the page does.
vi.mock('react-i18next', async () => {
  const en = (await import('../../i18n/locales/en.json')).default as Record<string, unknown>
  const lookup = (key: string): unknown =>
    key.split('.').reduce<unknown>((n, p) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[p] : undefined), en)
  const t = (key: string, options?: string | Record<string, unknown>) => {
    const opts = typeof options === 'object' ? options : {}
    let value = lookup(key)
    if (typeof opts.count === 'number') value = lookup(`${key}_${opts.count === 1 ? 'one' : 'other'}`) ?? value
    if (typeof value !== 'string') value = typeof options === 'string' ? options : (opts.defaultValue as string) ?? key
    return (value as string).replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts[k] ?? ''))
  }
  return { useTranslation: () => ({ t, i18n: { language: 'en' } }) }
})

function item(overrides: Partial<AdoptionItem> & Pick<AdoptionItem, 'id'>): AdoptionItem {
  return {
    kind: 'file', format: 'ebook', fileCount: 1, sizeBytes: 2048, relPath: `Andy Weir/Book ${overrides.id}.epub`,
    rootPath: '/books', authorFolder: 'Andy Weir', parsedTitle: `Book ${overrides.id}`, parsedAuthor: 'Andy Weir',
    reason: 'no_title_match', candidates: [], topScore: 0, state: 'pending', bookCreated: false, authorCreated: false,
    members: [`Book ${overrides.id}.epub`], firstSeenAt: '2026-09-17T00:00:00Z',
    ...overrides,
  }
}

const martian = { id: 42, title: 'The Martian', authorId: 7, authorName: 'Andy Weir', status: 'wanted', mediaType: 'ebook', monitored: true }

function listResponse(items: AdoptionItem[], overrides: Partial<AdoptionListResponse> = {}): AdoptionListResponse {
  return {
    items,
    total: items.length,
    facets: { reasons: [{ value: 'no_title_match', count: items.length }], formats: [{ value: 'ebook', count: items.length }], folders: [] },
    summary: { pending: items.length, pendingFiles: items.length, ignored: 0, adopted: 0 },
    scan: { ran: true, ranAt: new Date(Date.now() - 2 * 3600_000).toISOString(), running: false, filesFound: 10, truncated: false, noFilesFound: false },
    ...overrides,
  }
}

const suggested = item({
  id: 1, parsedTitle: 'A Martyrs Tale', relPath: 'Andy Weir/A Martyrs Tale.epub',
  candidates: [{ book: martian, score: 0.82 }], topScore: 0.82,
})
const unsuggested = item({ id: 2, parsedTitle: 'Mystery Notes', relPath: 'Andy Weir/Mystery Notes.epub' })

let fetchSpy: { mock: { calls: Parameters<typeof fetch>[] }; mockRestore: () => void }

function requestedURLs(): string[] {
  return fetchSpy.mock.calls.map(([input]: Parameters<typeof fetch>) => (typeof input === 'string' ? input : input instanceof URL ? input.href : (input as Request).url))
}

function serve(response: AdoptionListResponse) {
  server.use(
    http.get(apiUrl('/library/unmatched'), () => HttpResponse.json(response)),
    http.get(apiUrl('/library/unmatched/summary'), () => HttpResponse.json({ ...response.summary, scan: response.scan })),
  )
}

async function renderView() {
  render(<MemoryRouter><AdoptionView /></MemoryRouter>)
  return screen.findByRole('table', { name: /Books in your library that need a decision/ })
}

beforeEach(() => {
  localStorage.clear()
  fetchSpy = vi.spyOn(window, 'fetch')
})

afterEach(() => {
  fetchSpy.mockRestore()
})

describe('AdoptionView', () => {
  it('confirms a suggestion in one click, then undoes it', async () => {
    serve(listResponse([suggested, unsuggested]))
    let adoptBody: unknown = null
    server.use(
      http.post(apiUrl('/library/unmatched/1/adopt'), async ({ request }) => {
        adoptBody = await request.json()
        return HttpResponse.json({ ...suggested, state: 'adopted', book: martian })
      }),
      http.post(apiUrl('/library/unmatched/1/undo'), () => HttpResponse.json(suggested)),
    )
    const table = await renderView()

    const row = within(table).getByRole('row', { name: 'A Martyrs Tale' })
    expect(within(row).getByText(/Closest match is The Martian by Andy Weir \(82%\)/)).toBeInTheDocument()
    fireEvent.click(within(row).getByRole('button', { name: 'Confirm' }))

    expect(await within(row).findByText(/Adopted as The Martian by Andy Weir\./)).toBeInTheDocument()
    expect(adoptBody).toEqual({ bookId: 42 })

    fireEvent.click(within(row).getByRole('button', { name: 'Undo' }))
    expect(await within(row).findByRole('button', { name: 'Confirm' })).toBeInTheDocument()
  })

  it('reverts an optimistic adopt and shows the error on the row', async () => {
    serve(listResponse([suggested]))
    server.use(
      http.post(apiUrl('/library/unmatched/1/adopt'), () =>
        HttpResponse.json({ error: 'Provenance.epub already belongs to a book in your library.' }, { status: 409 })),
    )
    const table = await renderView()
    const row = within(table).getByRole('row', { name: 'A Martyrs Tale' })
    fireEvent.click(within(row).getByRole('button', { name: 'Confirm' }))

    expect(await within(row).findByRole('alert')).toHaveTextContent('already belongs to a book')
    expect(within(row).getByRole('button', { name: 'Confirm' })).toBeInTheDocument()
  })

  it('asks a metadata provider only when the search is submitted', async () => {
    serve(listResponse([unsuggested]))
    server.use(http.get(apiUrl('/search/book'), () => HttpResponse.json([])))
    const table = await renderView()

    const providerCalls = () => requestedURLs().filter(u => u.includes('/search/book') || u.includes('/book/lookup'))
    fireEvent.click(within(table).getByRole('button', { name: 'Choose book' }))
    await screen.findByRole('region', { name: 'Which book is Mystery Notes?' })
    fireEvent.click(screen.getByRole('button', { name: 'Not in your library? Search metadata' }))
    fireEvent.change(screen.getByRole('textbox', { name: 'Search metadata' }), { target: { value: 'mystery notes weir' } })
    expect(providerCalls()).toEqual([])

    fireEvent.click(screen.getByRole('button', { name: 'Search' }))
    await waitFor(() => expect(providerCalls()).toHaveLength(1))
    expect(providerCalls()[0]).toContain('/search/book?term=mystery%20notes%20weir')
  })

  it('is operable from the keyboard: arrows, Enter, Esc and i', async () => {
    serve(listResponse([suggested, unsuggested]))
    server.use(http.get(apiUrl('/book'), () => HttpResponse.json({ items: [], total: 0 })))
    let ignored = false
    server.use(http.post(apiUrl('/library/unmatched/2/ignore'), () => {
      ignored = true
      return HttpResponse.json({ ...unsuggested, state: 'ignored' })
    }))
    const table = await renderView()
    const [first, second] = within(table).getAllByRole('row').slice(1)

    expect(first).toHaveAttribute('tabindex', '0')
    expect(second).toHaveAttribute('tabindex', '-1')
    first.focus()
    fireEvent.keyDown(first, { key: 'ArrowDown' })
    expect(second).toHaveFocus()
    expect(second).toHaveAttribute('tabindex', '0')

    fireEvent.keyDown(second, { key: 'Enter' })
    const editor = await screen.findByRole('region', { name: 'Which book is Mystery Notes?' })
    expect(within(editor).getByRole('textbox', { name: 'Search your library' })).toHaveFocus()

    fireEvent.keyDown(within(editor).getByRole('textbox', { name: 'Search your library' }), { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('region', { name: /Which book is/ })).toBeNull())
    expect(second).toHaveFocus()

    fireEvent.keyDown(second, { key: 'i' })
    expect(await within(second).findByText(/Ignored\. Later scans keep it out of this list\./)).toBeInTheDocument()
    expect(ignored).toBe(true)
  })

  it('shows the never scanned state with a scan action', async () => {
    serve(listResponse([], { scan: { ran: false, running: false, filesFound: 0, truncated: false, noFilesFound: false } }))
    render(<MemoryRouter><AdoptionView /></MemoryRouter>)
    expect(await screen.findByText('Your library has not been scanned yet')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Scan library' })).toBeInTheDocument()
    expect(screen.getByText('Not scanned yet')).toBeInTheDocument()
  })

  it('shows the all matched state after a scan with nothing left', async () => {
    serve(listResponse([]))
    render(<MemoryRouter><AdoptionView /></MemoryRouter>)
    expect(await screen.findByText('Every book in your library is matched')).toBeInTheDocument()
    expect(screen.getByText('Nothing needs a decision')).toBeInTheDocument()
  })

  it('says when the scan was truncated and when one is running', async () => {
    serve(listResponse([unsuggested], {
      scan: { ran: true, ranAt: new Date().toISOString(), running: true, filesFound: 60000, truncated: true, noFilesFound: false },
    }))
    await renderView()
    expect(screen.getByText('Scanning your library…')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Scanning…' })).toBeDisabled()
    expect(screen.getByText(/larger than one scan lists/)).toBeInTheDocument()
  })

  it('lists ignored books with Unignore and an empty ignored state', async () => {
    serve(listResponse([]))
    render(<MemoryRouter><AdoptionView /></MemoryRouter>)
    await screen.findByText('Every book in your library is matched')

    const ignoredItem = item({ id: 3, parsedTitle: 'Old Manual', state: 'ignored' })
    server.use(http.get(apiUrl('/library/unmatched'), ({ request }) =>
      HttpResponse.json(new URL(request.url).searchParams.get('state') === 'ignored' ? listResponse([ignoredItem]) : listResponse([]))))
    fireEvent.click(screen.getByRole('radio', { name: /Ignored/ }))
    const table = await screen.findByRole('table', { name: /Ignored books in your library/ })
    expect(within(table).getByRole('button', { name: 'Unignore' })).toBeInTheDocument()
  })

  it('offers Add author for a folder whose author is not in the library', async () => {
    serve(listResponse([unsuggested], {
      facets: { reasons: [], formats: [], folders: [{ folder: 'Becky Chambers', units: 4, files: 40, notInLibrary: 4, author: 'Becky Chambers' }] },
    }))
    await renderView()
    const rail = screen.getByRole('navigation', { name: 'Folders with the most books to decide' })
    expect(within(rail).getByText('Becky Chambers is not in your library')).toBeInTheDocument()
    expect(within(rail).getByRole('button', { name: 'Add author' })).toBeInTheDocument()
  })
})

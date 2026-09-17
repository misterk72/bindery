import { render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { MemoryRouter } from 'react-router'
import ImportHints from './ImportHints'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({
    t: (key: string) => ({ 'importHints.manualImport': 'Import from a folder', 'importHints.scanLibrary': 'Review my library' }[key] ?? key),
  }),
}))

// The empty Queue and Wanted pages point new users at the Import page, which
// now holds both ways in: a folder elsewhere, and the library folder itself.
describe('ImportHints', () => {
  it('links to the Import page views, not to Settings tabs', () => {
    render(<MemoryRouter><ImportHints /></MemoryRouter>)
    expect(screen.getByRole('link', { name: 'Import from a folder' })).toHaveAttribute('href', '/import?view=folder')
    expect(screen.getByRole('link', { name: 'Review my library' })).toHaveAttribute('href', '/import')
  })
})

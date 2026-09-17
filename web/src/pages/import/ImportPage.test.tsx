import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { MemoryRouter, useLocation } from 'react-router'
import ImportPage from './ImportPage'

vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string, fallback?: unknown) => (typeof fallback === 'string' ? fallback : key) }),
}))
vi.mock('../../auth/AuthContext', () => ({ useAuth: () => ({ isAdmin: true }) }))
vi.mock('./AdoptionView', () => ({ default: () => <div data-testid="adoption-view" /> }))
vi.mock('./FolderImportView', () => ({ default: () => <div data-testid="folder-view" /> }))

function Location() {
  const loc = useLocation()
  return <output data-testid="location">{loc.pathname + loc.search}</output>
}

function renderAt(url: string) {
  render(
    <MemoryRouter initialEntries={[url]}>
      <ImportPage />
      <Location />
    </MemoryRouter>,
  )
}

describe('ImportPage', () => {
  it('opens on "In your library" by default', () => {
    renderAt('/import')
    expect(screen.getByRole('tab', { name: 'In your library' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('adoption-view')).toBeInTheDocument()
  })

  it('addresses the folder view as ?view=folder, which ImportHints links to', () => {
    renderAt('/import?view=folder')
    expect(screen.getByRole('tab', { name: 'From a folder' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('tabpanel')).toContainElement(screen.getByTestId('folder-view'))
  })

  it('switches views by click and by arrow key', () => {
    renderAt('/import')
    fireEvent.click(screen.getByRole('tab', { name: 'From a folder' }))
    expect(screen.getByTestId('location')).toHaveTextContent('/import?view=folder')

    const folderTab = screen.getByRole('tab', { name: 'From a folder' })
    folderTab.focus()
    fireEvent.keyDown(folderTab, { key: 'ArrowRight' })
    expect(screen.getByTestId('location')).toHaveTextContent(/^\/import$/)
    expect(screen.getByRole('tab', { name: 'In your library' })).toHaveFocus()
  })
})

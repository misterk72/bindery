import type { Book, ScanItem } from '../../api/client'

// Shared by FolderImportView and FolderImportRow.

// RowState is the per-unit resolution the user is building, keyed by unit path.
export interface RowState {
  // chosen is the catalogue book the unit will import against (a confident
  // match, a picked candidate, or a searched existing book). null until resolved.
  chosen: Book | null
  // format override: '' = auto-detect, or 'ebook' / 'audiobook'. Defaults to the
  // scan's detected format.
  format: string
  // selected marks the unit for the next bulk import. Only meaningful when chosen.
  selected: boolean
}

export function groupHeading(match: ScanItem['match']): string {
  switch (match) {
    case 'confident': return 'Matched'
    case 'ambiguous': return 'Needs a choice'
    default: return 'Unmatched'
  }
}

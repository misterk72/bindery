import type {
  AdoptionBookRef, AdoptionFacets, AdoptionItem, AdoptionListResponse, AdoptionScanStatus, AdoptionSummary,
} from '../../api/client'

// Optimistic state for the adoption list, as named actions. A decision shows
// at once as a quiet line on its row; the server's answer then confirms it or
// reverts it with the error inline. The line stays until the next fetch, so a
// person can read what happened and undo it without the row jumping away.

export type Outcome =
  // Adopt sent; preview is the book the person picked, shown before the answer.
  | { kind: 'adopting'; preview: AdoptionBookRef | null }
  | { kind: 'adopted'; item: AdoptionItem }
  | { kind: 'ignoring' }
  | { kind: 'ignored' }
  | { kind: 'undoing' }
  // Undone or unignored while viewing the adopted or ignored list: the row
  // has gone back to needing a decision.
  | { kind: 'restored' }

export interface AdoptionListState {
  items: AdoptionItem[]
  total: number
  facets: AdoptionFacets | null
  summary: AdoptionSummary | null
  scan: AdoptionScanStatus | null
  loading: boolean
  loaded: boolean
  loadError: string
  outcomes: Record<number, Outcome>
  errors: Record<number, string>
  expandedId: number | null
}

export const initialAdoptionState: AdoptionListState = {
  items: [], total: 0, facets: null, summary: null, scan: null,
  loading: true, loaded: false, loadError: '', outcomes: {}, errors: {}, expandedId: null,
}

export type AdoptionAction =
  | { type: 'loadStarted' }
  | { type: 'loaded'; response: AdoptionListResponse }
  | { type: 'loadFailed'; error: string }
  | { type: 'expanded'; id: number }
  | { type: 'collapsed' }
  | { type: 'adoptRequested'; id: number; preview: AdoptionBookRef | null }
  | { type: 'adoptSucceeded'; id: number; item: AdoptionItem }
  | { type: 'ignoreRequested'; id: number }
  | { type: 'ignoreSucceeded'; id: number }
  | { type: 'undoRequested'; id: number }
  | { type: 'undoSucceeded'; id: number; item: AdoptionItem; restored: boolean }
  | { type: 'requestFailed'; id: number; error: string; revertTo: Outcome | null }
  | { type: 'scanStatus'; summary: AdoptionSummary; scan: AdoptionScanStatus }

function withOutcome(state: AdoptionListState, id: number, outcome: Outcome | null): AdoptionListState {
  const outcomes = { ...state.outcomes }
  if (outcome) outcomes[id] = outcome
  else delete outcomes[id]
  const errors = { ...state.errors }
  delete errors[id]
  return { ...state, outcomes, errors }
}

export function adoptionReducer(state: AdoptionListState, action: AdoptionAction): AdoptionListState {
  switch (action.type) {
    case 'loadStarted':
      return { ...state, loading: true, loadError: '' }
    case 'loaded': {
      const { response } = action
      return {
        ...state,
        loading: false,
        loaded: true,
        items: response.items,
        total: response.total,
        // Facets come only with a filter change; a page turn keeps the last.
        facets: response.facets ?? state.facets,
        summary: response.summary,
        scan: response.scan,
        outcomes: {},
        errors: {},
        expandedId: response.items.some(i => i.id === state.expandedId) ? state.expandedId : null,
      }
    }
    case 'loadFailed':
      return { ...state, loading: false, loaded: true, loadError: action.error }
    case 'expanded':
      return { ...state, expandedId: action.id }
    case 'collapsed':
      return { ...state, expandedId: null }
    case 'adoptRequested':
      return { ...withOutcome(state, action.id, { kind: 'adopting', preview: action.preview }), expandedId: null }
    case 'adoptSucceeded':
      return withOutcome(state, action.id, { kind: 'adopted', item: action.item })
    case 'ignoreRequested':
      return { ...withOutcome(state, action.id, { kind: 'ignoring' }), expandedId: null }
    case 'ignoreSucceeded':
      return withOutcome(state, action.id, { kind: 'ignored' })
    case 'undoRequested':
      return withOutcome(state, action.id, { kind: 'undoing' })
    case 'undoSucceeded': {
      const next = withOutcome(state, action.id, action.restored ? { kind: 'restored' } : null)
      return { ...next, items: state.items.map(i => (i.id === action.id ? action.item : i)) }
    }
    case 'requestFailed': {
      const next = withOutcome(state, action.id, action.revertTo)
      return { ...next, errors: { ...next.errors, [action.id]: action.error } }
    }
    case 'scanStatus':
      return { ...state, summary: action.summary, scan: action.scan }
  }
}

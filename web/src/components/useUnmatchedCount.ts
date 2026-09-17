import { useEffect, useState } from 'react'
import { api } from '../api/client'

// The count behind the Import nav badge: books the library scan could not
// match that still need a decision. Read once per app load for an admin, then
// updated from the event the adoption view fires after each adopt, ignore or
// scan (P5). Nothing polls.

const EVENT = 'bindery:unmatched-changed'

export function notifyUnmatchedChanged(pending: number) {
  window.dispatchEvent(new CustomEvent<number>(EVENT, { detail: pending }))
}

export function useUnmatchedCount(enabled: boolean): number {
  const [count, setCount] = useState(0)

  useEffect(() => {
    if (!enabled) return
    let cancelled = false
    api.unmatchedSummary()
      .then(s => { if (!cancelled) setCount(s.pending) })
      .catch(() => { /* the badge is a convenience; no count shows nothing */ })
    const onChange = (e: Event) => setCount((e as CustomEvent<number>).detail)
    window.addEventListener(EVENT, onChange)
    return () => {
      cancelled = true
      window.removeEventListener(EVENT, onChange)
    }
  }, [enabled])

  return enabled ? count : 0
}

import { describe, expect, it } from 'vitest'
import type { TFunction } from 'i18next'
import en from '../../i18n/locales/en.json'
import type { AdoptionItem } from '../../api/client'
import { adoptionHint, scorePercent } from './adoptionHint'

const lookup = (key: string): unknown =>
  key.split('.').reduce<unknown>((n, p) => (n && typeof n === 'object' ? (n as Record<string, unknown>)[p] : undefined), en)

const t = ((key: string, options?: string | Record<string, unknown>) => {
  const opts = typeof options === 'object' ? options : {}
  const value = lookup(key)
  return (typeof value === 'string' ? value : key).replace(/\{\{(\w+)\}\}/g, (_, k) => String(opts[k] ?? ''))
}) as unknown as TFunction

function item(overrides: Partial<AdoptionItem>): AdoptionItem {
  return {
    id: 1, kind: 'file', format: 'ebook', fileCount: 1, sizeBytes: 1, relPath: 'A/B.epub', rootPath: '/books',
    authorFolder: 'A', parsedTitle: 'B', parsedAuthor: 'Becky Chambers', reason: 'no_title_match', candidates: [],
    topScore: 0, state: 'pending', bookCreated: false, authorCreated: false, members: [], firstSeenAt: '',
    ...overrides,
  }
}

// The row's sentence is written from facts and ends in an action; the raw
// reason code only ever appears in the tooltip (#2033).
describe('adoptionHint', () => {
  const codes = ['author_not_in_library', 'no_candidate_books', 'no_title_match', 'no_title_parsed'] as const

  it.each(codes)('never puts the %s code in the sentence, and ends in an action', code => {
    const hint = adoptionHint(item({ reason: code }), t)
    expect(hint.sentence).not.toContain(code)
    expect(hint.sentence).toMatch(/(Add the author, then scan again|Choose the book[^.]*|search metadata)\.$/)
    expect(hint.tooltip).toContain(code)
  })

  it('names the author the scan read when the author is missing', () => {
    expect(adoptionHint(item({ reason: 'author_not_in_library' }), t).sentence)
      .toBe('Becky Chambers is not in your library yet. Add the author, then scan again.')
  })

  it('leads with the suggestion when there is one', () => {
    const book = { id: 9, title: 'The Martian', authorId: 1, authorName: 'Andy Weir', status: 'wanted', mediaType: 'ebook', monitored: true }
    expect(adoptionHint(item({ candidates: [{ book, score: 0.816 }] }), t).sentence)
      .toBe('Closest match is The Martian by Andy Weir (82%). Confirm it or choose another book.')
  })

  it('clamps scores to a percentage', () => {
    expect([scorePercent(-1), scorePercent(0.6), scorePercent(1.4)]).toEqual([0, 60, 100])
  })
})

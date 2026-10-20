import React from 'react'
import { Entry } from '../types/chat'

export function countSearchMatches(entries: Entry[], query: string): number {
  if (!query) return 0
  const q = query.toLowerCase()
  let count = 0
  for (const e of entries) {
    const text = e.kind === 'tool' ? (e.data.title || e.data.kind || '') : ('text' in e ? e.text : '')
    if (!text) continue
    let idx = 0
    const lower = text.toLowerCase()
    while ((idx = lower.indexOf(q, idx)) !== -1) { count++; idx += q.length }
  }
  return count
}

export function HighlightText({ text, query }: { text: string; query?: string }) {
  if (!query || !text) return <>{text}</>
  const lower = text.toLowerCase()
  const q = query.toLowerCase()
  const parts: React.ReactNode[] = []
  let last = 0
  let idx = lower.indexOf(q, 0)
  let key = 0
  while (idx !== -1) {
    if (idx > last) parts.push(text.slice(last, idx))
    parts.push(
      <mark key={key++} className="bg-accent-yellow/40 theme-text-primary rounded-sm px-px" data-search-match>
        {text.slice(idx, idx + query.length)}
      </mark>
    )
    last = idx + query.length
    idx = lower.indexOf(q, last)
  }
  if (last < text.length) parts.push(text.slice(last))
  return <>{parts}</>
}

export function SearchBar({
  query,
  onQueryChange,
  matchCount,
  matchIdx,
  onNext,
  onPrev,
  onClose,
  inputRef,
}: {
  query: string
  onQueryChange: (q: string) => void
  matchCount: number
  matchIdx: number
  onNext: () => void
  onPrev: () => void
  onClose: () => void
  inputRef: React.RefObject<HTMLInputElement | null>
}) {
  return (
    <div
      className="absolute top-1 right-3 z-20 flex items-center gap-1 rounded-md px-2 py-1"
      style={{
        background: 'var(--theme-panel-bg)',
        border: '1px solid var(--theme-panel-divider)',
        boxShadow: 'var(--theme-shadow-panel)',
      }}
    >
      <input
        ref={inputRef}
        type="text"
        value={query}
        onChange={(e) => onQueryChange(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            if (e.shiftKey) onPrev(); else onNext()
            // Scroll to the current match
            const marks = document.querySelectorAll('[data-search-match]')
            const target = marks[e.shiftKey ? ((matchIdx - 1 + marks.length) % marks.length) : ((matchIdx + 1) % marks.length)]
            target?.scrollIntoView({ block: 'center', behavior: 'smooth' })
          }
          if (e.key === 'Escape') { e.stopPropagation(); onClose() }
        }}
        placeholder="Search…"
        className="bg-transparent text-l theme-font-mono theme-text-primary theme-placeholder-faint outline-none w-40"
      />
      {query && (
        <span className="text-m theme-text-faint theme-font-mono shrink-0">
          {matchCount > 0 ? `${matchIdx + 1}/${matchCount}` : '0/0'}
        </span>
      )}
      <button onClick={() => { onPrev(); scrollToMatch(matchIdx - 1, matchCount) }} className="theme-text-muted theme-hover-text-primary text-l px-0.5" title="Previous (Shift+Enter)">▲</button>
      <button onClick={() => { onNext(); scrollToMatch(matchIdx + 1, matchCount) }} className="theme-text-muted theme-hover-text-primary text-l px-0.5" title="Next (Enter)">▼</button>
      <button onClick={onClose} className="theme-text-muted theme-hover-text-primary text-l px-1" title="Close (Esc)">✕</button>
    </div>
  )
}

export function scrollToMatch(idx: number, total: number) {
  if (total <= 0) return
  const wrapped = ((idx % total) + total) % total
  setTimeout(() => {
    const marks = document.querySelectorAll('[data-search-match]')
    marks[wrapped]?.scrollIntoView({ block: 'center', behavior: 'smooth' })
  }, 0)
}

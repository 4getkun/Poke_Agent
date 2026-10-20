import React, { useEffect, useRef, useState } from 'react'
import { useSpriteAnimation } from './spriteAnimations'
import { PixelSprite } from './PixelSprite'
import { tinySpriteScaleFor, useSpriteNaturalSize } from '../utils/spriteSizing'

// Floating sprite in the bottom-right of the chat panel transcript area.
// Floating large sprite with the same idle/busy/done animations.
export function ChatPanelSprite({ sprite, state }: { sprite?: string; state?: string }) {
  const animClass = useSpriteAnimation(state || 'idle', true)
  const naturalSize = useSpriteNaturalSize(sprite)
  if (!sprite) return null
  const scale = tinySpriteScaleFor(naturalSize) * 2
  return (
    <div
      className={`absolute bottom-0 right-0 pointer-events-none ${animClass}`}
      style={{ width: 96, height: 96, display: 'flex', alignItems: 'center', justifyContent: 'center', overflow: 'visible' }}
    >
      <PixelSprite sprite={sprite} scale={scale} alt="" shadow="panel" />
    </div>
  )
}

// ── Header dropdown ─────────────────────────────────────────

export function ChatPanelDropdown({
  onSearch,
  onMenu,
  onCancel,
  onClose,
  searchOpen,
  onDebug,
  showTimestamps,
  onToggleTimestamps,
}: {
  onSearch: () => void
  onMenu: (e: React.MouseEvent) => void
  onCancel?: () => void
  onClose: () => void
  searchOpen: boolean
  onDebug?: () => void
  showTimestamps?: boolean | 'debug'
  onToggleTimestamps?: () => void
}) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  return (
    <div ref={ref} className="relative shrink-0">
      <button
        onClick={() => setOpen(o => !o)}
        className="text-xl theme-text-muted theme-hover-text-primary px-1 py-0.5 leading-none"
        title="Actions"
      >⋯</button>
      {open && (
        <div
          className="absolute right-0 top-full mt-1 z-30 overflow-hidden min-w-[140px] gba-dropdown-panel"
        >
          <button
            onClick={() => { onSearch(); setOpen(false) }}
            className="w-full text-left px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow theme-text-secondary theme-bg-dropdown-hover flex items-center gap-2"
          >
            <span className="theme-text-faint">⌘F</span>
            <span>{searchOpen ? 'CLOSE SEARCH' : 'SEARCH'}</span>
          </button>
          {onCancel && (
            <button
              onClick={() => { onCancel(); setOpen(false) }}
              className="w-full text-left px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow text-accent-red/80 theme-bg-dropdown-hover flex items-center gap-2"
            >
              <span className="theme-text-faint">⌃C</span>
              <span>CANCEL</span>
            </button>
          )}
          <button
            onClick={(e) => { onMenu(e); setOpen(false) }}
            className="w-full text-left px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow theme-text-secondary theme-bg-dropdown-hover"
          >AGENT MENU…</button>
          <div className="border-t theme-border-subtle" />
          <button
            onClick={() => { onClose(); setOpen(false) }}
            className="w-full text-left px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow theme-text-muted theme-bg-dropdown-hover flex items-center gap-2"
          >
            <span className="theme-text-faint">Esc</span>
            <span>CLOSE PANEL</span>
          </button>
          {onToggleTimestamps && (
            <button
              onClick={() => { onToggleTimestamps(); setOpen(false) }}
              className="w-full text-left px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow theme-text-secondary theme-bg-dropdown-hover flex items-center gap-2"
            >
              <span className={`text-m ${showTimestamps ? 'text-accent-green' : 'theme-text-faint'}`}>{showTimestamps ? '●' : '○'}</span>
              <span>TIMESTAMPS</span>
            </button>
          )}
          {onDebug && (
            <>
              <div className="border-t theme-border-subtle" />
              <button
                onClick={() => { onDebug(); setOpen(false) }}
                className="w-full text-left px-3 py-1.5 text-s theme-font-display uppercase pixel-shadow theme-text-warning theme-bg-dropdown-hover"
              >DEBUG PANEL</button>
            </>
          )}
        </div>
      )}
    </div>
  )
}

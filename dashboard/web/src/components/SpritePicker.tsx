import { useState, useEffect, useRef } from 'react'
import { POKEMON_SPRITES } from './sprites'
import { PixelSprite } from './PixelSprite'

interface SpritePickerProps {
  currentSprite: string
  onSelect: (sprite: string) => void
  onClose: () => void
}

export function SpritePicker({ currentSprite, onSelect, onClose }: SpritePickerProps) {
  const [search, setSearch] = useState('')
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  const filtered = search
    ? POKEMON_SPRITES.filter(s => s.includes(search.toLowerCase()))
    : [...POKEMON_SPRITES]

  return (
    <div
      className="fixed inset-0 theme-modal-scrim z-50 flex items-center justify-center"
      onClick={onClose}
    >
      <div
        className="gba-panel p-4 w-[360px] max-h-[420px] flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        <div className="gba-dialog rounded-lg mb-3">
          <input
            ref={inputRef}
            type="text"
            value={search}
            onChange={e => setSearch(e.target.value)}
            placeholder="Search POKeMON..."
            className="w-full bg-transparent px-3 py-2 text-m theme-font-mono text-gba-dialog-border placeholder:text-gba-dialog-border/30 outline-none"
          />
        </div>
        <div className="flex-1 overflow-auto grid grid-cols-6 gap-1">
          {filtered.map(sprite => (
            <button
              key={sprite}
              onClick={() => { onSelect(sprite); onClose() }}
              className={`p-1 rounded-lg theme-bg-panel-hover flex items-center justify-center transition-colors ${
                sprite === currentSprite ? 'theme-bg-panel-subtle ring-2 ring-accent-yellow' : ''
              }`}
              title={sprite}
            >
              <div className="w-8 h-8 flex items-center justify-center overflow-visible">
                <PixelSprite sprite={sprite} alt={sprite} />
              </div>
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

import { ReactNode, useEffect } from 'react'

const FRAME_OUTER = 'var(--theme-modal-frame-outer)'
const FRAME_SHINE = 'var(--theme-modal-frame-shine)'

export interface GameModalProps {
  title: string
  onClose: () => void
  children: ReactNode
  width?: string | number
  height?: string | number
  maxWidth?: string | number
  maxHeight?: string | number
  zIndex?: number
  scanlines?: boolean
}

/**
 * Shared GBA modal shell used by PC Box, Settings, and future centered panels.
 * Owns the dark scrim, escape/outside-click close behavior, outer frame, title bar,
 * and close button so modal surfaces behave consistently.
 */
export function GameModal({ title, onClose, children, width = 'min(820px, 96vw)', height = 'min(520px, 92vh)', maxWidth = '96vw', maxHeight = '92vh', zIndex = 50, scanlines = true }: GameModalProps) {
  useEffect(() => {
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [onClose])

  return (
    <div
      className="fixed inset-0 flex items-center justify-center"
      style={{ background: 'var(--theme-modal-scrim)', zIndex }}
      onClick={(e) => { if (e.target === e.currentTarget) onClose() }}
    >
      <div
        className={scanlines ? 'gba-modal-scanlines' : undefined}
        style={{
          width,
          height,
          maxWidth,
          maxHeight,
          background: FRAME_OUTER,
          borderRadius: 10,
          padding: 3,
          boxShadow: `0 0 0 2px ${FRAME_SHINE}, 0 0 0 4px ${FRAME_OUTER}, var(--theme-shadow-strong)`,
          userSelect: 'none',
          position: 'relative',
          display: 'flex',
          flexDirection: 'column',
        }}
      >
        <button
          onClick={onClose}
          style={{
            position: 'absolute', top: -14, right: -14, width: 28, height: 28, borderRadius: '50%',
            background: 'linear-gradient(180deg, var(--theme-accent-red) 0%, var(--theme-accent-red) 100%)',
            border: `2px solid ${FRAME_SHINE}`,
            boxShadow: 'var(--theme-shadow-panel)',
            color: 'var(--theme-text-primary)', fontFamily: 'var(--theme-font-display)', fontSize: 'var(--theme-type-s)', cursor: 'pointer',
            display: 'flex', alignItems: 'center', justifyContent: 'center', zIndex: 10,
            textShadow: 'var(--theme-text-shadow-pixel)',
          }}
        >✕</button>

        <div style={{
          background: 'var(--theme-modal-title-bg)',
          borderRadius: '7px 7px 0 0', padding: '8px 16px', marginBottom: 3,
          display: 'flex', alignItems: 'center', justifyContent: 'center',
          flexShrink: 0,
        }}>
          <span style={{ fontFamily: 'var(--theme-font-display)', fontSize: 'var(--theme-type-m)', lineHeight: 1.5, color: 'var(--theme-text-primary)', textShadow: 'var(--theme-modal-title-text-shadow)', letterSpacing: 1 }}>
            {title}
          </span>
        </div>

        {children}
      </div>
    </div>
  )
}

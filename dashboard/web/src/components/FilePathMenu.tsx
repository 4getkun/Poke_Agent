import { ReactNode, useEffect, useState } from 'react'
import { createPortal } from 'react-dom'
import { openFileInConfiguredEditor } from '../utils/openExternal'

export function openFileInEditor(path: string) {
  void openFileInConfiguredEditor(path)
}

export function FilePathMenu({ path, x, y, onClose }: {
  path: string
  x: number
  y: number
  onClose: () => void
}) {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  return createPortal(
    <>
      <div
        className="fixed inset-0"
        style={{ zIndex: 9998 }}
        onClick={onClose}
        onContextMenu={(e) => { e.preventDefault(); onClose() }}
      />
      <div
        className="fixed gba-dropdown-panel py-1 min-w-[180px]"
        style={{ left: Math.min(x, window.innerWidth - 200), top: Math.min(y, window.innerHeight - 120), zIndex: 9999 }}
      >
        <button
          className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover flex items-center gap-2 transition-colors pixel-shadow"
          onClick={(e) => { e.stopPropagation(); openFileInEditor(path); onClose() }}
        >
          <span className="w-4 text-center">↗</span>
          Open in editor
        </button>
        <button
          className="w-full text-left px-3 py-1.5 text-s theme-font-display theme-text-primary theme-bg-dropdown-hover flex items-center gap-2 transition-colors pixel-shadow"
          onClick={async (e) => { e.stopPropagation(); await navigator.clipboard.writeText(path); onClose() }}
        >
          <span className="w-4 text-center">⧉</span>
          Copy file path
        </button>
      </div>
    </>,
    document.body,
  )
}

export function FilePathLink({ path, children, className, title, hitAreaClassName }: {
  path: string
  children: ReactNode
  className?: string
  title?: string
  hitAreaClassName?: string
}) {
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null)
  return (
    <>
      <button
        type="button"
        className={hitAreaClassName || 'inline min-w-0 text-left'}
        title={title || path}
        onClick={(e) => {
          e.preventDefault()
          e.stopPropagation()
          setMenu({ x: e.clientX, y: e.clientY })
        }}
      >
        <span className={className}>{children}</span>
      </button>
      {menu && <FilePathMenu path={path} x={menu.x} y={menu.y} onClose={() => setMenu(null)} />}
    </>
  )
}

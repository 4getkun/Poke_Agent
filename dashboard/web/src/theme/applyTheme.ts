import { ThemeDefinition } from './themeTypes'

function tokenToVarName(prefix: string, token: string) {
  return `--theme-${prefix}${token.replace(/\./g, '-').replace(/[A-Z]/g, m => `-${m.toLowerCase()}`)}`
}

function hexToRgbParts(value: string): string | null {
  const hex = value.trim()
  const m = hex.match(/^#([0-9a-f]{3}|[0-9a-f]{6})$/i)
  if (!m) return null
  const raw = m[1].length === 3 ? m[1].split('').map(c => c + c).join('') : m[1]
  const n = Number.parseInt(raw, 16)
  return `${(n >> 16) & 255} ${(n >> 8) & 255} ${n & 255}`
}

function setToken(root: HTMLElement, name: string, value: string | number) {
  const cssValue = String(value)
  root.style.setProperty(name, cssValue)
  const rgb = hexToRgbParts(cssValue)
  if (rgb) root.style.setProperty(`${name}-rgb`, rgb)
}

export function applyTheme(theme: ThemeDefinition, themeId: string) {
  const root = document.documentElement
  root.dataset.theme = themeId

  setToken(root, '--theme-font-display', theme.fonts.display)
  setToken(root, '--theme-font-body', theme.fonts.body)
  setToken(root, '--theme-font-mono', theme.fonts.mono)

  for (const [token, value] of Object.entries(theme.colors)) setToken(root, tokenToVarName('', token), value)
  for (const [token, value] of Object.entries(theme.effects)) setToken(root, tokenToVarName('', token), value)
  for (const [token, value] of Object.entries(theme.radii || {})) setToken(root, tokenToVarName('radius-', token), value)

  document.body.classList.toggle('theme-classic', themeId === 'classic')
  document.body.classList.toggle('theme-fire-red', themeId === 'fire-red')
}

import themesJson from './themes.json'
import { ThemeDefinition, ThemeRegistry } from './themeTypes'

const REQUIRED_COLOR_TOKENS = [
  'app.bg',
  'app.text',
  'panel.bg',
  'panel.border',
  'panel.text',
  'card.bg',
  'chat.bg',
  'chat.message.user.bg',
  'chat.message.assistant.bg',
  'accent.blue',
  'accent.yellow',
  'accent.green',
  'accent.red',
]

const REQUIRED_EFFECT_TOKENS = ['modal.scrim', 'shadow.strong', 'textShadow.pixel', 'motion.scale']

export function validateTheme(id: string, theme: ThemeDefinition): string[] {
  const errors: string[] = []
  if (!theme || typeof theme !== 'object') return [`${id}: theme must be an object`]
  for (const font of ['display', 'body', 'mono'] as const) {
    if (typeof theme.fonts?.[font] !== 'string' || !theme.fonts[font]) errors.push(`${id}: missing fonts.${font}`)
  }
  for (const token of REQUIRED_COLOR_TOKENS) {
    if (typeof theme.colors?.[token] !== 'string' || !theme.colors[token]) errors.push(`${id}: missing colors.${token}`)
  }
  for (const token of REQUIRED_EFFECT_TOKENS) {
    const value = theme.effects?.[token]
    if (typeof value !== 'string' && typeof value !== 'number') errors.push(`${id}: missing effects.${token}`)
  }
  return errors
}

function validateRegistry(registry: ThemeRegistry): ThemeRegistry {
  const errors: string[] = []
  if (registry.version !== 1) errors.push('themes.json: version must be 1')
  if (!registry.themes || typeof registry.themes !== 'object') errors.push('themes.json: themes must be an object')
  for (const [id, theme] of Object.entries(registry.themes || {})) errors.push(...validateTheme(id, theme))
  if (errors.length) throw new Error(`Invalid theme registry:\n${errors.join('\n')}`)
  return registry
}

export const themeRegistry = validateRegistry(themesJson as ThemeRegistry)

export function getTheme(themeId: string): ThemeDefinition {
  return themeRegistry.themes[themeId] || themeRegistry.themes['fire-red']
}

export function listThemes() {
  return Object.entries(themeRegistry.themes).map(([id, theme]) => ({ id, label: theme.label }))
}

export interface ThemeDefinition {
  label: string
  fonts: Record<'display' | 'body' | 'mono', string>
  colors: Record<string, string>
  radii?: Record<string, string>
  effects: Record<string, string | number>
}

export interface ThemeRegistry {
  version: number
  themes: Record<string, ThemeDefinition>
}

export type ThemeId = string

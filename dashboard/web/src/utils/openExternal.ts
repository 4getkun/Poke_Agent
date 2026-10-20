const STORAGE_KEY = 'pokegents-settings'

type OpenKind = 'file' | 'url'

function readSettings(): { editorOpenCommand?: string; browserOpenCommand?: string } {
  try {
    const raw = localStorage.getItem(STORAGE_KEY)
    return raw ? JSON.parse(raw) : {}
  } catch {
    return {}
  }
}

async function postOpenExternal(kind: OpenKind, target: string, command: string) {
  const res = await fetch('/api/open-external', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ kind, target, command }),
  })
  if (!res.ok) throw new Error(await res.text())
}

export async function openFileInConfiguredEditor(path: string, command?: string) {
  if (!path) return
  const cmd = command || readSettings().editorOpenCommand || 'code {path}'
  try {
    await postOpenExternal('file', path, cmd)
  } catch {
    window.open(`file://${path.startsWith('/') ? path : `/${path}`}`, '_blank')
  }
}

export async function openUrlInConfiguredBrowser(url: string, command?: string) {
  if (!url) return
  const cmd = command || readSettings().browserOpenCommand || 'open -a "Google Chrome" {url}'
  try {
    await postOpenExternal('url', url, cmd)
  } catch {
    window.open(url, '_blank', 'noopener,noreferrer')
  }
}

import { useEffect, useState } from 'react'
import { fetchRuntimes, RuntimeCapabilities } from '../api'

// Default capabilities used before /api/runtimes returns. Mirrors what
// ITerm2Runtime advertises today; harmless overestimate (we'd just show a
// menu item that turns into a no-op for ~50ms until the real caps load).
const DEFAULT_ITERM2: RuntimeCapabilities = {
  can_focus: true,
  can_clone: true,
  can_cancel: false,
  has_streaming_ui: false,
  has_permission_ui: false,
}
const DEFAULT_CHAT: RuntimeCapabilities = {
  can_focus: false,
  can_clone: true,
  can_cancel: true,
  has_streaming_ui: true,
  has_permission_ui: true,
}

let cached: Record<string, RuntimeCapabilities> | null = null
let inflight: Promise<Record<string, RuntimeCapabilities>> | null = null

// useRuntimeCapabilities loads the runtime registry once per app boot and
// reuses it for every consumer. The map key is the `agent.interface` value
// ("iterm2" or "chat"); to look up caps for an agent, do
// `caps[agent.interface ?? "iterm2"]`.
export function useRuntimeCapabilities(): Record<string, RuntimeCapabilities> {
  const [caps, setCaps] = useState<Record<string, RuntimeCapabilities>>(
    () => cached ?? { iterm2: DEFAULT_ITERM2, chat: DEFAULT_CHAT },
  )
  useEffect(() => {
    if (cached) return
    if (!inflight) {
      inflight = fetchRuntimes().then((r) => {
        cached = r && Object.keys(r).length > 0 ? r : { iterm2: DEFAULT_ITERM2, chat: DEFAULT_CHAT }
        return cached
      })
    }
    inflight.then((r) => setCaps(r))
  }, [])
  return caps
}

// capsFor resolves an agent's runtime capabilities from a caps map. Takes the
// `interface` field as input (empty string treated as "iterm2", matching the
// backend's default).
export function capsFor(
  caps: Record<string, RuntimeCapabilities>,
  iface: string | undefined,
): RuntimeCapabilities {
  return caps[iface || 'iterm2'] ?? DEFAULT_ITERM2
}

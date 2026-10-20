import { useEffect, useRef, useState, useCallback } from 'react'
import { AgentState, AgentMessage } from '../types'


function activitySignature(items?: { time: string; type: string; text: string }[]): string {
  return (items || []).map(item => `${item.time}|${item.type}|${item.text}`).join('\n')
}

function stringListSignature(items?: string[]): string {
  return (items || []).join('\n')
}

function agentsEqual(a: AgentState[], b: AgentState[]): boolean {
  if (a.length !== b.length) return false
  for (let i = 0; i < a.length; i++) {
    if (a[i].session_id !== b[i].session_id ||
        a[i].state !== b[i].state ||
        a[i].detail !== b[i].detail ||
        a[i].last_trace !== b[i].last_trace ||
        a[i].last_summary !== b[i].last_summary ||
        a[i].card_preview?.phase !== b[i].card_preview?.phase ||
        a[i].card_preview?.text !== b[i].card_preview?.text ||
        activitySignature(a[i].card_preview?.feed) !== activitySignature(b[i].card_preview?.feed) ||
        a[i].user_prompt !== b[i].user_prompt ||
        a[i].display_name !== b[i].display_name ||
        a[i].sprite !== b[i].sprite ||
        a[i].task_group !== b[i].task_group ||
        a[i].role !== b[i].role ||
        a[i].project !== b[i].project ||
        a[i].busy_since !== b[i].busy_since ||
        a[i].context_tokens !== b[i].context_tokens ||
        a[i].context_window !== b[i].context_window ||
        stringListSignature(a[i].recent_actions) !== stringListSignature(b[i].recent_actions) ||
        activitySignature(a[i].activity_feed) !== activitySignature(b[i].activity_feed)) {
      return false
    }
  }
  return true
}

export function useSSE() {
  const [agents, setAgents] = useState<AgentState[]>([])
  const [newMessage, setNewMessage] = useState<AgentMessage | null>(null)
  const [connected, setConnected] = useState(false)
  const esRef = useRef<EventSource | null>(null)
  const reconnectTimeout = useRef<ReturnType<typeof setTimeout> | null>(null)

  // Track when we last heard ANYTHING from the server (event or ping). If
  // 30s elapses with nothing — half-dead socket, OS sleep/wake, proxy timeout
  // — force a reconnect. EventSource's own `onerror` only fires for hard
  // network failures and misses these silent stalls.
  const lastSeen = useRef<number>(Date.now())
  const watchdog = useRef<ReturnType<typeof setInterval> | null>(null)

  const connect = useCallback(() => {
    if (esRef.current) {
      esRef.current.close()
    }

    const es = new EventSource('/api/events')
    esRef.current = es
    lastSeen.current = Date.now()

    es.onopen = () => {
      setConnected(true)
      lastSeen.current = Date.now()
    }

    const touch = () => { lastSeen.current = Date.now() }

    es.addEventListener('state_update', (e) => {
      touch()
      try {
        const data = JSON.parse(e.data) as AgentState[]
        setAgents(prev => agentsEqual(prev, data) ? prev : data)
      } catch { /* ignore */ }
    })

    // Phase 1: targeted state patch from chat agent transitions —
    // applies a surgical update to a single agent's state fields,
    // bypassing the full state_update rebuild cycle.
    es.addEventListener('agent_state_patch', (e) => {
      touch()
      try {
        const patch = JSON.parse(e.data) as Partial<AgentState> & {
          pokegent_id: string
          state: string
          busy_since: string
          background_tasks: number
        }
        setAgents(prev => {
          const idx = prev.findIndex(a =>
            a.pokegent_id === patch.pokegent_id || a.session_id === patch.pokegent_id
          )
          if (idx < 0) return prev
          const updated = [...prev]
          updated[idx] = {
            ...updated[idx],
            state: patch.state,
            detail: patch.detail ?? updated[idx].detail,
            busy_since: patch.busy_since,
            last_summary: patch.last_summary ?? updated[idx].last_summary,
            last_trace: patch.last_trace ?? updated[idx].last_trace,
            user_prompt: patch.user_prompt ?? updated[idx].user_prompt,
            recent_actions: patch.recent_actions ?? updated[idx].recent_actions,
            activity_feed: patch.activity_feed ?? updated[idx].activity_feed,
            card_preview: patch.card_preview ?? updated[idx].card_preview,
            background_tasks: patch.background_tasks,
            context_tokens: patch.context_tokens ?? updated[idx].context_tokens,
            context_window: patch.context_window ?? updated[idx].context_window,
          }
          return updated
        })
      } catch { /* ignore */ }
    })

    es.addEventListener('new_message', (e) => {
      touch()
      try {
        setNewMessage(JSON.parse(e.data) as AgentMessage)
      } catch { /* ignore */ }
    })

    // Server sends `event: ping` every 10s. We don't act on it — its only
    // job is to refresh `lastSeen` so the watchdog knows the stream is alive.
    es.addEventListener('ping', touch)

    es.onerror = () => {
      setConnected(false)
      es.close()
      reconnectTimeout.current = setTimeout(connect, 3000)
    }
  }, [])

  // Watchdog: if no event/ping in 30s, force-reconnect. The server pings
  // every 10s, so 30s gives ~3 missed pings before we treat the stream as
  // dead. Lowers the chance of a "stuck UI until shift+refresh" by a lot.
  useEffect(() => {
    watchdog.current = setInterval(() => {
      if (Date.now() - lastSeen.current > 30000) {
        setConnected(false)
        esRef.current?.close()
        connect()
      }
    }, 5000)
    return () => {
      if (watchdog.current) clearInterval(watchdog.current)
    }
  }, [connect])

  useEffect(() => {
    connect()
    return () => {
      esRef.current?.close()
      if (reconnectTimeout.current) clearTimeout(reconnectTimeout.current)
    }
  }, [connect])

  return { agents, newMessage, connected }
}

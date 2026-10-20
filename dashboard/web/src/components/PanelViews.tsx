import React, { useMemo, useState } from 'react'
import { Entry } from '../types/chat'
import { extractFileOpsFromEntries, parseTool } from '../utils/toolAdapters'
import { FilePathMenu } from './FilePathMenu'

// ── Shared timestamp row layout ────────────────────────────

export function TimestampRow({ ts, children, showTimestamps, padTop }: {
  ts?: number
  children: React.ReactNode
  showTimestamps?: boolean | 'debug'
  padTop?: boolean
}) {
  const rowPad = padTop ? 'pt-3' : ''
  if (!showTimestamps) return padTop ? <div className={rowPad}>{children}</div> : <>{children}</>
  const tsLabel = ts ? new Date(ts).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false }) : ''
  return (
    <table className="w-full border-collapse"><tbody><tr>
      <td className={`align-top w-0 whitespace-nowrap pr-1 ${rowPad} ${showTimestamps === 'debug' ? 'border theme-border-danger' : ''}`}>
        <span className="text-m theme-font-mono theme-text-faint tabular-nums select-none">{tsLabel}</span>
      </td>
      <td className={`align-top ${rowPad} ${showTimestamps === 'debug' ? 'border theme-border-subtle' : ''}`}>
        {children}
      </td>
    </tr></tbody></table>
  )
}

type TableColumn<T> = {
  key: string
  header: string
  width?: string
  className?: string
  render: (row: T) => React.ReactNode
}

function UnifiedPanelTable<T>({ columns, rows, rowKey, empty, scrollRef, onRowClick, expandedRow }: {
  columns: TableColumn<T>[]
  rows: T[]
  rowKey: (row: T, index: number) => string
  empty: string
  scrollRef?: React.RefObject<HTMLDivElement | null>
  onRowClick?: (row: T, event: React.MouseEvent) => void
  expandedRow?: (row: T) => React.ReactNode
}) {
  if (rows.length === 0) {
    return (
      <div className="h-full flex items-center justify-center rounded-md text-l theme-font-mono theme-text-muted" style={{ background: 'var(--theme-chat-bg)' }}>
        {empty}
      </div>
    )
  }

  const template = columns.map(c => c.width || 'minmax(0, 1fr)').join(' ')
  const fontSize = 'var(--agent-card-output-font-size, 10px)'
  return (
    <div ref={scrollRef} className="h-full overflow-y-auto rounded-md theme-font-mono" style={{ background: 'var(--theme-chat-bg)' }}>
      <div className="min-w-full">
        <div
          className="sticky top-0 z-20 grid border-b theme-border-subtle theme-font-mono theme-text-primary font-bold"
          style={{
            gridTemplateColumns: template,
            background: 'var(--theme-chat-header-bg, var(--theme-chat-tool-bg))',
            boxShadow: '0 1px 0 var(--theme-panel-divider)',
            fontSize,
          }}
        >
          {columns.map((col, i) => (
            <div key={col.key} className={`px-2 py-1.5 border-r last:border-r-0 theme-border-subtle ${i === 0 ? '' : ''}`}>
              {col.header}
            </div>
          ))}
        </div>
        {rows.map((row, index) => (
          <div key={rowKey(row, index)} className="border-b theme-border-subtle last:border-b-0">
            <div
              className={`grid theme-font-mono theme-text-secondary ${onRowClick ? 'cursor-pointer theme-bg-panel-hover' : ''}`}
              style={{ gridTemplateColumns: template, background: 'var(--theme-chat-tool-bg)', fontSize }}
              onClick={(event) => onRowClick?.(row, event)}
            >
              {columns.map(col => (
                <div key={col.key} className={`min-w-0 px-2 py-1.5 border-r last:border-r-0 theme-border-subtle ${col.className || ''}`}>
                  {col.render(row)}
                </div>
              ))}
            </div>
            {expandedRow?.(row)}
          </div>
        ))}
      </div>
    </div>
  )
}

// ── Panel views: Files + Commands ──────────────────────────

export function FilesView({ entries, scrollRef }: { entries: Entry[]; showTimestamps?: boolean | 'debug'; scrollRef?: React.RefObject<HTMLDivElement | null> }) {
  const [fileMenu, setFileMenu] = useState<{ path: string; x: number; y: number } | null>(null)
  const files = useMemo(() => {
    const all = extractFileOpsFromEntries(entries as any)
    return all.filter((f: any) => f.verb === 'edited' || f.verb === 'created' || f.verb === 'notebook')
  }, [entries])

  return (
    <>
      <UnifiedPanelTable
        rows={files}
        scrollRef={scrollRef}
        empty="No file edits yet"
        rowKey={(f: any, i) => `${f.path}-${f.verb}-${f.ts || i}`}
        onRowClick={(f: any, e) => setFileMenu({ path: f.path, x: e.clientX, y: e.clientY })}
        columns={[
        {
          key: 'kind',
          header: 'Kind',
          width: '88px',
          render: (f: any) => (
            <span className={`theme-font-mono ${
              f.verb === 'edited' ? 'theme-text-warning' :
              f.verb === 'created' ? 'theme-text-success' :
              'theme-text-accent'
            }`}>{f.verb}</span>
          ),
        },
        {
          key: 'file',
          header: 'File',
          width: 'minmax(180px, 1fr)',
          render: (f: any) => {
            const filename = f.path.split('/').pop() || f.path
            const dir = f.path.slice(0, f.path.length - filename.length) || '—'
            return (
              <span className="block min-w-0 leading-snug">
                <span className="theme-text-primary truncate block">{filename}</span>
                <span className="theme-text-faint truncate block text-m mt-0.5">{dir}</span>
              </span>
            )
          },
        },
        {
          key: 'diff',
          header: 'Diff',
          width: 'minmax(120px, 0.45fr)',
          render: (f: any) => <DiffText value={f.diffSummary || '—'} />,
        },
        ]}
      />
      {fileMenu && <FilePathMenu path={fileMenu.path} x={fileMenu.x} y={fileMenu.y} onClose={() => setFileMenu(null)} />}
    </>
  )
}

function DiffText({ value }: { value: string }) {
  const parts = value.split(/(\+\d+|−\d+|-\d+)/g).filter(Boolean)
  return (
    <span className="truncate block">
      {parts.map((part, i) => {
        const cls = part.startsWith('+')
          ? 'theme-text-success'
          : part.startsWith('−') || /^-\d+/.test(part)
            ? 'theme-text-danger'
            : 'theme-text-muted'
        return <span key={i} className={cls}>{part}</span>
      })}
    </span>
  )
}

export interface CommandRecord {
  command: string
  summary: string
  isLong: boolean
  status: string
  description?: string
  ts?: number
}

export function summarizeCommand(cmd: string): string {
  const trimmed = cmd.trim()
  if (trimmed.startsWith('python3 -c') || trimmed.startsWith('python -c')) {
    const m = trimmed.match(/(?:def |class |import |from )(\w+)/)
    return `python3 -c "${m ? m[1] + '…' : 'inline script'}"`
  }
  if (trimmed.startsWith('curl ')) {
    const urlMatch = trimmed.match(/https?:\/\/[^\s'"]+/)
    const methodMatch = trimmed.match(/-X\s+(\w+)/)
    const method = methodMatch ? methodMatch[1] : 'GET'
    return `curl ${method} ${urlMatch ? urlMatch[0].split('?')[0] : '…'}`
  }
  if (trimmed.startsWith('cat ') && trimmed.includes('|')) {
    const pipe = trimmed.split('|').map(s => s.trim().split(/\s+/)[0]).join(' | ')
    return pipe.length > 80 ? pipe.slice(0, 77) + '…' : pipe
  }
  const first = trimmed.split('\n')[0]
  if (first.length > 100) return first.slice(0, 97) + '…'
  return first
}

export function extractCommands(entries: Entry[]): CommandRecord[] {
  const cmds: CommandRecord[] = []
  for (const e of entries) {
    if (e.kind !== 'tool') continue
    const tc = e.data
    const parsed = parseTool(tc as any)
    const inp = tc.rawInput && typeof tc.rawInput === 'object' ? tc.rawInput as Record<string, unknown> : undefined
    const cmd = parsed.bashCommand || (inp?.command as string | undefined) || (inp?.cmd as string | undefined)
    if (!cmd || typeof cmd !== 'string') continue
    const status = tc.status === 'completed' ? 'done' : tc.status === 'failed' ? 'error' : tc.status || ''
    const isLong = cmd.length > 120 || cmd.split('\n').length > 3
    cmds.push({
      command: cmd,
      summary: isLong ? summarizeCommand(cmd) : cmd,
      isLong,
      status,
      description: parsed.bashDescription || inp?.description as string | undefined,
      ts: e.ts,
    })
  }
  return cmds
}

export function CommandsView({ entries, scrollRef }: { entries: Entry[]; showTimestamps?: boolean | 'debug'; scrollRef?: React.RefObject<HTMLDivElement | null> }) {
  const allCmds = useMemo(() => extractCommands(entries), [entries])
  const [filter, setFilter] = useState('')
  const [expanded, setExpanded] = useState<Record<number, boolean>>({})
  const filtered = filter
    ? allCmds.filter(c => c.command.toLowerCase().includes(filter.toLowerCase()) || (c.description || '').toLowerCase().includes(filter.toLowerCase()))
    : allCmds
  const rowIndex = new Map(filtered.map((cmd, i) => [cmd, i]))

  return (
    <div className="h-full flex flex-col rounded-md" style={{ background: 'var(--theme-chat-bg)' }}>
      <div className="shrink-0 p-2 border-b theme-border-subtle">
        <input
          type="text"
          value={filter}
          onChange={e => setFilter(e.target.value)}
          placeholder="filter commands…"
          className="w-full theme-bg-panel-subtle border theme-border-subtle rounded px-2 py-1 text-l theme-font-mono theme-text-secondary theme-placeholder-faint outline-none focus-theme-border-subtle"
        />
      </div>
      <div className="flex-1 min-h-0">
        <UnifiedPanelTable
          rows={filtered}
          scrollRef={scrollRef}
          empty={allCmds.length === 0 ? 'No commands yet' : 'No matches'}
          rowKey={(cmd, i) => `${cmd.ts || i}-${cmd.summary}`}
          onRowClick={(cmd) => {
            const i = rowIndex.get(cmd) ?? 0
            if (cmd.isLong) setExpanded(prev => ({ ...prev, [i]: !prev[i] }))
            else navigator.clipboard.writeText(cmd.command)
          }}
          expandedRow={(cmd) => {
            const i = rowIndex.get(cmd) ?? 0
            if (!expanded[i]) return null
            return (
              <pre className="m-0 border-t theme-border-subtle px-2 py-2 text-l theme-font-mono theme-text-muted leading-snug overflow-x-auto whitespace-pre-wrap break-all" style={{ background: 'var(--theme-chat-tool-bg)' }}>
                {cmd.command}
              </pre>
            )
          }}
          columns={[
            {
              key: 'status',
              header: 'Status',
              width: '64px',
              render: (cmd) => {
                const color = cmd.status === 'error' ? 'theme-text-danger' : cmd.status === 'done' ? 'theme-text-success' : 'theme-text-faint'
                return <span className={color}>{cmd.status || '—'}</span>
              },
            },
            {
              key: 'command',
              header: 'Command',
              width: 'minmax(180px, 1fr)',
              render: (cmd) => <span className="theme-text-secondary truncate block">{cmd.summary}</span>,
            },
            {
              key: 'action',
              header: '',
              width: '52px',
              render: (cmd) => {
                const i = rowIndex.get(cmd) ?? 0
                return <span className="theme-text-muted">{cmd.isLong ? (expanded[i] ? 'hide' : 'show') : 'copy'}</span>
              },
            },
          ]}
        />
      </div>
    </div>
  )
}

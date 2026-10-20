import ReactMarkdown, { defaultUrlTransform } from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { Highlight, themes } from 'prism-react-renderer'
import { useRef, useState } from 'react'
import { openUrlInConfiguredBrowser } from '../utils/openExternal'

// Markdown is the full-fidelity renderer used in chat-style transcripts.
// Wraps react-markdown with custom renderers — links open in new tabs, code
// blocks get syntax highlighting via prism-react-renderer, prose spacing
// matches Claude Code's terminal output (tight within block, loose between).
//
// For tiny one-line summaries (AgentCard's last_summary box) use
// `renderMiniMarkdown` from utils/miniMarkdown.ts — it's a string→HTML
// regex pass that doesn't pull react-markdown into the per-card render path.

interface MarkdownProps {
  children: string
  desktop?: { openURL: (url: string) => Promise<unknown>; imageLabel: string; copyLabel: string; copiedLabel: string }
}

export function Markdown({ children, desktop }: MarkdownProps) {
  return (
    <ReactMarkdown
      remarkPlugins={[remarkGfm]}
      urlTransform={(url) => {
        if (!desktop) return defaultUrlTransform(url)
        if (/^[\u0000-\u0020]|[\u0000-\u001f\u007f]/.test(url)) return ''
        try { const parsed = new URL(url); return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !parsed.username && !parsed.password ? parsed.href : '' } catch { return '' }
      }}
      components={{
        ...(desktop ? { img: ({ alt }: { alt?: string }) => <span className="win-inline-note">{desktop.imageLabel}{alt ? ` (${alt})` : ''}</span> } : {}),
        a: ({ node: _node, href, children, ...props }) => (
          <a
            href={href}
            target="_blank"
            rel="noopener noreferrer"
            className="text-accent-blue underline decoration-accent-blue/40 underline-offset-2 hover:decoration-accent-blue break-all"
            onClick={(e) => {
              if (desktop) { e.preventDefault(); if (href) void desktop.openURL(href).catch(() => undefined); return }
              if (!href || href.startsWith('#') || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return
              e.preventDefault()
              void openUrlInConfiguredBrowser(href)
            }}
            {...props}
          >
            {children}
          </a>
        ),
        p: ({ node: _node, children, ...props }) => (
          <p data-selectable-text className="mb-3 first:mt-0 mt-3 text-l theme-font-mono leading-snug" {...props}>{children}</p>
        ),
        ul: ({ node: _node, children, ...props }) => (
          <ul data-selectable-text className="my-3 pl-5 list-disc text-l theme-font-mono space-y-1.5 [&_p]:my-0 [&_p]:text-l" {...props}>{children}</ul>
        ),
        ol: ({ node: _node, children, ...props }) => (
          <ol data-selectable-text className="my-3 pl-5 list-decimal text-l theme-font-mono space-y-1.5 [&_p]:my-0 [&_p]:text-l" {...props}>{children}</ol>
        ),
        li: ({ node: _node, children, ...props }) => (
          <li data-selectable-text className="text-l theme-font-mono leading-snug" {...props}>{children}</li>
        ),
        h1: ({ node: _node, children, ...props }) => (
          <h2 data-selectable-text className="theme-text-primary font-bold text-xl theme-font-mono mt-4 mb-1.5 leading-tight first:mt-0" {...props}>{children}</h2>
        ),
        h2: ({ node: _node, children, ...props }) => (
          <h3 data-selectable-text className="theme-text-primary font-bold text-l theme-font-mono mt-4 mb-1.5 leading-tight first:mt-0" {...props}>{children}</h3>
        ),
        h3: ({ node: _node, children, ...props }) => (
          <h4 data-selectable-text className="theme-text-primary font-semibold text-l theme-font-mono mt-3 mb-1 leading-tight first:mt-0" {...props}>{children}</h4>
        ),
        strong: ({ node: _node, children, ...props }) => (
          <strong className="theme-text-primary font-semibold" {...props}>{children}</strong>
        ),
        code: ({ node: _node, className, children, ...props }) => {
          const isBlock = /\n/.test(String(children))
          const langMatch = /language-([\w-]+)/.exec(className || '')
          const lang = langMatch?.[1] || ''
          if (!isBlock) {
            return (
              <code
                data-selectable-text
                className="px-1 py-0.5 rounded theme-bg-panel-subtle text-accent-yellow text-l theme-font-mono break-words"
                {...props}
              >
                {children}
              </code>
            )
          }
          return <CodeBlock code={String(children).replace(/\n$/, '')} lang={lang} copyLabel={desktop?.copyLabel} copiedLabel={desktop?.copiedLabel} />
        },
        pre: ({ node: _node, children }) => <>{children}</>,
        hr: () => <hr className="theme-border-subtle my-2" />,
        blockquote: ({ node: _node, children, ...props }) => (
          <blockquote data-selectable-text className="border-l-2 theme-border-subtle pl-2 my-1 theme-text-secondary italic" {...props}>{children}</blockquote>
        ),
        table: ({ node: _node, children, ...props }) => (
          <div className="my-2 overflow-x-auto rounded border theme-border-subtle">
            <table className="w-full border-collapse text-l theme-font-mono" {...props}>
              {children}
            </table>
          </div>
        ),
        thead: ({ node: _node, children, ...props }) => (
          <thead className="theme-bg-panel-subtle border-b theme-border-subtle" {...props}>{children}</thead>
        ),
        tbody: ({ node: _node, children, ...props }) => (
          <tbody {...props}>{children}</tbody>
        ),
        tr: ({ node: _node, children, ...props }) => (
          <tr className="border-b theme-border-subtle last:border-b-0" {...props}>{children}</tr>
        ),
        th: ({ node: _node, children, style: _style, ...props }) => (
          <th data-selectable-text className="px-2.5 py-1.5 text-left font-semibold theme-text-primary border-r theme-border-subtle last:border-r-0 align-top" {...props}>
            {children}
          </th>
        ),
        td: ({ node: _node, children, style: _style, ...props }) => (
          <td data-selectable-text className="px-2.5 py-1.5 theme-text-primary/85 border-r theme-border-subtle last:border-r-0 align-top" {...props}>
            {children}
          </td>
        ),
      }}
    >
      {children}
    </ReactMarkdown>
  )
}

function CodeBlock({ code, lang, copyLabel = 'COPY', copiedLabel = 'COPIED' }: { code: string; lang: string; copyLabel?: string; copiedLabel?: string }) {
  const [copied, setCopied] = useState(false)
  const textAreaRef = useRef<HTMLTextAreaElement | null>(null)
  const language = (lang || 'text') as keyof typeof themes
  const copyCode = async () => {
    try {
      if (navigator.clipboard?.writeText) {
        await navigator.clipboard.writeText(code)
      } else {
        throw new Error('clipboard api unavailable')
      }
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1200)
    } catch {
      const ta = textAreaRef.current
      if (!ta) {
        setCopied(false)
        return
      }
      ta.value = code
      ta.select()
      try {
        document.execCommand('copy')
        setCopied(true)
        window.setTimeout(() => setCopied(false), 1200)
      } catch {
        setCopied(false)
      }
    }
  }

  return (
    <div
      className="chat-code-block group relative my-1.5 min-w-0 select-text"
      data-no-drag
      onMouseDownCapture={(e) => e.stopPropagation()}
    >
      <button
        type="button"
        className="chat-code-copy absolute right-1.5 top-1.5 z-10 rounded theme-modal-scrim px-1.5 py-0.5 text-s theme-font-display uppercase pixel-shadow theme-text-faint opacity-0 transition-opacity theme-hover-text-primary group-hover:opacity-100"
        onMouseDown={(e) => { e.preventDefault(); e.stopPropagation() }}
        onClick={(e) => { e.preventDefault(); e.stopPropagation(); void copyCode() }}
      >
        {copied ? copiedLabel : copyLabel}
      </button>
      <textarea ref={textAreaRef} aria-hidden tabIndex={-1} className="fixed -left-[9999px] -top-[9999px] w-px h-px opacity-0" />
      <Highlight code={code} language={language as string} theme={themes.vsDark}>
        {({ className, style, tokens, getLineProps, getTokenProps }) => (
          <pre
            className={`${className} chat-code-pre rounded-md p-2.5 overflow-auto text-l theme-font-mono leading-snug select-text`}
            style={{ ...style, background: 'var(--theme-chat-bg)' }}
          >
            <code data-selectable-text className="chat-code-code block min-w-max pr-12 select-text">
              {tokens.map((line, i) => (
                <span key={i} {...getLineProps({ line })} className="block">
                  {line.map((token, key) => (
                    <span key={key} {...getTokenProps({ token })} />
                  ))}
                </span>
              ))}
            </code>
          </pre>
        )}
      </Highlight>
    </div>
  )
}

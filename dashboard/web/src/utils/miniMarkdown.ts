// renderMiniMarkdown is a tiny markdown-to-HTML renderer for one-line
// summaries that need to read in tight grid cells (AgentCard's last-summary
// box). Handles **bold** and `code` only — anything richer should use the
// full Markdown component (which pulls in react-markdown + prism).
//
// Output is HTML-escaped first so it's safe to drop into
// dangerouslySetInnerHTML.
export function renderMiniMarkdown(text: string): string {
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/\*\*(.+?)\*\*/g, '<strong>$1</strong>')
    .replace(/`([^`]+)`/g, '<code>$1</code>')
}

#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'

const root = path.resolve(process.cwd(), 'src/components')
const exts = new Set(['.ts', '.tsx'])
const patterns = [
  { name: 'hex color', re: /#[0-9a-fA-F]{3,8}\b/g },
  { name: 'rgb/rgba color', re: /\brgba?\(/g },
  { name: 'hardcoded font family', re: /fontFamily\s*:/g },
  { name: 'tailwind white text', re: /\btext-white(?:\/\d+)?\b/g },
  { name: 'tailwind black bg', re: /\bbg-black(?:\/\d+)?\b/g },
  { name: 'tailwind white bg', re: /\bbg-white(?:\/(?:\d+|\[[^\]]+\]))?\b/g },
  { name: 'tailwind white border', re: /\bborder-white(?:\/\d+)?\b/g },
  { name: 'tailwind black border', re: /\bborder-black(?:\/\d+)?\b/g },
  { name: 'tailwind hardcoded color shade', re: /\b(?:text|bg|border)-(?:red|green|blue|cyan|amber|yellow|orange|purple|pink|slate|zinc|gray|neutral|stone)-(?:50|100|200|300|400|500|600|700|800|900)(?:\/\d+)?\b/g },
  { name: 'legacy pixel font utility', re: /(?<!theme-)\bfont-pixel\b/g },
  { name: 'legacy mono font utility', re: /(?<!theme-)\bfont-mono\b/g },
]

function walk(dir) {
  const out = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const p = path.join(dir, entry.name)
    if (entry.isDirectory()) out.push(...walk(p))
    else if (exts.has(path.extname(entry.name))) out.push(p)
  }
  return out
}

const hits = []
for (const file of walk(root)) {
  const text = fs.readFileSync(file, 'utf8')
  const lines = text.split('\n')
  for (const { name, re } of patterns) {
    for (const match of text.matchAll(re)) {
      const idx = match.index ?? 0
      const lineNo = text.slice(0, idx).split('\n').length
      const line = lines[lineNo - 1].trim()
      if (line.includes('theme-audit-ignore')) continue
      if (line.includes('var(--theme-')) continue
      if ((match[0] === 'rgb(' || match[0] === 'rgba(') && (line.includes('`rgb') || line.includes('rgba(${'))) continue
      hits.push({ file: path.relative(process.cwd(), file), lineNo, name, match: match[0], line })
    }
  }
}

if (hits.length) {
  console.error(`Theme token audit found ${hits.length} unapproved hardcoded presentation value(s):`)
  for (const hit of hits.slice(0, 250)) console.error(`${hit.file}:${hit.lineNo}: ${hit.name}: ${hit.match} :: ${hit.line}`)
  if (hits.length > 250) console.error(`... ${hits.length - 250} more`)
  process.exit(1)
}

console.log('Theme token audit passed: no unapproved hardcoded presentation values in src/components/**')

#!/usr/bin/env node
import fs from 'node:fs'
import path from 'node:path'

const roots = [path.resolve(process.cwd(), 'src')]
const exts = new Set(['.ts', '.tsx', '.css'])
const allowed = new Set(['text-xs', 'text-s', 'text-m', 'text-l', 'text-xl'])
const patterns = [
  { name: 'arbitrary Tailwind font size', re: /\btext-\[[0-9.]+px\]/g },
  { name: 'arbitrary Tailwind line height', re: /\bleading-\[[0-9.]+px\]/g },
  { name: 'inline numeric fontSize', re: /fontSize\s*:\s*[0-9.]+/g },
  { name: 'disallowed named Tailwind font size', re: /\btext-(?:sm|base|lg|2xl|3xl|4xl|5xl|6xl|7xl|8xl|9xl)\b/g },
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
for (const root of roots) {
  for (const file of walk(root)) {
    if (file.endsWith('styles/pokemon.css')) continue
    const text = fs.readFileSync(file, 'utf8')
    const lines = text.split('\n')
    for (const { name, re } of patterns) {
      for (const match of text.matchAll(re)) {
        const lineNo = text.slice(0, match.index ?? 0).split('\n').length
        const line = lines[lineNo - 1].trim()
        if (line.includes('typography-audit-ignore')) continue
        hits.push({ file: path.relative(process.cwd(), file), lineNo, name, match: match[0], line })
      }
    }
    // Catch accidental text-* scale classes outside the approved five, but avoid color utilities.
    for (const match of text.matchAll(/\btext-(xs|s|m|l|xl)\b/g)) {
      if (!allowed.has(match[0])) hits.push({ file: path.relative(process.cwd(), file), lineNo: text.slice(0, match.index ?? 0).split('\n').length, name: 'invalid typography token', match: match[0], line: '' })
    }
  }
}

if (hits.length) {
  console.error(`Typography audit found ${hits.length} unapproved font-size value(s):`)
  for (const hit of hits.slice(0, 200)) console.error(`${hit.file}:${hit.lineNo}: ${hit.name}: ${hit.match} :: ${hit.line}`)
  if (hits.length > 200) console.error(`... ${hits.length - 200} more`)
  process.exit(1)
}
console.log('Typography audit passed: font sizes use text-xs/text-s/text-m/text-l/text-xl or theme vars')

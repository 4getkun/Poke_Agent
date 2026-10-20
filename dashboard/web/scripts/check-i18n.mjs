import { readFile } from 'node:fs/promises'
import { readdir } from 'node:fs/promises'
import { resolve } from 'node:path'
import { fileURLToPath } from 'node:url'

const root = fileURLToPath(new URL('..', import.meta.url))
const catalogPath = resolve(root, 'src/windows/i18n.ts')
const sourceDir = resolve(root, 'src/windows')
const catalog = await readFile(catalogPath, 'utf8')

function extractCatalog(name, endMarker) {
  const start = catalog.indexOf(`const ${name} = {`)
  if (start < 0) throw new Error(`Catalog ${name} was not found`)
  const end = catalog.indexOf(endMarker, start)
  if (end < 0) throw new Error(`Catalog end marker for ${name} was not found`)
  const body = catalog.slice(start, end)
  return new Set([...body.matchAll(/^\s*'([^']+)'\s*:/gm)].map((match) => match[1]))
}

const japanese = extractCatalog('jaMessages', '} satisfies Record<string, string>')
const english = extractCatalog('enMessages', '} satisfies Record<MessageKey, string>')
const missingEnglish = [...japanese].filter((key) => !english.has(key))
const missingJapanese = [...english].filter((key) => !japanese.has(key))
const unknownUse = []

for (const entry of await readdir(sourceDir, { withFileTypes: true })) {
  if (!entry.isFile() || !/\.tsx?$/.test(entry.name) || entry.name === 'i18n.ts') continue
  const source = await readFile(resolve(sourceDir, entry.name), 'utf8')
  for (const match of source.matchAll(/\bt\(\s*'([^']+)'/g)) {
    if (!japanese.has(match[1])) unknownUse.push(`${entry.name}: ${match[1]}`)
  }
}

if (missingEnglish.length || missingJapanese.length || unknownUse.length) {
  for (const key of missingEnglish) console.error(`Missing English key: ${key}`)
  for (const key of missingJapanese) console.error(`Missing Japanese key: ${key}`)
  for (const key of unknownUse) console.error(`Unknown translation use: ${key}`)
  process.exitCode = 1
} else {
  console.log(`i18n OK: ${japanese.size} keys in both catalogs; literal component keys resolve.`)
}

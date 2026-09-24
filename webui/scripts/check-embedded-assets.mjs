import { readdir, readFile, stat } from 'node:fs/promises'
import { dirname, extname, relative, resolve, sep } from 'node:path'
import { fileURLToPath } from 'node:url'

const scriptDir = dirname(fileURLToPath(import.meta.url))
const root = resolve(scriptDir, '../../internal/api/dashboard/static/web')
const htmlPath = resolve(root, 'index.html')
const html = await readFile(htmlPath, 'utf8')
const reachable = new Set()
const queue = []
const base = '/admin/static/web/'

function resolveReference(from, reference) {
  if (/^(?:data:|https?:|#)/i.test(reference)) return undefined
  let target
  if (reference.startsWith(base)) {
    target = resolve(root, reference.slice(base.length))
  } else if (reference.startsWith('assets/')) {
    target = resolve(root, reference)
  } else if (reference.startsWith('/')) {
    return undefined
  } else {
    target = resolve(root, from ? dirname(from) : '.', reference)
  }
  const path = relative(root, target).split(sep).join('/')
  if (path === '..' || path.startsWith(`..${sep}`) || path.startsWith(sep)) {
    throw new Error(`embedded asset reference escapes static root: ${reference}`)
  }
  return path
}

for (const match of html.matchAll(/(?:src|href)=["']([^"']+)["']/g)) {
  const path = resolveReference('', match[1])
  if (path) queue.push(path)
}
if (!queue.length) throw new Error('dashboard index does not reference a local JavaScript or CSS asset')

while (queue.length) {
  const path = queue.pop()
  if (reachable.has(path)) continue
  const absolute = resolve(root, path)
  if (!(await stat(absolute).catch(() => undefined))) {
    throw new Error(`dashboard references a missing embedded asset: ${path}`)
  }
  reachable.add(path)
  const extension = extname(path)
  if (extension === '.js') {
    const source = await readFile(absolute, 'utf8')
    // Vite's preload helper records lazy chunks in a dependency array as well
    // as emitting import() expressions, so inspect all local JS/CSS references.
    for (const match of source.matchAll(/["']([^"']+\.(?:js|css)(?:[?#].*)?)["']/g)) {
      const child = resolveReference(path, match[1])
      if (child) queue.push(child)
    }
  } else if (extension === '.css') {
    const source = await readFile(absolute, 'utf8')
    for (const match of source.matchAll(/url\(["']?([^"')]+)["']?\)/g)) {
      const child = resolveReference(path, match[1])
      if (child) queue.push(child)
    }
  }
}

async function walk(directory, prefix = '') {
  const files = []
  for (const entry of await readdir(directory, { withFileTypes: true })) {
    const path = prefix ? `${prefix}/${entry.name}` : entry.name
    if (entry.isDirectory()) files.push(...await walk(resolve(directory, entry.name), path))
    else files.push(path)
  }
  return files
}

const files = await walk(root)
for (const path of files) {
  if (path === 'index.html') continue
  if (!reachable.has(path)) throw new Error(`stale embedded asset is not reachable from index.html: ${path}`)
  if (/\.(?:js|css)$/.test(path) && !/^[^/]+-[A-Za-z0-9_-]{8}\.(?:js|css)$/.test(path.split('/').at(-1))) {
    throw new Error(`JavaScript/CSS asset is missing its content hash: ${path}`)
  }
}

const woffFallbacks = files.filter(path => path.endsWith('.woff'))
if (woffFallbacks.length) throw new Error(`legacy WOFF files must not be embedded: ${woffFallbacks.slice(0, 3).join(', ')}`)

const cssFiles = files.filter(path => path.endsWith('.css'))
const cssSources = await Promise.all(cssFiles.map(path => readFile(resolve(root, path), 'utf8')))
const fontFaces = cssSources.flatMap(source => [...source.matchAll(/@font-face\s*\{[^}]*\}/g)].map(match => match[0]))
if (fontFaces.some(face => /url\(data:font\//i.test(face))) throw new Error('font faces must be external WOFF2 assets, not CSP-blocked data: URLs')
const notoFaces = fontFaces.filter(face => /font-family:["']?Noto Sans SC["']?;/.test(face))
if (notoFaces.length < 180) throw new Error(`expected unicode-sliced Noto Sans SC faces for both weights; found ${notoFaces.length}`)
const notoCounts = new Map([['400', 0], ['500', 0]])
for (const face of notoFaces) {
  if (!/unicode-range\s*:/i.test(face)) throw new Error('Noto Sans SC face is missing unicode-range; fonts would load as whole files')
  if (!/url\([^)]*\.woff2(?:["')?]|$)/i.test(face)) throw new Error('Noto Sans SC face is missing its WOFF2 source')
  if (/url\([^)]*\.woff(?:["')?]|$)/i.test(face)) throw new Error('Noto Sans SC face still references legacy WOFF')
  const weight = face.match(/font-weight:\s*(400|500)/i)?.[1]
  if (weight) notoCounts.set(weight, notoCounts.get(weight) + 1)
}
for (const weight of ['400', '500']) {
  if (notoCounts.get(weight) < 90) throw new Error(`expected at least 90 Noto Sans SC unicode slices at weight ${weight}; found ${notoCounts.get(weight)}`)
}

console.log(`verified ${reachable.size} reachable embedded assets and ${notoFaces.length} WOFF2-only Noto unicode slices`)

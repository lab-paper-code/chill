// Capture directory reader for server/replay.mjs: index matching, one fixed time shift, list paging.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { parseMeasurementJSON } from '../lib/measurement-summary.ts'

const VOLATILE = new Set(['from', 'to', 'at', 'cursor', 'limit', 'offset', 'order', 'page', 'sort'])
const RFC3339 = /(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d{1,9})?(Z|[+-]\d\d:\d\d)/g

// Shift every RFC3339 timestamp in text by whole seconds; fraction and zone are kept verbatim.
export function shiftTimes(text, ms) {
  if (!ms) return text
  return text.replace(RFC3339, (all, wall, fraction = '', zone) => wall.startsWith('0001-') ? all : new Date(Date.parse(`${wall}Z`) + ms).toISOString().slice(0, 19) + fraction + zone)
}

// RFC3339(Nano) text as BigInt nanoseconds since the Unix epoch; null when invalid.
export function nanos(text) {
  const match = /^(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(?:\.(\d{1,9}))?(Z|[+-]\d\d:\d\d)$/.exec(text ?? '')
  if (!match) return null
  const seconds = Date.parse(`${match[1]}${match[3]}`)
  return Number.isFinite(seconds) ? BigInt(seconds) * 1_000_000n + BigInt((match[2] ?? '').padEnd(9, '0')) : null
}
export const msNanos = ms => BigInt(Math.round(ms)) * 1_000_000n

// One page of `items`; nextOffset only while items remain.
export function pageOf(items, offset, limit) {
  const page = { offset, limit, total: items.length, ...(offset + limit < items.length ? { nextOffset: offset + limit } : {}) }
  return { items: items.slice(offset, offset + limit), page }
}

const canonical = params => params.map(([key, value]) => `${key}=${value}`).sort().join('&')
const ITEM_KEYS = ['items', 'runs', 'profiles', 'systems']

// `startedAt` fixes the shift once (whole minutes keep 5 s and 30 s power buckets aligned): every
// captured record keeps one timestamp for the life of the process and simply ages afterwards.
export function loadCapture(dir, startedAt = Date.now()) {
  const { meta, index } = JSON.parse(readFileSync(join(dir, '_index.json'), 'utf8'))
  const frames = JSON.parse(readFileSync(join(dir, '_live.json'), 'utf8'))
  const entries = Object.entries(index).map(([key, value]) => {
    const at = key.indexOf('?')
    const params = at < 0 ? [] : [...new URLSearchParams(key.slice(at + 1))]
    return { key, path: at < 0 ? key : key.slice(0, at), params, canonical: canonical(params), ...value }
  })
  const capturedAt = Date.parse(meta.capturedAt)
  const shift = Math.round((startedAt - capturedAt) / 60_000) * 60_000
  const texts = new Map(), raw = new Map()
  const capture = {
    meta, frames, capturedAt, shift, entries,
    rawText(entry) {
      if (!raw.has(entry.file)) raw.set(entry.file, readFileSync(join(dir, entry.file), 'utf8'))
      return raw.get(entry.file)
    },
    // Shifted body text.
    text(entry) {
      if (!texts.has(entry.file)) texts.set(entry.file, shiftTimes(capture.rawText(entry), shift))
      return texts.get(entry.file)
    },
    // Exact path?query, else the same path scored by shared non-volatile query pairs (fewest params on ties).
    match(path, search = '') {
      const params = [...new URLSearchParams(search)]
      const same = entries.filter(entry => entry.path === path)
      const exact = same.find(entry => entry.canonical === canonical(params))
      if (exact || !same.length) return exact ?? null
      const wanted = params.filter(([key]) => !VOLATILE.has(key))
      const score = entry => wanted.reduce((sum, [key, value]) => sum + (entry.params.some(([k, v]) => k === key && v === value) ? 2 : entry.params.some(([k]) => k === key) ? 1 : 0), 0)
        - entry.params.filter(([key]) => !VOLATILE.has(key) && !wanted.some(([k]) => k === key)).length
      return same.reduce((best, entry) => score(entry) > score(best) || (score(entry) === score(best) && entry.params.length < best.params.length) ? entry : best)
    },
    // Parsed shifted body (int64-safe, readProjection expanded); null when the capture lacks it.
    json(path, search = '') {
      const entry = capture.match(path, search)
      return entry ? parseMeasurementJSON(capture.text(entry)) : null
    },
    // Every captured body for a path whose non-paging params equal those of `search`, parsed and shifted.
    pages(path, search = '') {
      const wanted = canonical([...new URLSearchParams(search)].filter(([key]) => key !== 'offset' && key !== 'limit'))
      return entries.filter(entry => entry.path === path && entry.status === 200 && canonical(entry.params.filter(([key]) => key !== 'offset' && key !== 'limit')) === wanted)
        .map(entry => parseMeasurementJSON(capture.text(entry)))
    },
    // Items known for a paged list: captured pages joined from offset 0 while contiguous. `complete`
    // means every item up to the captured total is known; otherwise the list ends at the known items.
    known(path, search = '') {
      let pages = capture.pages(path, search)
      if (!pages.length) {
        const entry = capture.match(path, search)
        if (!entry || entry.status !== 200) return null
        pages = [parseMeasurementJSON(capture.text(entry))]
      }
      const body = pages[0], key = ITEM_KEYS.find(name => Array.isArray(body[name]))
      if (!key || !body.page) return null
      const byOffset = new Map(pages.map(page => [page.page.offset ?? 0, page[key]]))
      const items = []
      while (byOffset.has(items.length) && byOffset.get(items.length).length) items.push(...byOffset.get(items.length))
      return { body, key, items, complete: items.length >= body.page.total }
    },
  }
  return capture
}

// Capture directory reader for server/replay.mjs: index matching and time shifting.
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import { parseMeasurementJSON } from '../lib/measurement-summary.ts'

const VOLATILE = new Set(['from', 'to', 'at', 'cursor', 'limit', 'offset', 'order', 'page', 'sort'])
const RFC3339 = /(\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d)(\.\d{1,9})?(Z|[+-]\d\d:\d\d)/g

// Shift every RFC3339 timestamp in text by a whole number of seconds; fraction and zone are kept verbatim.
export function shiftTimes(text, ms) {
  if (!ms) return text
  return text.replace(RFC3339, (all, wall, fraction = '', zone) => wall.startsWith('0001-') ? all : new Date(Date.parse(`${wall}Z`) + ms).toISOString().slice(0, 19) + fraction + zone)
}

const canonical = params => params.map(([key, value]) => `${key}=${value}`).sort().join('&')

export function loadCapture(dir) {
  const { meta, index } = JSON.parse(readFileSync(join(dir, '_index.json'), 'utf8'))
  const frames = JSON.parse(readFileSync(join(dir, '_live.json'), 'utf8'))
  const entries = Object.entries(index).map(([key, value]) => {
    const at = key.indexOf('?')
    const params = at < 0 ? [] : [...new URLSearchParams(key.slice(at + 1))]
    return { key, path: at < 0 ? key : key.slice(0, at), params, canonical: canonical(params), ...value }
  })
  const bodies = new Map()
  const capturedAt = Date.parse(meta.capturedAt)
  return {
    meta, frames, capturedAt,
    // Whole seconds keep whole-second buckets whole and fractional digit counts unchanged.
    shiftMs: (now = Date.now()) => Math.round((now - capturedAt) / 1000) * 1000,
    text(entry) {
      if (!bodies.has(entry.file)) bodies.set(entry.file, readFileSync(join(dir, entry.file), 'utf8'))
      return bodies.get(entry.file)
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
    // Parsed (int64-safe, readProjection expanded) copy of the body served for a path; null when the capture lacks it.
    json(path, search = '') {
      const entry = this.match(path, search)
      return entry ? parseMeasurementJSON(this.text(entry)) : null
    },
  }
}

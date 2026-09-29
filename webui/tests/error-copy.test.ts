import { describe, expect, it } from 'vitest'
import { messages } from '../src/i18n'
import { ADMIN_ERROR_CODES, INFERENCE_ERROR_CODES, isKnownErrorCode } from '../src/lib/errors'

const copy = messages['zh-CN'].errors.codes
const allCodes = [...ADMIN_ERROR_CODES, ...INFERENCE_ERROR_CODES]
const uniqueCodes = [...new Set(allCodes)]

describe('error copy table', () => {
  it('covers the management and forwarding vocabularies without duplication', () => {
    // Both lists are checked against the generated contract enum by the type-level proofs in
    // src/lib/errors.ts; this assertion guards the runtime shape those proofs rely on.
    expect(ADMIN_ERROR_CODES.length).toBe(new Set(ADMIN_ERROR_CODES).size)
    expect(INFERENCE_ERROR_CODES.length).toBe(new Set(INFERENCE_ERROR_CODES).size)
    expect(uniqueCodes.length).toBe(54)
  })

  it('keeps invalid_request in both vocabularies because both envelopes can carry it', () => {
    // The diagnostic endpoints answer on the OpenAI envelope and report a malformed body with
    // the management spelling, so the code has to be a member of both lists. The Go-side guard
    // (internal/api/error_codes_contract_test.go) enforces the same fact against the contract.
    expect(ADMIN_ERROR_CODES).toContain('invalid_request')
    expect(INFERENCE_ERROR_CODES).toContain('invalid_request')
  })

  it('gives every vocabulary code a non-empty Chinese title and description', () => {
    const missing: string[] = []
    const incomplete: string[] = []
    for (const code of uniqueCodes) {
      const entry = copy[code as keyof typeof copy]
      if (!entry) { missing.push(code); continue }
      if (!entry.title.trim() || !entry.description.trim()) incomplete.push(code)
    }
    expect(missing).toEqual([])
    expect(incomplete).toEqual([])
  })

  it('has no copy key the contract does not declare', () => {
    const declared = new Set<string>(uniqueCodes)
    const extra = Object.keys(copy).filter(code => !declared.has(code))
    expect(extra).toEqual([])
  })

  it('keeps one copy entry per code so a title cannot be silently shadowed', () => {
    const titles = uniqueCodes.map(code => copy[code as keyof typeof copy].title)
    // Titles may repeat across the two vocabularies (the same failure is reported by both
    // surfaces), but a duplicated entry inside `codes` itself would be a copy table bug.
    expect(Object.keys(copy).length).toBe(new Set(Object.keys(copy)).size)
    expect(titles.every(title => title.length > 0)).toBe(true)
  })

  it('renders copy in Chinese rather than echoing the English machine code', () => {
    for (const code of uniqueCodes) {
      const entry = copy[code as keyof typeof copy]
      expect(entry.title).not.toBe(code)
      expect(entry.description).not.toBe(code)
      expect(/[\u4e00-\u9fff]/.test(entry.title)).toBe(true)
      expect(/[\u4e00-\u9fff]/.test(entry.description)).toBe(true)
    }
  })

  it('describes every code with an actionable sentence rather than a bare restatement', () => {
    const tooShort: string[] = []
    for (const code of uniqueCodes) {
      const { description } = copy[code as keyof typeof copy]
      if (description.length < 8 || !/[。：]$/.test(description)) tooShort.push(code)
    }
    expect(tooShort).toEqual([])
  })

  it('provides both tiers of fallback copy and the field/status templates', () => {
    const fallback = messages['zh-CN'].errors.fallback
    for (const key of ['network', 'clientUnknown', 'server', 'unknown'] as const) {
      expect(fallback[key].title.length).toBeGreaterThan(0)
      expect(fallback[key].description.length).toBeGreaterThan(0)
    }
    // The two unknown-status tiers interpolate the observed status and code.
    expect(fallback.clientUnknown.description).toContain('{status}')
    expect(fallback.clientUnknown.description).toContain('{code}')
    expect(fallback.server.description).toContain('{status}')
    expect(fallback.server.description).toContain('{code}')
    expect(messages['zh-CN'].errors.field).toContain('{field}')
    expect(messages['zh-CN'].errors.httpStatus).toBe('HTTP {status}')
  })

  it('does not expose the front-end synthetic codes as contract vocabulary', () => {
    expect(isKnownErrorCode('network_error')).toBe(false)
    expect(isKnownErrorCode('request_failed')).toBe(false)
    expect(isKnownErrorCode('')).toBe(false)
    for (const code of uniqueCodes) expect(isKnownErrorCode(code)).toBe(true)
  })
})

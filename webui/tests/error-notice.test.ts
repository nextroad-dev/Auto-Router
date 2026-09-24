import { describe, expect, it } from 'vitest'
import { ApiError } from '../src/lib/api'
import { errorMessage, errorNotice, errorNoticeForCode, httpStatusLabel, isKnownErrorCode } from '../src/lib/errors'

describe('errorNotice', () => {
  it('renders a known management code from the copy table', () => {
    const notice = errorNotice(new ApiError(409, 'unknown_pair', 'the requested binding is not registered'))
    expect(notice.status).toBe(409)
    expect(notice.code).toBe('unknown_pair')
    expect(notice.title).toBe('找不到指定绑定')
    expect(notice.description).toContain('刷新列表确认对象是否仍然存在')
    // The server's English message is never shown when the code is known.
    expect(notice.description).not.toContain('the requested binding')
  })

  it('appends the offending field without translating the field path', () => {
    const notice = errorNotice(new ApiError(400, 'invalid_filter', 'bad window', 'window'))
    expect(notice.title).toBe('筛选条件无效')
    expect(notice.description).toContain('（字段：window）')
  })

  it('omits the field suffix when the backend named no field', () => {
    const notice = errorNotice(new ApiError(400, 'invalid_filter', 'bad filter'))
    expect(notice.description).not.toContain('字段：')
  })

  it('renders a forwarding code that also appears on the management surface', () => {
    const notice = errorNotice(new ApiError(422, 'no_eligible_candidate', 'no eligible model is available'))
    expect(notice.title).toBe('没有可用的候选模型')
    expect(notice.description).toContain('检查模型组配置')
  })

  it('grades an unknown code by HTTP status class', () => {
    const network = errorNotice(new ApiError(0, 'network_error', 'unreachable'))
    expect(network.title).toBe('无法连接管理服务')
    expect(network.description).not.toContain('HTTP')

    const client = errorNotice(new ApiError(404, 'weird_code', 'not understood'))
    expect(client.title).toBe('操作未能完成')
    expect(client.description).toContain('HTTP 404')
    expect(client.description).toContain('weird_code')

    const server = errorNotice(new ApiError(503, 'weird_code', 'not understood'))
    expect(server.title).toBe('服务暂时不可用')
    expect(server.description).toContain('HTTP 503')
    expect(server.description).toContain('weird_code')
    expect(server.description).toContain('服务日志')
  })

  it('never lets an unknown code fall back to a bare machine token as the title', () => {
    for (const status of [0, 400, 404, 500, 503, 302]) {
      const notice = errorNotice(new ApiError(status, 'totally_unknown', 'x'))
      expect(notice.title).not.toBe('totally_unknown')
      expect(/[\u4e00-\u9fff]/.test(notice.title)).toBe(true)
    }
  })

  it('treats a non-ApiError Error as a front-end defect and keeps its message', () => {
    const notice = errorNotice(new TypeError('cannot read properties of undefined'))
    expect(notice.code).toBe('internal_error')
    expect(notice.title).toBe('服务内部错误')
    expect(notice.description).toBe('cannot read properties of undefined')
  })

  it('does not throw on values that are not errors at all', () => {
    for (const value of [null, undefined, 0, 'a string', {}, [], Symbol('s')]) {
      const notice = errorNotice(value)
      expect(notice.title.length).toBeGreaterThan(0)
      expect(notice.status).toBe(0)
      expect(notice.code).toBe('request_failed')
    }
  })

  it('falls back to the unknown tier when an Error carries no message', () => {
    const notice = errorNotice(new Error(''))
    expect(notice.code).toBe('internal_error')
    expect(notice.description.length).toBeGreaterThan(0)
  })
})

describe('errorMessage compatibility layer', () => {
  it('returns the title half of the same notice', () => {
    const error = new ApiError(422, 'no_eligible_candidate', 'x')
    expect(errorMessage(error)).toBe(errorNotice(error).title)
    expect(errorMessage(error)).toBe('没有可用的候选模型')
  })

  it('never throws for an unrecognized value', () => {
    expect(errorMessage(undefined)).toBe(errorNotice(undefined).title)
    expect(errorMessage({ odd: true })).toBe(errorNotice({ odd: true }).title)
  })
})

describe('errorNoticeForCode', () => {
  it('is the same rendering used for a thrown ApiError', () => {
    const direct = errorNoticeForCode(409, 'settings_conflict')
    expect(direct).toEqual(errorNotice(new ApiError(409, 'settings_conflict', 'x')))
  })

  it('reports an empty code as the request-level fallback rather than an empty string', () => {
    const notice = errorNoticeForCode(500, '')
    expect(notice.description).toContain('request_failed')
    expect(notice.description).not.toContain('HTTP 500，代码 ）')
  })
})

describe('httpStatusLabel', () => {
  it('renders the localized status prefix', () => {
    expect(httpStatusLabel(429)).toBe('HTTP 429')
  })
})

describe('isKnownErrorCode', () => {
  it('discriminates contract codes from everything else', () => {
    expect(isKnownErrorCode('storage_error')).toBe(true)
    expect(isKnownErrorCode('upstream_timeout')).toBe(true)
    expect(isKnownErrorCode('not_a_code')).toBe(false)
    expect(isKnownErrorCode('__proto__')).toBe(false)
  })
})

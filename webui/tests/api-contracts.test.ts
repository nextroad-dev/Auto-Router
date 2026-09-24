import { describe, expect, it } from 'vitest'
import { createInboundKeyRequest } from '../src/lib/api'

describe('createInboundKeyRequest', () => {
  it('trims the credential name and supplies the required inference scope', () => {
    expect(createInboundKeyRequest(' dashboard-laptop ')).toEqual({
      name: 'dashboard-laptop',
      scopes: ['inference'],
    })
  })
})

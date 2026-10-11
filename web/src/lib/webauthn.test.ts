import { describe, expect, it } from 'vitest';
import {
  assertionToJSON,
  b64urlDecode,
  b64urlEncode,
  friendlyError,
  registrationToJSON,
  toCreationOptions,
  toRequestOptions,
  WebAuthnCancelled,
} from './webauthn.js';

const bytes = (...b: number[]) => new Uint8Array(b);
const buf = (...b: number[]) => bytes(...b).buffer;

describe('base64url', () => {
  it('encodes without padding using the URL alphabet', () => {
    expect(b64urlEncode(bytes())).toBe('');
    expect(b64urlEncode(bytes(0xfb, 0xff))).toBe('-_8');
    expect(b64urlEncode(bytes(0x66))).toBe('Zg');
    expect(b64urlEncode(bytes(0x66, 0x6f))).toBe('Zm8');
    expect(b64urlEncode(bytes(0x66, 0x6f, 0x6f))).toBe('Zm9v');
  });

  it('encodes ArrayBuffers and views over part of a buffer', () => {
    expect(b64urlEncode(buf(0x66, 0x6f))).toBe('Zm8');
    const view = new Uint8Array(buf(1, 0x66, 0x6f, 2), 1, 2);
    expect(b64urlEncode(view)).toBe('Zm8');
  });

  it('decodes with or without padding', () => {
    expect([...b64urlDecode('-_8')]).toEqual([0xfb, 0xff]);
    expect([...b64urlDecode('Zg==')]).toEqual([0x66]);
    expect([...b64urlDecode('')]).toEqual([]);
  });

  it('round-trips every byte value', () => {
    const all = new Uint8Array(256).map((_, i) => i);
    const s = b64urlEncode(all);
    expect(s).toMatch(/^[A-Za-z0-9_-]+$/);
    expect([...b64urlDecode(s)]).toEqual([...all]);
  });

  it('rejects invalid input', () => {
    expect(() => b64urlDecode('a')).toThrow();
    expect(() => b64urlDecode('ab$c')).toThrow();
  });
});

describe('options', () => {
  it('converts creation options', () => {
    const o = toCreationOptions({
      rp: { id: 'example.com', name: 'Files' },
      user: { id: 'AQI', name: 'alice', displayName: 'alice' },
      challenge: 'AwQ',
      pubKeyCredParams: [{ type: 'public-key', alg: -7 }],
      timeout: 60000,
      excludeCredentials: [{ type: 'public-key', id: 'BQY', transports: ['usb'] }],
      authenticatorSelection: { residentKey: 'preferred', userVerification: 'preferred' },
      attestation: 'none',
    });
    expect([...new Uint8Array(o.challenge as ArrayBuffer)]).toEqual([3, 4]);
    expect([...new Uint8Array(o.user.id as ArrayBuffer)]).toEqual([1, 2]);
    expect(o.user.name).toBe('alice');
    expect(o.rp).toEqual({ id: 'example.com', name: 'Files' });
    expect(o.excludeCredentials).toHaveLength(1);
    expect([...new Uint8Array(o.excludeCredentials![0].id as ArrayBuffer)]).toEqual([5, 6]);
    expect(o.excludeCredentials![0].transports).toEqual(['usb']);
    expect(o.authenticatorSelection?.residentKey).toBe('preferred');
    expect(o.timeout).toBe(60000);
  });

  it('converts request options, with or without allowCredentials', () => {
    const o = toRequestOptions({
      challenge: 'AwQ',
      rpId: 'example.com',
      allowCredentials: [{ type: 'public-key', id: 'BQY' }],
      userVerification: 'preferred',
    });
    expect([...new Uint8Array(o.challenge as ArrayBuffer)]).toEqual([3, 4]);
    expect(o.rpId).toBe('example.com');
    expect(o.allowCredentials![0].transports).toBeUndefined();
    expect(toRequestOptions({ challenge: 'AA', allowCredentials: null }).allowCredentials).toEqual(
      []
    );
    expect(toRequestOptions({ challenge: 'AA' }).allowCredentials).toEqual([]);
  });
});

function fakeCredential(response: object, extra: object = {}): PublicKeyCredential {
  return {
    id: 'Bwg',
    rawId: buf(7, 8),
    type: 'public-key',
    authenticatorAttachment: null,
    response,
    getClientExtensionResults: () => ({}),
    ...extra,
  } as unknown as PublicKeyCredential;
}

describe('serializers', () => {
  it('serializes a registration', () => {
    const cred = fakeCredential(
      {
        clientDataJSON: buf(1),
        attestationObject: buf(2, 3),
        getTransports: () => ['usb', 'nfc'],
      },
      {
        authenticatorAttachment: 'cross-platform',
        getClientExtensionResults: () => ({ credProps: { rk: true } }),
      }
    );
    expect(registrationToJSON(cred)).toEqual({
      id: 'Bwg',
      rawId: 'Bwg',
      type: 'public-key',
      authenticatorAttachment: 'cross-platform',
      clientExtensionResults: { credProps: { rk: true } },
      response: { clientDataJSON: 'AQ', attestationObject: 'AgM', transports: ['usb', 'nfc'] },
    });
  });

  it('omits transports and attachment when the browser lacks them', () => {
    const json = registrationToJSON(
      fakeCredential({ clientDataJSON: buf(1), attestationObject: buf(2) })
    );
    expect(json.response.transports).toBeUndefined();
    expect('authenticatorAttachment' in json).toBe(false);
    expect(json.clientExtensionResults).toEqual({});
  });

  it('serializes an assertion', () => {
    const cred = fakeCredential(
      {
        clientDataJSON: buf(1),
        authenticatorData: buf(2),
        signature: buf(3, 4),
        userHandle: buf(9),
      },
      { getClientExtensionResults: () => ({ prf: { results: { first: buf(0xff) } } }) }
    );
    expect(assertionToJSON(cred)).toEqual({
      id: 'Bwg',
      rawId: 'Bwg',
      type: 'public-key',
      clientExtensionResults: { prf: { results: { first: '_w' } } },
      response: {
        clientDataJSON: 'AQ',
        authenticatorData: 'Ag',
        signature: 'AwQ',
        userHandle: 'CQ',
      },
    });
  });

  it('omits a missing userHandle', () => {
    const json = assertionToJSON(
      fakeCredential({
        clientDataJSON: buf(1),
        authenticatorData: buf(2),
        signature: buf(3),
        userHandle: null,
      })
    );
    expect('userHandle' in json.response).toBe(false);
  });
});

describe('friendlyError', () => {
  const domError = (name: string) => Object.assign(new Error('raw'), { name });

  it('maps cancellation and timeouts', () => {
    expect(friendlyError(domError('NotAllowedError'))).toBeInstanceOf(WebAuthnCancelled);
    expect(friendlyError(domError('AbortError')).message).toMatch(/cancelled or timed out/);
  });

  it('explains an already registered key', () => {
    expect(friendlyError(domError('InvalidStateError'), true).message).toMatch(/already/);
  });

  it('passes other errors through', () => {
    const e = new Error('boom');
    expect(friendlyError(e)).toBe(e);
  });
});

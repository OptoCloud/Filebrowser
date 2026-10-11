// WebAuthn (security keys and passkeys) on top of the browser API.
//
// The server sends options as JSON with binary values in base64url, and expects
// the resulting credential back in the WebAuthn Level 3 toJSON() shape. The
// conversion is done here by hand because PublicKeyCredential.parse*FromJSON and
// toJSON() are missing in older browsers (notably Safari before 18).

/** A credential descriptor as JSON (allowCredentials / excludeCredentials). */
export interface CredentialDescriptorJSON {
  type: 'public-key';
  id: string;
  transports?: string[];
}

export interface CreationOptionsJSON {
  rp: { id?: string; name: string };
  user: { id: string; name: string; displayName: string };
  challenge: string;
  pubKeyCredParams: { type: 'public-key'; alg: number }[];
  timeout?: number;
  excludeCredentials?: CredentialDescriptorJSON[] | null;
  authenticatorSelection?: AuthenticatorSelectionCriteria;
  attestation?: AttestationConveyancePreference;
  extensions?: AuthenticationExtensionsClientInputs;
}

export interface RequestOptionsJSON {
  challenge: string;
  timeout?: number;
  rpId?: string;
  allowCredentials?: CredentialDescriptorJSON[] | null;
  userVerification?: UserVerificationRequirement;
  extensions?: AuthenticationExtensionsClientInputs;
}

/** What the server's begin endpoints return. */
export interface Ceremony<T> {
  ceremony: string;
  options: { publicKey: T };
}

export interface RegistrationJSON {
  id: string;
  rawId: string;
  type: 'public-key';
  authenticatorAttachment?: string;
  clientExtensionResults: Record<string, unknown>;
  response: {
    clientDataJSON: string;
    attestationObject: string;
    transports?: string[];
  };
}

export interface AssertionJSON {
  id: string;
  rawId: string;
  type: 'public-key';
  authenticatorAttachment?: string;
  clientExtensionResults: Record<string, unknown>;
  response: {
    clientDataJSON: string;
    authenticatorData: string;
    signature: string;
    userHandle?: string;
  };
}

/** The user dismissed the browser prompt, or it timed out. */
export class WebAuthnCancelled extends Error {
  constructor() {
    super('The security key request was cancelled or timed out. Please try again.');
    this.name = 'WebAuthnCancelled';
  }
}

export function webauthnSupported(): boolean {
  return (
    typeof window !== 'undefined' &&
    typeof window.PublicKeyCredential === 'function' &&
    typeof navigator.credentials?.create === 'function' &&
    typeof navigator.credentials?.get === 'function'
  );
}

/** Encodes bytes as base64url without padding. */
export function b64urlEncode(data: ArrayBuffer | ArrayBufferView): string {
  const bytes =
    data instanceof ArrayBuffer
      ? new Uint8Array(data)
      : new Uint8Array(data.buffer, data.byteOffset, data.byteLength);
  let bin = '';
  for (let i = 0; i < bytes.length; i++) bin += String.fromCharCode(bytes[i]);
  return btoa(bin).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

/** Decodes base64url (padding optional). Throws on invalid input. */
export function b64urlDecode(s: string): Uint8Array<ArrayBuffer> {
  let b64 = s.replace(/-/g, '+').replace(/_/g, '/').replace(/=+$/, '');
  if (b64.length % 4 === 1 || /[^A-Za-z0-9+/]/.test(b64)) throw new Error('invalid base64url');
  b64 += '='.repeat((4 - (b64.length % 4)) % 4);
  const bin = atob(b64);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function descriptor(d: CredentialDescriptorJSON): PublicKeyCredentialDescriptor {
  const out: PublicKeyCredentialDescriptor = { type: d.type, id: b64urlDecode(d.id) };
  if (d.transports?.length) out.transports = d.transports as AuthenticatorTransport[];
  return out;
}

export function toCreationOptions(o: CreationOptionsJSON): PublicKeyCredentialCreationOptions {
  return {
    ...o,
    challenge: b64urlDecode(o.challenge),
    user: { ...o.user, id: b64urlDecode(o.user.id) },
    excludeCredentials: (o.excludeCredentials ?? []).map(descriptor),
  };
}

export function toRequestOptions(o: RequestOptionsJSON): PublicKeyCredentialRequestOptions {
  const out: PublicKeyCredentialRequestOptions = {
    ...o,
    challenge: b64urlDecode(o.challenge),
    allowCredentials: (o.allowCredentials ?? []).map(descriptor),
  };
  return out;
}

/** Converts binary values in extension results (e.g. prf) to base64url. */
function extensionsToJSON(v: unknown): unknown {
  if (v instanceof ArrayBuffer || ArrayBuffer.isView(v)) return b64urlEncode(v);
  if (Array.isArray(v)) return v.map(extensionsToJSON);
  if (v && typeof v === 'object') {
    const out: Record<string, unknown> = {};
    for (const [k, x] of Object.entries(v)) out[k] = extensionsToJSON(x);
    return out;
  }
  return v;
}

function common(cred: PublicKeyCredential) {
  const out: {
    id: string;
    rawId: string;
    type: 'public-key';
    authenticatorAttachment?: string;
    clientExtensionResults: Record<string, unknown>;
  } = {
    id: cred.id,
    rawId: b64urlEncode(cred.rawId),
    type: 'public-key',
    clientExtensionResults: (extensionsToJSON(cred.getClientExtensionResults?.() ?? {}) ??
      {}) as Record<string, unknown>,
  };
  if (cred.authenticatorAttachment) out.authenticatorAttachment = cred.authenticatorAttachment;
  return out;
}

export function registrationToJSON(cred: PublicKeyCredential): RegistrationJSON {
  const r = cred.response as AuthenticatorAttestationResponse;
  const response: RegistrationJSON['response'] = {
    clientDataJSON: b64urlEncode(r.clientDataJSON),
    attestationObject: b64urlEncode(r.attestationObject),
  };
  // getTransports() is missing in some older browsers.
  const transports = typeof r.getTransports === 'function' ? r.getTransports() : [];
  if (transports.length) response.transports = transports;
  return { ...common(cred), response };
}

export function assertionToJSON(cred: PublicKeyCredential): AssertionJSON {
  const r = cred.response as AuthenticatorAssertionResponse;
  const response: AssertionJSON['response'] = {
    clientDataJSON: b64urlEncode(r.clientDataJSON),
    authenticatorData: b64urlEncode(r.authenticatorData),
    signature: b64urlEncode(r.signature),
  };
  if (r.userHandle && r.userHandle.byteLength) response.userHandle = b64urlEncode(r.userHandle);
  return { ...common(cred), response };
}

/** Maps a DOMException from the browser to a friendly error. */
export function friendlyError(err: unknown, registering = false): Error {
  const name = err instanceof Error ? err.name : '';
  switch (name) {
    case 'NotAllowedError':
    case 'AbortError':
      return new WebAuthnCancelled();
    case 'InvalidStateError':
      return new Error(
        registering
          ? 'This security key is already registered to your account.'
          : 'This security key cannot be used here.'
      );
    case 'SecurityError':
      return new Error(
        'Security keys cannot be used on this address. The site must be served over HTTPS on the configured domain.'
      );
    case 'NotSupportedError':
      return new Error('This browser or security key does not support the requested options.');
  }
  return err instanceof Error ? err : new Error(String(err));
}

/** Registers a new credential; returns it in the JSON shape the server expects. */
export async function createCredential(
  options: { publicKey: CreationOptionsJSON },
  signal?: AbortSignal
): Promise<RegistrationJSON> {
  if (!webauthnSupported()) throw new Error('This browser does not support security keys.');
  let cred: Credential | null;
  try {
    cred = await navigator.credentials.create({
      publicKey: toCreationOptions(options.publicKey),
      signal,
    });
  } catch (e) {
    throw friendlyError(e, true);
  }
  if (!cred) throw new WebAuthnCancelled();
  return registrationToJSON(cred as PublicKeyCredential);
}

/** Asks for an assertion; returns it in the JSON shape the server expects. */
export async function getCredential(
  options: { publicKey: RequestOptionsJSON },
  signal?: AbortSignal
): Promise<AssertionJSON> {
  if (!webauthnSupported()) throw new Error('This browser does not support security keys.');
  let cred: Credential | null;
  try {
    cred = await navigator.credentials.get({
      publicKey: toRequestOptions(options.publicKey),
      signal,
    });
  } catch (e) {
    throw friendlyError(e);
  }
  if (!cred) throw new WebAuthnCancelled();
  return assertionToJSON(cred as PublicKeyCredential);
}

import crypto from "node:crypto";

// RFC 4648 base32 decode (no padding) — the manual secret is uppercase
// A-Z2-7, exactly what real authenticator apps decode.
export function base32Decode(encoded: string): Buffer {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  const clean = encoded.replace(/=+$/, "").toUpperCase();
  let bits = "";
  for (const char of clean) {
    const val = alphabet.indexOf(char);
    if (val === -1) continue;
    bits += val.toString(2).padStart(5, "0");
  }
  const bytes: number[] = [];
  for (let i = 0; i + 8 <= bits.length; i += 8) {
    bytes.push(parseInt(bits.substring(i, i + 8), 2));
  }
  return Buffer.from(bytes);
}

// RFC 6238 TOTP (HMAC-SHA1, 30s step, 6 digits) — matches the
// `algorithm=SHA1&digits=6&period=30` params in the otpauth URI the
// backend returns.
//
// The server accepts each code once (a code at or before the last accepted
// time step is refused), so a test that confirms enrollment and then signs in
// must confirm with the previous step (stepOffset -1, inside the server's
// +/-1 step skew) and sign in with the current one.
export function generateTotp(secret: string, stepOffset = 0): string {
  const key = base32Decode(secret);
  const counter = Math.floor(Date.now() / 1000 / 30) + stepOffset;
  const counterBuf = Buffer.alloc(8);
  counterBuf.writeBigUInt64BE(BigInt(counter));
  const hmac = crypto.createHmac("sha1", key).update(counterBuf).digest();
  const offset = hmac[hmac.length - 1] & 0xf;
  const code =
    ((hmac[offset] & 0x7f) << 24) |
    ((hmac[offset + 1] & 0xff) << 16) |
    ((hmac[offset + 2] & 0xff) << 8) |
    (hmac[offset + 3] & 0xff);
  return (code % 1_000_000).toString().padStart(6, "0");
}

---
model: opus
effort: high
---

# Support VeNCrypt (TLS) and Apple ARD authentication in the `vnc` check

## Problem
After spec `2026-09-30-05`, the `vnc` check only authenticates with classic VNC auth (security type 2). Two common setups offer something else and would fail as "auth type unsupported":
- **VeNCrypt (type 19)**: TigerVNC defaults, libvirt/QEMU, many Linux distros. Wraps auth in TLS and supports username + password (`Plain`).
- **Apple Remote Desktop (type 30)**: macOS Screen Sharing. Diffie-Hellman key agreement, then AES-128-ECB encrypted username + password.

Depends on spec `2026-09-30-05`. The JS `vnc.connect()` from spec `2026-09-30-06` should accept the same new options once both are in.

## Proposal
1. `server/internal/checkers/checkvnc/config/`: add `username` (used by VeNCrypt Plain and ARD) and `tlsVerify` (verify the VeNCrypt X.509 certificate, default per open question below), plus certificate expiry thresholds reusing the SSL-style grading that `checkrdp` does for its TLS certificate (`server/internal/checkers/checkrdp/config/config.go:177` `readThresholds`).
2. Security-type selection in `checkvnc/protocol.go`: pick the strongest type both sides support that the configured credentials can satisfy. Order: VeNCrypt X509* > VeNCrypt TLS* > ARD > VNC auth > None. Report the chosen type in the output.
3. VeNCrypt (`checkvnc/vencrypt.go`):
   - version exchange (0.2), sub-type list, pick X509Plain (262) / X509Vnc (261) / X509None (260), then TLSPlain (259) / TLSVnc (258) / TLSNone (257);
   - `crypto/tls` upgrade of the existing conn; for X509 subtypes, expose the certificate (subject, issuer, expiry) in the output and grade expiry; anonymous-TLS subtypes (TLS*) need anonymous DH ciphers Go does not support, so report them as unsupported with a clear message;
   - after TLS: Plain = `uint32 userLen, uint32 passLen, user, pass`; Vnc = the type-2 DES challenge from spec 05.
4. Apple ARD (`checkvnc/ard.go`): read generator (2 bytes), key length (2 bytes), prime and server public key; generate a DH key pair with `math/big`; shared secret, MD5 of it = AES-128 key; encrypt a 128-byte block (username in the first 64 bytes, password in the last 64, each null-terminated and padded with random bytes) with AES-ECB; send ciphertext + client public key; read SecurityResult.
5. Output: `securityType` chosen, `vencryptSubtype`, TLS certificate details when present.
6. dash0: `username` and `tlsVerify` fields, locale keys in every locale. Docs: extend the `vnc` section of `web/docs/docs/features/check-types.md` with a table of supported and unsupported auth types (RealVNC RA2 = unsupported, proprietary). CHANGELOG entry.

## Tests
- `checkvnc/vencrypt_test.go` (fake server with a self-signed cert from `crypto/tls` test helpers):
  - X509Plain happy path: server receives the exact username/password bytes after the TLS upgrade;
  - `tlsVerify: true` with an untrusted self-signed cert gives down; `tlsVerify: false` with the same server gives up (positive control);
  - expiring certificate within the warning threshold gives the warning state;
  - server offering only TLSNone/TLSVnc (anonymous TLS) gives the unsupported message, not a panic or hang;
  - wrong password gives `AUTH_FAILED` with the server reason.
- `checkvnc/ard_test.go`:
  - fixed DH parameters + fixed private key + fixed padding RNG give a known ciphertext (golden vector computed once and checked in);
  - fake server decrypts the block and checks username/password; a wrong password gives `AUTH_FAILED`;
  - username longer than 63 bytes is rejected at validation.
- `checkvnc/protocol_test.go`: type selection picks VeNCrypt over VNC auth when both are offered, falls back to VNC auth when no username is set and only VeNCrypt Plain would need one.
- `slowtests` live test: TigerVNC container configured with `-SecurityTypes X509Plain` and a PAM/`-PlainUsers` user. ARD has no container server, so it stays covered by the golden vector and the fake server only.

## To verify
- Whether spec 05 landed a shared TLS-certificate grading helper usable here, or whether `checkrdp`'s logic needs to be extracted into a common package first.
- The exact ARD padding and DH byte layout, against a reference client (e.g. noVNC's `ra2`/`ard` code or libvncclient) before writing the golden vector.

## Open questions
- **Default for `tlsVerify`?** Recommended: `false`. VeNCrypt certificates are almost always self-signed (TigerVNC generates one), so `true` would make most checks fail out of the box. Report the certificate either way and grade its expiry.

## Resolved open questions
- `tlsVerify` defaults to `false`. Report the certificate either way and grade its expiry.

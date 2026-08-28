---
model: sonnet
effort: medium
---

# The SFTP checker cannot authenticate against servers that only offer keyboard-interactive SSH auth

## Problem

When an SFTP check is configured with a password, the checker builds its SSH
auth list with **only** `ssh.Password(cfg.Password)`
(`server/internal/checkers/checksftp/checker.go:82-85`). Go's `x/crypto/ssh`
client never *attempts* an auth method the server does not advertise in its
"authentications that can continue" list — so against a server that offers only
`keyboard-interactive` (a very common configuration: sshd's
`ChallengeResponseAuthentication` wraps password auth this way), authentication
fails having attempted nothing but `none`:

```
SSH connection failed: ssh: handshake failed: ssh: unable to authenticate,
attempted methods [none], no supported methods remain
```

This is not hypothetical: on 2026-08-25 ~00:15 UTC, `test.rebex.net` (the
public SFTP test server) stopped advertising `password` and now offers only
`keyboard-interactive` (verified with `ssh -v demo@test.rebex.net` →
`Authentications that can continue: keyboard-interactive`). The prod
`sftp-test-rebex` check in org `default` has failed every 5 minutes since,
from every region — while the sibling `ftp-test-rebex` check against the same
host stays green, because FTP auth is unaffected. The stored encrypted
credential is intact and correctly merged at dispatch; the failure is purely
the missing client-side auth method.

Every OpenSSH client handles this transparently by answering
keyboard-interactive prompts with the password, so from a user's point of view
"my password works with `sftp` but SolidPing says down" is a checker bug, not
a target outage.

## Proposal

1. **`checksftp`**: when `cfg.Password != ""`, append *both* methods:
   `ssh.Password(cfg.Password)` **and** `ssh.KeyboardInteractive(...)` whose
   challenge callback answers every prompt with the password (return one
   answer per question; return an empty slice for zero questions). This is
   the standard OpenSSH-equivalent behavior. Keep the existing
   `private_key` branch unchanged.

2. **Sweep other SSH clients in the repo** for the same password-only auth
   list and apply the identical fix where a password is used:
   - `server/internal/integrations/sshtunnel/sshtunnel.go`
   - any other `ssh.ClientConfig` construction found via
     `grep -rn "ssh.Password" server/internal`.

3. **Tests** (`testify/require`, `t.Parallel()` per `server/CLAUDE.md`):
   - a unit test with an in-process `x/crypto/ssh` server whose
     `ServerConfig` sets **only** `KeyboardInteractiveCallback` (no
     `PasswordCallback`), asserting the checker (and the sshtunnel dial, if
     practical) authenticates successfully with a configured password;
   - a companion test against a password-only server config as a positive
     control that the existing path still works;
   - a negative test: wrong password fails against the
     keyboard-interactive-only server (no silent success).

The in-process SSH server does not need a real SFTP subsystem for the
sshtunnel test; for the checker test, either wire the minimal sftp subsystem
(`github.com/pkg/sftp` server side, already an indirect dependency of the
checker) or, if that proves heavy, factor the auth-method construction into a
small helper (`buildAuthMethods(cfg)`) and test authentication at the SSH
layer while covering the helper's method list directly.

Out of scope: supporting servers that use keyboard-interactive for real
multi-factor challenges (answering anything other than a password-style
prompt), and any dashboard/UI change.

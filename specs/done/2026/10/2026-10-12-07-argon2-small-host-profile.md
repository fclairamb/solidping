---
model: opus     # changes the default password-hashing cost: a subtle mistake weakens stored hashes or makes logins churn
effort: medium
---

# On small hosts, default to the OWASP minimum argon2id profile instead of 64 MiB per derivation

## Problem

Every argon2id derivation (login, password change, signup) allocates the configured argon2 memory: 64 MiB with the default profile `m=65536 KiB, t=3, p=4` (`server/internal/config/config.go:2031-2036`, `server/internal/utils/passwords/policy.go:44-46`). `maxConcurrentHashes = 4` (`server/internal/utils/passwords/argon2id.go:28`) caps the peak at about 256 MiB. Spec 2026-10-12-04 made that memory go back to the OS a second after the burst, but the peak itself stays: on a 1 GiB Raspberry Pi or a 1 GiB VPS, 256 MiB is a quarter of the machine for a few hundred milliseconds, and it pushes against the host-RAM soft cap that the same spec added (`memlimit.memLimitFromHost`, `server/internal/memlimit/memlimit.go:163`, half of a host with 2 GiB or less).

The docs already describe a lighter profile, the OWASP minimum `m=19456 KiB, t=2, p=1` (`web/docs/docs/installation/linux.md:313`), but only operators who read that section set it. The peak with that profile is about 76 MiB (4 × 19 MiB).

The fix is **not** to switch the default algorithm to bcrypt. argon2id is memory-hard, so a stolen hash costs an attacker GPU/ASIC memory, and bcrypt (4 KiB) does not. OWASP ranks argon2id first and bcrypt for legacy systems. The algorithm stays argon2id. Only its default cost changes, and only on small hosts.

## Proposal

1. **Know which argon2 fields the operator set.** Today `config.Load` fills the 64 MiB defaults (`config.go:2031-2036`) before `applyPasswordHashingEnv` (`config.go:2483-2486`) overlays `SP_AUTH_PASSWORD_ARGON2_*`, and the systemconfig overlay re-applies env then DB-stored `auth.password.*` parameters (comment at `config.go:2424-2440`, `app.reResolvePasswordPolicy` around `server/internal/app/server.go:4355`). So a resolved policy cannot tell "default 64 MiB" from "operator chose 64 MiB". Record it: an `Argon2Explicit bool` on `PasswordConfig` (or a per-field set, whichever is simpler), set when any of `memory`, `time`, `threads` comes from a config file, an `SP_AUTH_PASSWORD_ARGON2_*` variable, or a DB-stored `auth.password.argon2.*` parameter. `SP_AUTH_PASSWORD_ALGORITHM=bcrypt` keeps working as today and is untouched by this spec.
2. **Expose the small-host decision from `memlimit`.** Add an exported `memlimit.SmallHost() bool` (or reuse the value `Apply` already computes, `memlimit.go:105`) that answers with the same threshold as the soft cap, `smallHostMaxBytes = 2 GiB` (`memlimit.go:31`), measured on the **effective** memory budget: the cgroup limit when one is set, else host `MemTotal` (`host_linux.go:22`, no-op on other OSes via `cgroup_other.go:14`). One function, so the soft cap and the hashing profile never disagree about what "small" means.
3. **Pick the profile in `PolicyFromConfig`** (`server/internal/utils/passwords/policy.go:91`). When the algorithm is argon2id, `Argon2Explicit` is false, the host is small and the auto-profile is enabled (step 5), use `Memory: 19456, Time: 2, Threads: 1` and keep the default `KeyLength`/`SaltLength`. Otherwise keep today's resolution (`resolveArgon2Params`, `policy.go:~140`). Pass the small-host answer in (a parameter or an option), so the function stays pure and testable. Both call sites (`server.go:278` at boot, `server.go:4355` after the overlay) get the same answer.
4. **Log the choice once at boot**, at INFO: `password hashing: argon2id small-host profile m=19456 t=2 p=1 (host memory <n> MiB <= 2048 MiB; set SP_AUTH_PASSWORD_ARGON2_* to override)`. No log on a normal host.
5. **Opt-out.** New `SP_AUTH_PASSWORD_ARGON2_AUTO_PROFILE` (bool, default `true`), read in `applyPasswordHashingEnv` and in the systemconfig overlay like its siblings. `false` keeps 64 MiB everywhere. Setting any `SP_AUTH_PASSWORD_ARGON2_*` cost field already disables the auto-profile (step 1).
6. **Existing hashes.** No migration. `NeedsRehash`/`ShouldRehash` (`policy.go:~130`) already compare stored parameters with the active policy, so a 64 MiB hash is re-minted at 19 MiB on the next successful login, and stops costing 64 MiB on verify. `rehash_on_login: false` keeps them as they are, as today.
7. **Docs.**
   - `web/docs/docs/installation/linux.md` "Small hosts" section: the OWASP profile is now the default at 2 GiB or less. Keep the security caution, and say how to force 64 MiB back (`SP_AUTH_PASSWORD_ARGON2_AUTO_PROFILE=false` or explicit cost variables).
   - `web/docs/docs/configuration/authentication.md` (or wherever `SP_AUTH_PASSWORD_ARGON2_*` is documented): the new variable and the rule.
   - `wiki/runbooks/memory-profiling.md`: one line in §9 with the measured login peak before/after on a small host.

## Tests

- `server/internal/utils/passwords/policy_test.go`, table-driven `PolicyFromConfig`:
  - small host, nothing explicit → `m=19456, t=2, p=1`;
  - large host, nothing explicit → `m=65536, t=3, p=4` (negative control);
  - small host, `Argon2Explicit` with `m=65536` → 65536 kept (an operator choice is never overridden);
  - small host, auto-profile disabled → 65536;
  - small host, algorithm `bcrypt` → bcrypt policy, argon2 untouched.
- Same file: a 64 MiB hash `NeedsRehash` under the small-host policy, a 19 MiB hash does not; under the large-host policy the reverse.
- `server/internal/config/config_test.go`: `Argon2Explicit` is false after a plain `Load`, true when `SP_AUTH_PASSWORD_ARGON2_MEMORY`, `_TIME` or `_THREADS` is set, and true when the config file sets `auth.password.argon2.memory`.
- systemconfig overlay test (next to the existing `auth.password.*` overlay tests): a DB-stored `auth.password.argon2.memory` sets `Argon2Explicit` and wins over the auto-profile.
- `server/internal/memlimit/memlimit_test.go`, `SmallHost` rows: 1 GiB host → true, 2 GiB → true, 8 GiB → false, 8 GiB host with a 512 MiB cgroup limit → true, non-Linux → false.
- Login rehash (in `server/internal/handlers/auth`, next to the `maybeRehashPassword` tests): with the small-host policy active, a user stored at 64 MiB logs in and the stored hash now carries `m=19456`.

## To verify

- The exact DB parameter keys for the argon2 cost fields in systemconfig (`auth.password.argon2.memory`, ...) and where the overlay sets them, so step 1 marks them explicit.
- Whether `memlimit.Apply` runs before `NewServer` builds the password policy (`server.go:278`). If not, compute `SmallHost()` directly rather than reading the applied result.
- Where `SP_AUTH_PASSWORD_ARGON2_*` is documented in `web/docs/docs/configuration/`.

## Resolved open questions

**Decision (user, 2026-10-12): adopt the recommended answer of every question below. Implement exactly those recommendations.**

1. **Multi-node deployments.** The decision is per process. In a deployment where one API node is small and another large, a user's hash would be rehashed back and forth between 19 MiB and 64 MiB on each login, depending on which node answers. **Recommended answer:** apply the auto-profile only when the process runs role `all` (single-process deployment), the same rule #539 used for the worker-hostname warning. Split-role and multi-node deployments keep 64 MiB unless they set the cost variables.
2. **Threshold.** **Recommended answer:** reuse `smallHostMaxBytes` (2 GiB) as is, so there is one definition of "small host" in the codebase.

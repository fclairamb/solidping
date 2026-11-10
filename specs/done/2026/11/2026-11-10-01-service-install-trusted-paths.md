---
model: opus
effort: high
---

# Service install: refuse a binary or data folder path that a non-admin can change

## Problem

Found by the security scan of 2026-11-10 (findings AI1, AI2, AI3).

`solidping service install` registers a privileged service: LocalSystem on Windows, root on macOS (daemon), the
`solidping` system user on Linux. The service runs two things that it never checks: the executable it was installed
from, and the data folder it `chdir`s into. In the data folder it reads `config.yml`, where `plugins.dir` makes the
server start executables (`server/internal/plugins/loader.go:67`, `exec.go:137`). #817 (`b1832b5a`) closed the main
Windows case: a data folder that already exists and that someone else controls. Three gaps remain.

### AI1 — Medium — the service binary is never checked

- **Who can trigger it:** a local unprivileged user on the host. No SolidPing account needed.
- **Precondition:** the admin installs from a binary in a folder that non-admins can write to.
- **Windows** (`server/cmd_service_windows.go:48-75`, `serviceInstall`):
  - The service registers `os.Executable()` as is.
  - On a default Windows install, `C:\` grants Authenticated Users an inherit-only Modify right on every new
    subfolder. Any folder created directly under `C:\` (a common choice, e.g. `C:\solidping\`) is therefore writable
    by every local user.
  - The docs tell the admin to extract the archive into the current folder (`web/docs/docs/installation/windows.md:36-40`).
    The service then "runs the `solidping.exe` that installed it" (`:154`). The update section uses
    `C:\Program Files\SolidPing` (`:283`), so the docs do not even agree on where the binary lives.
  - A local user who replaces the binary gets code execution as LocalSystem at the next start. Recovery actions restart
    the service after a failure (`setServiceRecovery`, `:101`), so that start comes soon.
- **Linux** (`server/cmd_service_linux.go:116,152`):
  - The `ExecStart=` binary is only tested for whether others can *execute* it (`othersCanExecute`, `:232-250`).
    Nobody checks who can *write* it, or its folders.
  - A binary in `/home/alice/bin` or in a group-writable folder lets that user or group run code as the `solidping`
    service user, with `CAP_NET_RAW`.
- **macOS** (`server/cmd_service_darwin.go`, daemon when run with sudo):
  - The plist runs `os.Executable()` as root.
  - A Homebrew binary is owned by the user who installed Homebrew, not by root. So `sudo solidping service install` run
    from a Homebrew binary turns any process of that user into root, without a password prompt.
- **Impact:** local privilege escalation to the service account: SYSTEM on Windows, root on macOS.
- **Why Medium and not High:** it needs the admin to install from a writable location. The Windows data folder (AH1)
  was vulnerable on the default path.

### AI2 — Low — Windows: only the data folder itself is checked, not its parents

- **Where:** `server/cmd_service_windows.go:120-155` (`createServiceDir`).
- **What is checked:** `checkServiceDir` checks the leaf folder and what is inside it.
- **What is not checked:**
  - The parents are created with `os.MkdirAll` (`:132`) and are never checked.
  - `service run` does not check the folder again (`serviceRun` `:443`, `enterServiceDir` in `server/cmd_service_run.go:25`).
- **Who can trigger it:** a local user who created or owns a parent of a custom nested `--dir`, e.g.
  `C:\ProgramData\acme\SolidPing` with `C:\ProgramData\acme` created by that user beforehand. The owner of a parent has
  delete-child rights on it.
- **Attack:** after the install, that user can remove the protected leaf folder and create their own in its place. The
  LocalSystem service then starts in it and reads that user's `config.yml`. That is the AH1 impact again.
- **Default path not affected:** `C:\ProgramData\SolidPing` has a SYSTEM-owned parent.

### AI3 — Low — Linux and macOS: a data folder that already exists is used as is

- **Linux** (`server/cmd_service_linux.go:254-263`, `createSystemdServiceDir`):
  - If the folder already exists and the service user does not own it, install only prints a warning.
  - Nothing is checked when `owner == -1` (user unit), when the folder is a symlink (`os.Stat` follows it), or when it
    is group- or world-writable.
- **macOS** (`server/cmd_service_darwin.go:141-163`): leaves any existing folder as is, and the daemon runs as root.
- **Who can trigger it:** a local user who created the custom `--dir` the admin then picks (a shared volume, a folder
  under a writable parent).
- **Impact:** that user reads the instance's secrets and can plant `config.yml`. That is code execution through
  `plugins.dir`: as root on macOS, as `solidping` on Linux.
- **Default paths not affected:** `/var/lib/solidping` and `/Library/Application Support/SolidPing` can only be created
  by root.
- **Why it matters anyway:** the behaviour differs from Windows, which refuses such a folder since #817.

## Proposal

1. Add one check per OS, `checkTrustedPath(path, trust)`. It walks `path` and every parent up to the root, without
   following links (on Linux and macOS, `Lstat` each part; refuse a symlink in any part except a trusted owner's). Each
   part must be owned by a trusted account and must not be writable by anyone else:
   - **Linux and macOS:** each part is owned by root (or the service user, for the data folder only), with no group or
     other write bit. The sticky bit does not make a parent acceptable.
   - **Windows:** reuse `checkServiceDirEntry` (`server/cmd_service_windows.go:229`). Owner in
     SYSTEM/Administrators/TrustedInstaller/the installer. No allow-ACE granting a write right (`FILE_WRITE_DATA`,
     `FILE_APPEND_DATA`, `FILE_ADD_SUBDIRECTORY`, `FILE_DELETE_CHILD`, `WRITE_DAC`, `WRITE_OWNER`, `DELETE`,
     `GENERIC_WRITE`/`GENERIC_ALL`) to any other SID. Today `checkServiceDirDescriptor` refuses any allow-ACE to an
     untrusted SID. For parents and the binary, refuse only write-type rights: `Users` read/execute on `C:\Program Files`
     is fine. Ignore inherit-only ACEs on the object being checked.
2. On every OS, `serviceInstall` runs the check on:
   - the executable, after `filepath.EvalSymlinks(os.Executable())`, and every folder above it;
   - the data folder's parents.
   If a check fails, install refuses with an actionable message, e.g. "move solidping.exe to C:\Program Files\SolidPing
   (or /usr/local/bin), then install again". There is no `--force`.
3. Linux (`createSystemdServiceDir`) and macOS (`createServiceDir`) apply the Windows rule to an existing data folder.
   They accept it only if it meets all of these:
   - it is a real folder, not a symlink;
   - it is owned by the service account (root for a macOS daemon, `solidping` or root for a Linux system unit, the user
     for a user unit);
   - it has no group or other write bit;
   - nothing inside is a symlink or owned by someone else.
   Otherwise refuse, with the same "delete it or choose another `--dir`" message as Windows.
4. `service run` checks the data folder and its parents again, on all three OSes, before it `chdir`s. A failure exits
   with an error that the service manager logs. Check the executable again at run too, cheaply (on Windows with the
   same handle-based check).
5. Docs:
   - `web/docs/docs/installation/windows.md`: extract into `C:\Program Files\SolidPing` in the install steps too, not
     only in the update section, and say why.
   - `linux.md` and `macos.md`: install the binary root-owned (e.g. `/usr/local/bin` with `sudo install -o root -m 0755`),
     and say that a Homebrew binary cannot back a root daemon.
6. User units and launchd agents (non-root install) keep trusting the user's own files. Only check that no *other*
   account can write the binary or the folder.

## Tests

- `server/cmd_service_linux_test.go` (and a darwin twin; these run in CI on Linux and macOS):
  - Binary in a temp folder with mode `0777`, or owned by a non-root uid when the test runs as root: install refuses.
    This fails before the fix.
  - Binary in a root-owned `0755` chain: install succeeds (positive control).
  - Existing data folder: install refuses it when it is a symlink, when it is `0770`/`0777`, or when it is owned by
    another uid. Each case fails before the fix. A `0700` folder owned by the service user is accepted, so a reinstall
    after `uninstall` still works (positive control).
  - `service run` refuses a data folder that became world-writable after the install.
  - Use the existing `chownPath`/`lookupUser` seams where root is not available, so the tests run unprivileged.
- `server/cmd_service_windows_test.go` (run on the CI Windows job):
  - A binary in a folder with an inheritable `Authenticated Users` Modify ACE: install refuses. This fails before the fix.
  - A binary under a SYSTEM/Administrators-only folder: accepted (positive control).
  - `--dir` under a parent owned by a non-admin SID (built with an explicit owner SID and a DACL): refused. This fails
    before the fix.
  - `service run` re-checks: a data folder whose DACL was opened after the install is refused.
  - The existing `TestServiceDirReusesPreviousInstall` still passes.

## To verify

- Whether `checkServiceDirEntry` can be made to take a "write rights only" mode without weakening the leaf check, which
  must keep refusing any grant to an untrusted SID.
- How the winget package installs `solidping.exe` (user scope under `%LOCALAPPDATA%` or machine scope under
  `Program Files`). A user-scope winget binary sits in the admin's own profile, so the check should accept it when the
  only other writer is the installing admin.

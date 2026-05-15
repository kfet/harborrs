---
name: update
description: Update harborrs on a single host to the latest released version and restart its supervisor (systemd or launchd) so the new binary is live. Verifies on a side-by-side test unit if one exists.
---

# Update Skill

Upgrade `harborrs` on **one** host (local or remote) and restart the
supervisor. Use after a release publishes or when a specific host is
stale.

> Releasing lives in `.fir/skills/release/SKILL.md` (the kfet stdlib-Go
> release flow). For multi-host rollouts, repeat this skill per host.

## Inputs

Confirm with the user before acting:

1. **Host** — `local` or `user@host`. Default to local if omitted.
2. **Target version** — default: latest `vX.Y.Z` tag on `origin`.
   Override only if the user asks (e.g. pinning a known-good version).

## Steps

### 1. Determine target version

```bash
git fetch --tags origin
git tag --sort=-v:refname | head -1
```

If `VERSION` (the file at the repo root) is ahead of every pushed tag,
an unpublished release exists — stop and run the `release` skill first.

### 2. Probe the host

Detect installed version, install method, and supervisor. For remote
prefix every command with `ssh <host>`; for local run directly.

```bash
harborrs version 2>/dev/null || echo not-installed
brew list --versions harborrs 2>/dev/null         # brew install?
ls -l ~/.local/bin/harborrs /usr/local/bin/harborrs 2>/dev/null  # install.sh / hotfix?
systemctl --user is-active harborrs 2>/dev/null   # Linux supervisor
launchctl list 2>/dev/null | grep -i harborrs     # macOS supervisor
ls ~/Library/LaunchAgents/dev.*.harborrs.plist 2>/dev/null
```

Note the supervisor label exactly — it embeds the deploying user
(`dev.<user>.harborrs`). If installed version already equals target,
tell the user and stop unless they want a forced restart.

### 3. Pick the upgrade path

Pick the path that matches the **install method actually pinned by the
running supervisor's `ExecStart`**. Mixed installs are common; trust
the unit, not `which harborrs`.

**(a) `harborrs update` — selfupdate, the default path:**

```bash
ssh <host> 'harborrs update -check'        # prints current vs latest
ssh <host> 'harborrs update'               # downloads + atomic-replaces the binary
```

The `selfupdate` package writes to the same path as the running
binary. After a successful update, restart the supervisor (the running
process still holds the old binary in memory):

```bash
# Linux
ssh <host> 'systemctl --user restart harborrs'

# macOS
ssh <host> "launchctl kickstart -k gui/\$UID/dev.<user>.harborrs"
```

If `harborrs update` fails because the binary lives in a path the user
can't write (e.g. `/usr/local/bin` on a locked-down box), fall back to
`install.sh` with `PREFIX=$HOME/.local` or to `brew upgrade`.

**(b) Brew (typical macOS):**

```bash
ssh <host> 'brew update && brew upgrade kfet/harborrs/harborrs'
ssh <host> "launchctl kickstart -k gui/\$UID/dev.<user>.harborrs"
```

If `brew upgrade` reports "already up-to-date" but the version still
lags, the tap index is stale — re-run `brew update`. Persistent miss
→ fall back to selfupdate.

**(c) install.sh re-run (Linux, hotfix):**

```bash
ssh <host> 'curl -fsSL https://raw.githubusercontent.com/kfet/harborrs/main/install.sh | sh'
ssh <host> 'systemctl --user restart harborrs'
```

**(d) Direct deploy from the dev tree (cross-build hotfix):**

```bash
make build-linux-arm64               # or matching host arch
scp harborrs-linux-arm64 <host>:~/.local/bin/harborrs
ssh <host> 'systemctl --user restart harborrs'
```

### 4. Optional: side-by-side verify on the test unit

If a `harborrs-test` unit (from the deploy skill) is still installed,
upgrade and restart **it first**, exercise it, then upgrade prod:

```bash
ssh <host> 'systemctl --user restart harborrs-test'
curl -sf https://<host>.<tailnet>.ts.net/rss-test/accounts/ClientLogin \
  -X POST -d 'Email=<user>&Passwd=<test-password>' | head
```

Only proceed to prod if the test unit comes back clean (UI 200, login
returns `SID=`/`Auth=`). This catches schema migrations or storage
incompatibilities that would otherwise corrupt prod data.

### 5. Verify prod

```bash
ssh <host> 'harborrs version'                            # equals target
ssh <host> 'systemctl --user is-active harborrs'         # → active   (Linux)
ssh <host> "launchctl print gui/\$UID/dev.<user>.harborrs | grep state"  # → state = running  (macOS)
```

If the host has a known public Funnel URL, optional smoke from your
workstation:

```bash
curl -sf -o /dev/null -w '%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/rss/
```

Expect `200`.

### 6. Report

One-line summary: `<host>: <old> → <new>, supervisor active`. If
anything failed, surface the error and stop — do not paper over.

## Pitfalls

- **Stale tap** — `brew upgrade` is a no-op until `brew update`
  refreshes the tap index.
- **Missed restart** — replacing the binary on disk does not reload
  the running process. Always restart the supervisor.
- **launchd label varies** — embeds the deploying user
  (`dev.<user>.harborrs`). Read it from
  `~/Library/LaunchAgents/dev.*.harborrs.plist`, don't guess.
- **Mixed install methods** — a host may have `~/.local/bin/harborrs`
  *and* a brew copy *and* `/usr/local/bin/harborrs`. The supervisor's
  `ExecStart` pins one. Upgrade whichever the unit/plist points at,
  not `command -v harborrs`.
- **`harborrs update` write permissions** — selfupdate writes to the
  binary's own path. If the unit ExecStarts `/usr/local/bin/harborrs`
  but the user can't write there, selfupdate fails silently from the
  user's POV (errors only on stdout). Switch to `brew upgrade` or
  re-run `install.sh` with a writable `PREFIX`.
- **Active client polling drops** — restart kills in-flight HTTP;
  RSS clients will simply retry on their next sync interval. Avoid
  during a manual sync if avoidable.
- **Storage compatibility** — harborrs uses on-disk NDJSON / append-
  logs. Major version bumps may add migration steps; check the
  CHANGELOG before jumping more than one minor version, and prefer the
  test-unit verify path (step 4) for those.

## Checklist

- [ ] Target version confirmed (latest pushed tag).
- [ ] Install method + supervisor identified on the host (from the
      unit's `ExecStart`, not `which`).
- [ ] If a test unit exists: upgraded + smoke-tested first.
- [ ] Binary upgraded via the matching path.
- [ ] Supervisor restarted.
- [ ] `harborrs version` matches target.
- [ ] Service active.

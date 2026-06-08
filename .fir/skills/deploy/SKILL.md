---
name: deploy
description: Deploy harborrs to a remote host behind Tailscale Funnel, supervised by systemd (Linux) or launchd (macOS). Verify on a throwaway test unit on an alt port, then enable the prod unit.
---

# Deploy Skill

Deploy `harborrs` to a host fronted by `tailscale funnel`. The server
listens on loopback (or `0.0.0.0` on a chosen port); funnel terminates
TLS and forwards. Storage is plain files under a data dir.

The flow is **test-then-prod**: install the binary, start a *test*
service on an alternate port + scratch data dir, smoke-test it through
funnel, then enable the prod unit on the canonical port + data dir.
This catches binary/arch/funnel/firewall mistakes before they touch the
real subscriptions DB.

## Confirm with the user before acting

1. **Host** — ssh target (`user@host`). Linux or macOS.
2. **Funnel layout**:
   - **(a) Dedicated host** — funnel `127.0.0.1:8088` on `/`. Public
     URL: `https://<host>.<tailnet>.ts.net/`.
   - **(b) Prefix** — funnel `127.0.0.1:<port>` on `/<prefix>` (e.g.
     `/rss`). Funnel **strips** `/<prefix>` before forwarding, and
     harborrs's UI emits only relative URLs (since v0.3.1) so the
     prefix is transparent to the app. Public URL:
     `https://<host>.<tailnet>.ts.net/<prefix>/`.
3. **Install method**:
   - **Linux**: `install.sh` from a tagged release (default).
   - **macOS**: `brew install kfet/tap/harborrs` (default).
4. **Data dir** — default `~/.local/share/harborrs` (Linux) or
   `~/Library/Application Support/harborrs` (macOS); we set
   `HARBORRS_DATA` explicitly in the unit to remove ambiguity. Use the
   same convention for the test instance with a `-test` suffix.
5. **Initial credentials** — `harborrs init` generates a password and
   prints it once; capture it for the user.

## Steps

### 1. Ship the binary

**Linux (typical: Pi, racknerd box):**

```bash
ssh <host> 'curl -fsSL https://raw.githubusercontent.com/kfet/harb/main/install.sh | sh'
ssh <host> 'harborrs version'
```

`install.sh` lands the binary in `/usr/local/bin` if writable, else
`~/.local/bin`. Confirm `command -v harborrs` resolves to the same
path you expect the unit to ExecStart.

**macOS:**

```bash
ssh <host> 'brew tap kfet/tap && brew install kfet/tap/harborrs'
ssh <host> 'harborrs version'
```

(For a hotfix from the dev tree, cross-build with `make
build-<os>-<arch>` and `scp` to `~/.local/bin/harborrs` on the host.)

### 2. Bootstrap data dirs (test + prod)

Test instance lives entirely separately so we never touch prod state
during verify:

```bash
ssh <host> 'harborrs init \
  -data $HOME/.local/share/harborrs-test \
  -listen 127.0.0.1:8089'
```

Capture the printed password for the test smoke-test. (Use `-force` to
re-init in place if you've run this before.) Then bootstrap prod:

```bash
ssh <host> 'harborrs init \
  -data $HOME/.local/share/harborrs \
  -listen 127.0.0.1:8088'
```

Hand the prod-instance password to the user (one-time; it isn't
recoverable, only resettable via `harborrs passwd`).

### 3. Test unit — supervised on alt port

Start a throwaway service on `:8089` against the test data dir.

#### Linux: systemd user unit

`~/.config/systemd/user/harborrs-test.service`:

```ini
[Unit]
Description=harborrs (test)
After=network-online.target

[Service]
Environment=HARBORRS_DATA=%h/.local/share/harborrs-test
ExecStart=%h/.local/bin/harborrs serve
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=default.target
```

(Use the actual install path from step 1 — `/usr/local/bin/harborrs`
if `install.sh` ran with write access to `/usr/local/bin`, else
`~/.local/bin/harborrs`. Check with `ssh <host> 'command -v harborrs'`
and substitute into `ExecStart`. The unit file does not do PATH
resolution.)

```bash
ssh <host> 'systemctl --user daemon-reload && systemctl --user enable --now harborrs-test'
ssh <host> 'systemctl --user status harborrs-test --no-pager'
```

#### macOS: launchd user agent

`~/Library/LaunchAgents/dev.<user>.harborrs-test.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>dev.<user>.harborrs-test</string>
  <key>ProgramArguments</key>
  <array>
    <string>/opt/homebrew/bin/harborrs</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HARBORRS_DATA</key><string>/Users/<user>/Library/Application Support/harborrs-test</string>
    <key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string>
  </dict>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>StandardOutPath</key><string>/Users/<user>/Library/Logs/harborrs-test.out.log</string>
  <key>StandardErrorPath</key><string>/Users/<user>/Library/Logs/harborrs-test.err.log</string>
</dict>
</plist>
```

```bash
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.<user>.harborrs-test.plist
launchctl print gui/$UID/dev.<user>.harborrs-test | head
```

### 4. Funnel the test port and verify end-to-end

```bash
ssh <host> 'sudo tailscale funnel --bg --set-path=/rss-test 127.0.0.1:8089'
ssh <host> 'tailscale serve status'
```

From your workstation:

```bash
# Browser-equivalent: GET /rss-test/ follows the redirect chain to the
# login page. With the relative-URL UI (v0.3.1+), this lands on
# /rss-test/ui/login under the prefix, returning 200.
curl -sL --max-time 20 -o /dev/null \
  -w 'final=%{url_effective} code=%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/rss-test/

# Reader API — ClientLogin against the test creds.
curl -s -X POST \
  -d 'Email=<user>&Passwd=<test-password>' \
  https://<host>.<tailnet>.ts.net/rss-test/accounts/ClientLogin
```

Expect the curl to end at `/rss-test/ui/login` with `code=200`, and
`ClientLogin` to return `SID=…/Auth=…`. Connection refused → service
didn't bind to 127.0.0.1; check unit logs
(`journalctl --user -u harborrs-test -f` / `tail -f
~/Library/Logs/harborrs-test.err.log`).

### 5. Promote: enable the prod unit, retire the test unit

Repeat step 3 with name `harborrs.service` (or `dev.<user>.harborrs.plist`),
no `-test` suffix anywhere, and:
- `HARBORRS_DATA` → prod data dir
- listen port → `127.0.0.1:8088`

```bash
# Linux
ssh <host> 'systemctl --user daemon-reload && systemctl --user enable --now harborrs && loginctl enable-linger $USER'
ssh <host> 'systemctl --user status harborrs --no-pager'

# macOS
launchctl bootstrap gui/$UID ~/Library/LaunchAgents/dev.<user>.harborrs.plist
```

(`loginctl enable-linger` keeps the user unit running across logouts
and reboots; without it the service dies when the SSH session ends.)

Switch funnel: replace the test-prefix mapping with the prod
mapping. The prod mapping can be either the host root or a prefix
(`/rss`) — the UI is prefix-agnostic since v0.3.1.

```bash
# Remove test mapping
ssh <host> 'sudo tailscale funnel --https=443 --set-path=/rss-test off'

# Mount prod under the public path you want (here: /rss)
ssh <host> 'sudo tailscale funnel --bg --set-path=/rss 127.0.0.1:8088'
ssh <host> 'tailscale serve status'
```

If you'd rather have harborrs at the host root, drop `--set-path` and
the public URL becomes `https://<host>.<tailnet>.ts.net/`. If a stale
funnel mapping refuses to clear, `tailscale serve reset` wipes all
mappings on the host (destructive on multi-tenant boxes, fine on a
dedicated harborrs host).

Then tear down the test instance:

```bash
# Linux
ssh <host> 'systemctl --user disable --now harborrs-test'

# macOS
launchctl bootout gui/$UID/dev.<user>.harborrs-test
```

Optionally `rm -rf` the test data dir once you're sure prod is healthy.

### 6. Smoke test prod

```bash
# Browser-equivalent: GET <pub>/ follows the redirect chain to /ui/login,
# lands on 200. Works whether the funnel mount is / or /rss.
curl -sL --max-time 20 -o /dev/null \
  -w 'final=%{url_effective} code=%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/<pub>/

# Reader API ClientLogin (use real prod credentials).
curl -s -X POST \
  -d 'Email=<user>&Passwd=<prod-password>' \
  https://<host>.<tailnet>.ts.net/<pub>/accounts/ClientLogin
```

Expect the curl to end at `<pub>/ui/login` with `code=200`, and
`ClientLogin` to return `SID=…/Auth=…`. Note: `curl -I` (HEAD) returns
`405` on `/ui/login` because the login handler doesn't implement HEAD;
that's a curl-test artefact, not a server bug. Always smoke-test with
`-L` (follow) on a GET.

Then point one real RSS client (Reeder Classic / NetNewsWire / etc.)
at `https://<host>.<tailnet>.ts.net/<pub>/` with the FreshRSS API
profile and the prod credentials. Confirm it lists subscriptions and
`harborrs poll-once` works:

```bash
ssh <host> 'HARBORRS_DATA=$HOME/.local/share/harborrs harborrs poll-once'
```

### 7. Tail logs during first hour

```bash
# Linux
ssh <host> 'journalctl --user -u harborrs -f'

# macOS
ssh <host> 'tail -f ~/Library/Logs/harborrs.err.log'
```

Watch for: poll loop start, per-feed conditional GETs, no auth 401
spam from the client.

## Importing existing subscriptions (optional)

If the user has an OPML export from the previous reader, import after
the prod unit is up but before clients connect (or stop the unit first
to be safe):

```bash
scp subscriptions.opml <host>:/tmp/
ssh <host> 'systemctl --user stop harborrs && \
            HARBORRS_DATA=$HOME/.local/share/harborrs harborrs import /tmp/subscriptions.opml && \
            systemctl --user start harborrs'
```

## Pitfalls

- **`curl -I` returns 405 on `/ui/login`** — the login handler
  doesn't implement HEAD. Use `curl -sL` (GET, follow) for smoke
  tests, not `-I`.
- **Port collisions** — if the test unit is left running on `:8089`
  and you reuse `:8089` for prod, the second unit will fail to bind.
  Always retire the test unit before reusing its port.
- **`harborrs init` overwrites credentials with `-force`** — use
  `-force` only on the test data dir; prod `init` should be the
  one-and-only invocation, with the password captured immediately.
- **launchd PATH** — `harborrs serve` itself needs no extra binaries,
  but `EnvironmentVariables.PATH` should still include the install dir
  if you ever invoke `harborrs poll-once` etc. via launchd.
- **`loginctl enable-linger`** — without it the systemd user unit
  stops the moment your ssh session exits.
- **Data dir on a tmpfs / volatile mount** — verify the data dir is on
  persistent storage; `/tmp` and some Pi setups will silently lose
  feeds across reboots.
- **Funnel limit** — Tailscale Funnel allows a fixed number of
  prefixes per host (currently 3). If the host already runs poe-acp
  + something else, plan the prefix budget before starting.

## Backups

harb's data dir is backed up to a remote host (`kopione`) by an
event-driven rsync watcher, **not** a timer. A systemd user unit runs a
script that watches the data dir with `inotifywait` and syncs after a
quiet period.

- **Unit**: `harb-backup.service` (user unit), `ExecStart` = the watcher
  script `~/.local/bin/harb-backup-watch`.
- **Watcher script** (lives on the host, not version-controlled — keep
  this section as the source of truth):

```bash
SRC="$HOME/.local/share/harb/"
DEST="kopione:backups/harb/"
# debounce, then:
rsync -az --delete --max-delete=500 \
  --exclude='observe/' --exclude='*.tmp' --exclude='.*.tmp' \
  -e "ssh -o BatchMode=yes -o ConnectTimeout=15" "$SRC" "$DEST"
# plus: on OPML content change, copy subscriptions.opml to
#   kopione:backups/harb-opml/subscriptions.<utc-stamp>.opml  (keep all)
```

### Design rules (learned the hard way)

- **Latest-only mirror, no per-sync history.** An earlier version used
  `rsync --backup --backup-dir=../harb-history/<ts>` on *every* event.
  Every overwritten/deleted file was stashed into a new dated dir and
  nothing pruned them — 3815 snapshots filled the remote (a 228G SD
  card) to 100% while the live data was only ~80M. Do not reintroduce
  blanket `--backup-dir`.
- **Version only the curated file.** `subscriptions.opml` is the one
  irreplaceable, human-edited artefact → keep an immutable timestamped
  copy per change. Everything else (entries, read/starred state, poll
  state) is append-mostly or re-pollable; latest is all you need.
- **Exclude `observe/`.** It's pure poll telemetry (304/not-modified
  lines), the churniest + largest dir, and worthless in a backup.
- **`--max-delete` guard.** Caps how many deletions one sync may
  propagate, so a local corruption/wipe aborts the rsync (rc 25) and is
  logged instead of silently nuking the backup.
- **No per-file compression.** gzip-ing mirror files breaks rsync's
  delta transfer *and* its unchanged-file check. Wire compression
  (`-z`) is fine; for storage compression use filesystem-level zstd
  (btrfs/zfs) on the destination — transparent to rsync. No cold tar
  archive: the data shape doesn't justify one.

### Operating

```bash
# status / pause / resume
systemctl --user status  harb-backup.service
systemctl --user disable --now harb-backup.service     # pause
systemctl --user enable  --now harb-backup.service     # resume

# env knobs (set in the unit): HARB_BACKUP_QUIET (debounce s, default 30),
#                              HARB_BACKUP_MAX_DELETE (default 500)
```

### Restore

```bash
systemctl --user stop harb
rsync -az kopione:backups/harb/ ~/.local/share/harb/
# OPML point-in-time: pick a copy from kopione:backups/harb-opml/
systemctl --user start harb
```

## Handoff checklist

- [ ] `harborrs version` on the host matches the intended release.
- [ ] `tailscale funnel status` shows the expected prod mapping.
- [ ] Curl smoke test: UI returns `200`, `ClientLogin` returns
      `SID=…/Auth=…`.
- [ ] Test unit retired (disabled + bootout); test data dir cleaned
      up or labelled.
- [ ] Prod supervisor enabled: systemd user unit + linger (Linux) **or**
      launchd user agent with `RunAtLoad` + `KeepAlive` (macOS).
- [ ] Prod password handed to the user (one-time).
- [ ] `HARBORRS_DATA` set explicitly in the prod unit (not relying on
      shell env).
- [ ] At least one real RSS client successfully synced.

## Upgrading

See the `update` skill (`.fir/skills/update/SKILL.md`) for the per-host
upgrade flow. Quick reference:

- **In-place selfupdate**: `ssh <host> 'harborrs update'` (writes the
  new binary alongside the running one), then restart the supervisor.
- **Brew (macOS)**: `brew upgrade kfet/tap/harborrs && launchctl kickstart -k gui/$UID/<label>`.
- **install.sh re-run**: `ssh <host> 'curl -fsSL https://raw.githubusercontent.com/kfet/harb/main/install.sh | sh'`, then `systemctl --user restart harborrs`.

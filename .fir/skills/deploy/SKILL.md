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
     harborrs has no path-prefix flag (it serves at `/`), so the
     stripping is what makes prefix-mode work transparently.
3. **Install method**:
   - **Linux**: `install.sh` from a tagged release (default).
   - **macOS**: `brew install kfet/harborrs/harborrs` (default).
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
ssh <host> 'curl -fsSL https://raw.githubusercontent.com/kfet/harborrs/main/install.sh | sh'
ssh <host> 'harborrs version'
```

`install.sh` lands the binary in `/usr/local/bin` if writable, else
`~/.local/bin`. Confirm `command -v harborrs` resolves to the same
path you expect the unit to ExecStart.

**macOS:**

```bash
ssh <host> 'brew tap kfet/harborrs && brew install kfet/harborrs/harborrs'
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
ssh <host> 'tailscale funnel --bg --set-path=/rss-test 127.0.0.1:8089'
ssh <host> 'tailscale funnel status'
```

From your workstation:

```bash
# Server liveness — login page renders.
curl -sf -o /dev/null -w '%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/rss-test/

# Reader API — ClientLogin against the test creds.
curl -s -X POST \
  -d 'Email=<user>&Passwd=<test-password>' \
  https://<host>.<tailnet>.ts.net/rss-test/accounts/ClientLogin
```

Expect `200` for the UI, and a body containing `SID=` / `Auth=` lines
for `ClientLogin`. `404` → funnel prefix mismatch (funnel strips, but
the host rewrites need to be on the funnel side, not the app).
Connection refused → service didn't bind to 127.0.0.1; check unit
logs (`journalctl --user -u harborrs-test -f` / `tail -f
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

Switch funnel: add a new mapping for the prod port, then remove the
test mapping. `tailscale funnel`/`serve` syntax for **removing** a
prefix has varied across versions — use `tailscale serve status` to
inspect, then the matching `off` form:

```bash
# Add prod mapping
ssh <host> 'tailscale funnel --bg --set-path=/rss 127.0.0.1:8088'
ssh <host> 'tailscale serve status'

# Remove test mapping (pick the form your tailscale version accepts)
ssh <host> 'tailscale funnel --https=443 --set-path=/rss-test off' \
  || ssh <host> 'tailscale serve --https=443 --set-path=/rss-test off'
```

If neither form works, `tailscale serve reset` clears all mappings on
the host and you re-add only `/rss` — destructive on a multi-tenant
host, fine on a dedicated one.

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
curl -sf -o /dev/null -w '%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/rss/

curl -s -X POST \
  -d 'Email=<user>&Passwd=<prod-password>' \
  https://<host>.<tailnet>.ts.net/rss/accounts/ClientLogin
```

Then point one real RSS client (Reeder Classic / NetNewsWire / etc.)
at `https://<host>.<tailnet>.ts.net/rss/` with the FreshRSS API
profile and the prod credentials. Confirm it lists subscriptions
(empty on a fresh deploy) and `harborrs poll-once` works:

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

- **Funnel prefix vs app root** — harborrs serves at `/`. Funnel's
  `--set-path=/X` strips `/X` before forwarding, so the app never sees
  it. Don't try to "match" the prefix in app config; there's no flag
  for it.
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
- **Brew (macOS)**: `brew upgrade kfet/harborrs/harborrs && launchctl kickstart -k gui/$UID/<label>`.
- **install.sh re-run**: `ssh <host> 'curl -fsSL https://raw.githubusercontent.com/kfet/harborrs/main/install.sh | sh'`, then `systemctl --user restart harborrs`.

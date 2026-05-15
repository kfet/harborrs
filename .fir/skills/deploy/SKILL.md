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
2. **Funnel layout** — harborrs serves at `/` and emits **absolute**
   redirect/link paths (e.g. `Location: /ui/`). It has no path-prefix
   flag, so the only working funnel layout is **root mount**:
   - funnel `127.0.0.1:8088` on `/` (default).
   - Public URL: `https://<host>.<tailnet>.ts.net/`.
   - This means one harborrs per tailnet hostname. If the host already
     funnels something else at `/`, give harborrs its own host (or
     run it behind a real reverse proxy that can rewrite Location
     headers — out of scope here).
   - **Do not use `--set-path=/rss` etc.** Funnel will strip the
     prefix correctly on the request side, but harborrs's responses
     contain absolute paths like `/ui/login` which the browser
     resolves against the funnel hostname — landing outside the
     prefix and 404'ing at the tailscale edge.
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

The test instance uses a prefix mount only because we have to coexist
with the prod instance during verify — and we accept that some flows
(anything that follows a redirect) will look broken under the prefix.
The smoke tests below stay on single-hop endpoints that don't redirect.

```bash
ssh <host> 'sudo tailscale funnel --bg --set-path=/rss-test 127.0.0.1:8089'
ssh <host> 'tailscale serve status'
```

From your workstation:

```bash
# Single-hop UI endpoint (avoids the absolute-redirect trap).
curl -sf -o /dev/null -w '%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/rss-test/ui/login    # expect 200

# Reader API — ClientLogin against the test creds.
curl -s -X POST \
  -d 'Email=<user>&Passwd=<test-password>' \
  https://<host>.<tailnet>.ts.net/rss-test/accounts/ClientLogin
```

Expect `200` for the login page, and a body containing `SID=` / `Auth=`
lines for `ClientLogin`. `404` → funnel prefix mismatch on the
single-hop endpoint itself. Connection refused → service didn't bind
to 127.0.0.1; check unit logs (`journalctl --user -u harborrs-test -f`
/ `tail -f ~/Library/Logs/harborrs-test.err.log`).

Skip the `GET /rss-test/` smoke — it 303's to absolute `/ui/` which
escapes the prefix and 404's. That's a property of the prefix mount,
not a real bug; the prod mount at `/` won't have this issue.

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

Switch funnel: replace the test-prefix mapping with a **root** mapping
pointing at the prod port. The test prefix must go away — harborrs
needs `/` to itself for absolute redirects to land back on the same
host.

```bash
# Remove test mapping
ssh <host> 'sudo tailscale funnel --https=443 --set-path=/rss-test off'

# Mount prod at root
ssh <host> 'sudo tailscale funnel --bg 127.0.0.1:8088'
ssh <host> 'tailscale serve status'
```

If a stale funnel mapping refuses to clear, `tailscale serve reset`
wipes all mappings on the host (destructive on multi-tenant boxes,
fine on a dedicated harborrs host).

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
# Browser-equivalent: GET / follows 303 → /ui/ → /ui/login, lands on 200.
curl -sL --max-time 20 -o /dev/null \
  -w 'final=%{url_effective} code=%{http_code}\n' \
  https://<host>.<tailnet>.ts.net/

# Reader API ClientLogin (real prod credentials).
curl -s -X POST \
  -d 'Email=<user>&Passwd=<prod-password>' \
  https://<host>.<tailnet>.ts.net/accounts/ClientLogin
```

Expect the curl to end at `/ui/login` with `code=200`, and
`ClientLogin` to return `SID=…/Auth=…`. Note: `curl -I` (HEAD) returns
`405` on `/ui/login` because the login handler doesn't implement HEAD;
that's a curl-test artefact, not a server bug. Always smoke-test with
`-L` (follow) on a GET.

Then point one real RSS client (Reeder Classic / NetNewsWire / etc.)
at `https://<host>.<tailnet>.ts.net/` with the FreshRSS API profile
and the prod credentials. Confirm it lists subscriptions and
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

- **Funnel prefix breaks the UI** — harborrs serves at `/` and emits
  **absolute** `Location: /ui/...` redirects. If you funnel a prefix
  (`--set-path=/rss`), `GET /rss/` returns `303 Location: /ui/`, the
  browser follows to `https://host/ui/`, which has no funnel mapping,
  and tailscale's edge returns `404`. Mount at root or run a real
  reverse proxy with Location rewriting.
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

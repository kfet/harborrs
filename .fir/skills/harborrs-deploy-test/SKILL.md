---
name: harborrs-deploy-test
description: Build harborrs from the local repo HEAD, deploy to the staging launchd unit + funnel mount, and smoke-test. Use before promoting any change to prod.
---

# harborrs-deploy-test

Build the current repo HEAD as `harborrs-test`, restart the staging launchd
unit, and verify the funnel-mounted staging URL works. Used as a gate
before `harborrs-promote-prod`.

## Layout (assumed pre-existing)

- Test binary: `~/.local/bin/harborrs-test`
- Data dir:    `~/.local/share/harborrs-test/`
- LaunchAgent: `~/Library/LaunchAgents/dev.kfet.harborrs.test.plist` → label `dev.kfet.harborrs.test`, listens on `:8089`
- Funnel:      `https://kfetairm1.tail77d32.ts.net/rss-test` → `127.0.0.1:8089`
- Logs:        `~/Library/Logs/harborrs-test/{out,err}.log`

Prod runs in parallel on the same host (`/opt/homebrew/bin/harborrs`,
`:8088`, `/rss`, label `dev.kfet.harborrs`) and is **never touched** by
this skill.

## Recipe

1. **Build from current branch** with version metadata baked in:

   ```bash
   cd ~/dev/go/harborrs
   VERSION=$(cat VERSION)-test
   COMMIT=$(git rev-parse --short=12 HEAD)
   BUILD_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ)
   LDFLAGS="-s -w -X github.com/kfet/harb.Version=$VERSION -X github.com/kfet/harb.Commit=$COMMIT -X github.com/kfet/harb.BuildDate=$BUILD_DATE"
   go build -trimpath -ldflags="$LDFLAGS" -o ~/.local/bin/harborrs-test ./cmd/harborrs
   ~/.local/bin/harborrs-test version
   ```

2. **Restart the staging unit** so it picks up the new binary:

   ```bash
   launchctl kickstart -k gui/$(id -u)/dev.kfet.harborrs.test
   sleep 2
   launchctl print gui/$(id -u)/dev.kfet.harborrs.test | grep -E '^\s+(state|pid|last exit)'
   ```

   Expect `state = running` and a fresh `pid`.

3. **Smoke-test** local + public:

   ```bash
   curl -sS -o /dev/null -w "local /ui/login → HTTP %{http_code}\n" http://127.0.0.1:8089/ui/login
   curl -sS -o /dev/null -w "funnel /rss-test/ui/login → HTTP %{http_code}\n" https://kfetairm1.tail77d32.ts.net/rss-test/ui/login
   curl -sS -o /dev/null -w "funnel /rss-test/ui/ → HTTP %{http_code}\n" https://kfetairm1.tail77d32.ts.net/rss-test/ui/
   ```

   Both must be `200` / valid redirect chains. If the public URL is
   anything else, check funnel status:

   ```bash
   /Applications/Tailscale.app/Contents/MacOS/tailscale funnel status
   ```

4. **Targeted regression checks** for the specific change being shipped
   — e.g. for a redirect fix, walk every redirect path the change
   touches and assert relative `Location` headers when served via the
   funnel. Login with the prod admin password (the staging instance
   shares it).

5. **Report** back: version string from `harborrs-test version`, public
   URL, what specifically was tested, any anomalies.

## Tearing down (rare)

The staging unit is designed to live indefinitely. Only stop it if you
explicitly need :8089 free:

```bash
launchctl bootout gui/$(id -u)/dev.kfet.harborrs.test
/Applications/Tailscale.app/Contents/MacOS/tailscale serve --https=443 --set-path=/rss-test off  # remove funnel mount
```

## Notes

- The staging data dir was seeded once from prod's `subscriptions.opml`;
  if you want a fresh seed, copy again:
  `cp ~/.local/share/harborrs/subscriptions.opml ~/.local/share/harborrs-test/`.
  Don't run `harborrs-test poll-once` against staging in tight loops —
  the upstream feed servers see both polls.
- The staging password is whatever you last set with
  `harborrs-test passwd -data ~/.local/share/harborrs-test`.
- Prod is unaffected by this skill. No `brew` commands here.

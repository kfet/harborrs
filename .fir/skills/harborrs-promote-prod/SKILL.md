---
name: harborrs-promote-prod
description: Promote a released harborrs version to the prod launchd unit on this host via brew upgrade. Only run after `harborrs-deploy-test` validated the change.
---

# harborrs-promote-prod

Bring the prod harborrs deployment on this host (`dev.kfet.harborrs`) up
to the latest released version. Strict precondition: the same code must
already be running green on `dev.kfet.harborrs.test`.

## Layout

- Prod binary:  `/opt/homebrew/bin/harborrs` (Homebrew, tap `kfet/harborrs`)
- Prod data:    `~/.local/share/harborrs/`
- LaunchAgent:  `dev.kfet.harborrs` listens on `:8088`
- Funnel:       `https://kfetairm1.tail77d32.ts.net/rss` → `127.0.0.1:8088`
- Logs:         `~/Library/Logs/harborrs/{out,err}.log`

## Preconditions (abort on failure)

1. **Staging is the same version** — `harborrs-deploy-test` was run for
   the change and the staging URL behaves as expected. Confirm with
   the user if uncertain.
2. **Release is published** — `git tag` for the target version exists
   on `origin`, GitHub Release page has the binary tarballs, and the
   Homebrew formula in `kfet/harborrs` tap was updated.
   Check: `brew update && brew info kfet/harborrs/harborrs | head -3`
   should show the new version available.

## Recipe

1. **Capture current state** (for rollback context):

   ```bash
   harborrs version
   curl -sS -o /dev/null -w "before /rss/ui/login: HTTP %{http_code}\n" https://kfetairm1.tail77d32.ts.net/rss/ui/login
   ```

2. **Upgrade**:

   ```bash
   brew update
   brew upgrade kfet/harborrs/harborrs
   ```

   If brew reports already up-to-date but the tap formula hasn't
   refreshed yet, wait a minute and retry — the formula PR in the
   `homebrew-harborrs` repo is usually opened automatically by the
   release workflow and merged within a few minutes.

3. **Restart the launchd unit** so the new binary is live:

   ```bash
   launchctl kickstart -k gui/$(id -u)/dev.kfet.harborrs
   sleep 2
   launchctl print gui/$(id -u)/dev.kfet.harborrs | grep -E '^\s+(state|pid|last exit)'
   ```

4. **Verify**:

   ```bash
   harborrs version
   curl -sS -o /dev/null -w "after /rss/ui/login: HTTP %{http_code}\n" https://kfetairm1.tail77d32.ts.net/rss/ui/login
   ```

   Version must reflect the new release. HTTP must still be 200.

5. **Report**: old version → new version, prod URL, tail of
   `~/Library/Logs/harborrs/err.log` if anything looks off.

## Rollback

Homebrew keeps the previous Cellar version around briefly. If anything
is wrong:

```bash
brew unlink harborrs
brew install kfet/harborrs/harborrs@<old-version>  # if a versioned formula exists
# or: download the previous tarball from GitHub Releases and place it
# at /opt/homebrew/Cellar/harborrs/<old>/bin/harborrs, then re-link.
launchctl kickstart -k gui/$(id -u)/dev.kfet.harborrs
```

Simpler emergency option: copy `~/.local/bin/harborrs-test` (which was
built from a known-good commit) into `/opt/homebrew/bin/harborrs` and
restart — bypasses brew entirely until you can sort the release.

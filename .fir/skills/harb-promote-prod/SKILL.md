---
name: harb-promote-prod
description: Cut prod (sea-racknerd) over to a harb release that harb-deploy-test already staged + validated on disk — restart harb.service onto the new binary and smoke the public /rss funnel. Includes rollback via the harb.bak-<ts> binary.
---

# harb-promote-prod

Restart the prod `harb.service` on `sea-racknerd` so the live process
picks up the new on-disk binary that `harb-deploy-test` already
selfupdated and validated. Strict precondition: the new binary is
already at `~/.local/bin/harb` and passed the out-of-band smoke.

## Host + layout

- Host:      `sea-racknerd` (Linux/x86_64, systemd **user** units).
- Prod unit: `harb.service` — `~/.local/bin/harb serve`,
  `HARB_DATA=~/.local/share/harb`, `:8088`.
- Funnel:    `https://sea-racknerd.tail77d32.ts.net/rss` → `127.0.0.1:8088`.
- Backup:    `harb-backup.service` (event-driven rsync to `kopione`).
- Logs:      `journalctl --user -u harb.service`.
- Rollback:  `~/.local/bin/harb.bak-<ts>` (left by `harb-deploy-test`).

## Preconditions (abort on failure)

1. **`harb-deploy-test` ran GREEN for this version** — the new binary is
   on disk (`ssh sea-racknerd '~/.local/bin/harb version'` == target) and
   the out-of-band smoke passed. If the binary on disk is NOT the target,
   stop and run `harb-deploy-test` first.
2. **Release is published** — GitHub release `vX.Y.Z` exists with the
   platform tarballs + `checksums.txt` (that's what selfupdate pulled).
3. **Storage migration check** — skim the `CHANGELOG.md` span between the
   running and target versions. If it mentions an on-disk format/storage
   migration, pause the backup watcher first (see Notes) and be ready to
   roll back.

## Recipe

1. **Capture current state** (for rollback context):

   ```bash
   ssh sea-racknerd 'systemctl --user is-active harb.service'
   curl -sSL --max-time 20 -o /dev/null \
     -w "before /rss/ -> %{http_code}\n" \
     https://sea-racknerd.tail77d32.ts.net/rss/
   ```

2. **Restart prod onto the new binary**:

   ```bash
   ssh sea-racknerd 'systemctl --user restart harb.service && sleep 2 && systemctl --user is-active harb.service'
   ```

   Expect `active`.

3. **Verify**:

   ```bash
   ssh sea-racknerd '~/.local/bin/harb version'             # = target
   curl -sSL --max-time 20 -o /dev/null \
     -w "after /rss/ -> %{url_effective} %{http_code}\n" \
     https://sea-racknerd.tail77d32.ts.net/rss/
   ssh sea-racknerd 'journalctl --user -u harb.service -n 6 --no-pager'
   ```

   The public GET `/rss/` must follow its redirect chain to
   `…/rss/ui/login` with `200`. (`curl -I`/HEAD returns `405` on
   `/ui/login` — that's a curl artefact, not a bug; always smoke with
   `-L` on a GET.) The log should show the listen line and clean
   access entries — no auth-401 spam, no panic.

4. **Report**: old version → new version, prod URL, `is-active` state,
   and a tail of the journal if anything looks off.

## Rollback

The previous binary is still on disk as `~/.local/bin/harb.bak-<ts>`
(written by `harb-deploy-test`). Restore it and restart:

```bash
ssh sea-racknerd '
  cp -a ~/.local/bin/harb.bak-<ts> ~/.local/bin/harb
  systemctl --user restart harb.service && sleep 2
  systemctl --user is-active harb.service
  ~/.local/bin/harb version'        # back to the old version
```

Then smoke `/rss/` again. Investigate the bad release off the hot path
before retrying.

## Notes

- **`harb-backup.service`** is an event-driven rsync watcher; a normal
  upgrade needs no special handling. For a release that performs an
  on-disk storage migration, pause it before the restart and re-enable
  after you've confirmed prod is healthy:

  ```bash
  ssh sea-racknerd 'systemctl --user disable --now harb-backup.service'   # pause
  # … restart + verify …
  ssh sea-racknerd 'systemctl --user enable  --now harb-backup.service'   # resume
  ```

- **Active client polling drops** — the restart kills in-flight HTTP;
  RSS clients simply retry on their next sync. Harmless.
- **Only this host.** For other/new hosts use the generic `update` or
  `deploy` skills.

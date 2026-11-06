# Frontend-only RouteGate UI publication

A standard merge to `main` **does not deploy** the live Admin UI. The full
production-like deployment replaces the Manager, migrations, UI, and other
components. For RG-80 styling-only changes, use the isolated static publication
path **only after checking compatibility with the currently installed Manager**.

## Scope and audit

The existing `Production-like Ops` workflow accepts:

`operation=update-frontend commit=<40-character main SHA>`

It runs solely from trusted `main`. Normally the exact target SHA must
have successful `RouteGate CI` on a `main` push. For **UI-only** releases
squash-merged via GitHub App, where a new `main` CI push is not emitted, a
narrow alternative proves that the target main commit is the merge commit of
one internal, merged PR, its full Git tree is byte-for-byte identical to that
PR's head commit, and the head commit has successful `RouteGate CI` from
`pull_request`. Manager and bootstrap operations **never** use this fallback. GitHub Actions builds the frontend via `npm ci` and
`npm run build`, stamps the artifact with the full revision, computes SHA256,
and copies the archive and fixed audited runner to US via the pinned SSH key and
known-host verification. It executes under the production-like host lock and
environment controls.

The runner modifies **only**:
- `/var/www/routegate/index.html` (atomic rename after publishing assets);
- new immutable `/var/www/routegate/assets/*` bundles;
- `/var/www/routegate/routegate-ui-version.txt` (commit verification).

The previous `index.html` is saved under
`/var/www/.routegate-ui-backups/<commit>.index.html.bak` outside the served
tree. Existing bundles are retained, so active browser tabs do not lose their
current assets and an index rollback remains possible.

The runner never changes `/var/www/routegate/bootstrap`, `connect.html`,
nginx configuration, database, Manager binary/systemd unit, Agent or any VPN
runtime. It does **not** call `systemctl`, publish a bootstrap artifact, or
touch the update/version identity of Manager.

## Guardrails

1. Confirm the selected revision is compatible with the deployed Manager API;
   a frontend-only update is not a substitute for backend migrations.
2. Exact, successful main CI and trusted commit containment are mandatory.
3. The archive must contain an exact revision marker and valid, referenced
   immutable Vite assets; reject unsafe archive paths, links and hash collisions.
4. Publish new assets *before* atomically updating the HTML entrypoint.
5. Verify the public HTTPS HTML and revision marker match the new release and
   that its JS is reachable. On failure restore the previous index and marker.
6. No unreviewed parallel RG-140 production deployment or active Ops Bridge
   operation; GitHub's concurrency lock and host flock serialize execution.

## Request and validation

Use the designated control issue #268 by setting its body to the exact
allow-listed command above while closed, then reopening. Alternatively run
the typed `Production-like Ops` dispatch from `main` with
`operation=update-frontend` and the same full commit SHA.

After an explicit successful result, check:

- `https://us.routegate.org/routegate-ui-version.txt` returns the expected
  revision;
- the Dashboard's «Состояние инфраструктуры» shows icon rows and separated
  status surfaces at desktop and mobile widths;
- Manager API, subscriptions and all three VPN services remain as before;
- hard-refresh the browser (`Ctrl+F5`) if it cached the earlier entry.

### Tests

```sh
python3 -m unittest discover -s scripts -p test_production_like_frontend_static.py
python3 -m unittest discover -s scripts -p test_ops_bridge_identity.py
```

They simulate successful publication, file preservation and rollback without
connecting to production.

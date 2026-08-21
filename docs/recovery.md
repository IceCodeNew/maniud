# Recovery runbook

Keep the state database, its sidecars, the lock files, and the `backups`
directory together. Do not edit SQLite rows, runtime ownership labels, backup
manifests, or journal files by hand.

The default state locations are:

```text
$XDG_STATE_HOME/maniud/state.db
$XDG_STATE_HOME/maniud/backups/
```

When `XDG_STATE_HOME` is unset, replace it with `$HOME/.local/state`.

## Interrupted apply

1. Preserve the Git commit and Compose inputs used by the failed command.
2. Restore access to the same runtime endpoint, state directory, and registry
   credentials.
3. Preview the next safe action:

   ```sh
   maniud apply --dry-run services/web.yaml
   ```

   A recovery plan reports `resume`, `probe-unknown-effect`, or `restore`.
4. Run the same mutation command:

   ```sh
   maniud apply services/web.yaml
   ```

An action recorded as `effect_outcome_unknown` only permits that action's typed
probe. maniud does not replay the effect. The probe either proves the expected
postcondition, proves a negative result that the transaction can handle, or
leaves the transaction unresolved.

The first SIGINT or SIGTERM stops new effects and gives the active operation a
chance to persist recoverable state and release owned resources. The process
then exits 130. A later signal exits immediately, so use it only when waiting
for durable cleanup creates a larger operational risk.

## Interrupted daemon cycle

Leave the registered checkout on the commit used by the interrupted cycle, then
run:

```sh
maniud daemon --once
```

The daemon captures and validates the entire registered snapshot. It recovers
`resume`, `probe-unknown-effect`, and `restore` plans from the currently
registered commit before fetching a newer desired-state commit. It does not
apply part of a new snapshot when another service file fails validation.

## Degraded upgrade

A degraded upgrade restores the previous workload generation. Restore can use
only the transaction-owned backup whose manifest, artifacts, digests, runtime,
base transaction, and predecessor identity all match durable journal evidence.

1. Keep the transaction backup directory unchanged.
2. Restore runtime connectivity and enough host filesystem capacity.
3. Run the same `apply` command or `maniud daemon --once`.
4. Treat `daemon_mount_probe_unavailable` as an explicit phase-one downgrade.
   Host backup capacity and identity checks still run, but maniud cannot prove
   free space and filesystem identity at the remote daemon's mount target.

Do not copy a different backup into the expected transaction directory. The
manifest and content checks will reject it.

## Rebuild a missing backup index

`doctor` rebuilds only maniud's private SQLite backup index. It does not create
journal transactions, infer an applied commit, or repair backup files.

First scan and review the candidates without writing state:

```sh
maniud doctor --reindex-backups
```

If every reported manifest belongs to the expected transaction, replace the
index:

```sh
maniud doctor --reindex-backups --confirm
```

Confirmation waits for active service writers, prevents new writers from
starting, scans complete manifests, and replaces the full index in one SQLite
transaction. Any malformed directory, incomplete publication, identity
mismatch, or unknown successful upgrade aborts the replacement.

Use `--state /absolute/path/to/state.db` only when operating on an explicitly
selected state directory:

```sh
maniud doctor --reindex-backups --state /srv/maniud/state.db
```

Run `--confirm` only after the read-only command succeeds against the same path.

## Conflicting evidence

Stop retries when `apply_failed` has `retryable: false` and the debug causes
identify conflicting runtime or durable evidence. Preserve these items for
review:

- the exact desired-state Git commit and release version;
- the state database, sidecars, locks, and complete backup root;
- read-only runtime inspect output collected through existing operator tooling.

Do not delete or rename the observed workload to make a plan pass. That can
remove the evidence required to recover an unknown effect.

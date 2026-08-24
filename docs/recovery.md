# Recovery guide

[English](recovery.md) | [简体中文](recovery.zh-CN.md)

Keep the state database, its sidecars, lock files, and `backups` directory together.
Do not edit database rows, ownership labels, backup manifests, or journal files.

The default state paths are:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/state.db
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/backups/
```

## Recover an interrupted standalone apply

1. Keep the Compose file used by the interrupted command.
2. Restore access to the same runtime, state directory, host bind paths, and registry credentials.
3. Run `maniud apply photos.yaml` and let maniud continue the recorded transaction.

maniud checks the runtime before choosing a safe way to continue an operation whose outcome is unknown.
An unavailable or ambiguous runtime leaves the transaction unresolved instead of guessing.

The first SIGINT or SIGTERM stops new effects and lets maniud save recoverable state before exiting with status 130.
A later signal exits immediately and may interrupt that cleanup.

## Recover an interrupted GitOps cycle

Keep the registered checkout, state directory, runtime workload, and backups unchanged.
Restart the supervised `maniud daemon start` process after runtime and registry access have recovered.
The daemon reconciles immediately, recovers unfinished transactions, then fetches a newer commit.
It validates the complete desired-state snapshot and does not apply part of a snapshot when another service file is invalid.

## Recover a failed upgrade

maniud restores only the backup owned by the current transaction.
Keep that backup directory unchanged, restore runtime connectivity and host filesystem capacity, then rerun the same standalone `apply` or restart the supervised `maniud daemon start` process.
Do not copy another backup into the transaction directory because identity and content checks will reject it.

## Rebuild the backup index

Scan the backup directory without changing state:

```sh
maniud doctor --reindex-backups
```

If every reported backup belongs to the expected transaction, replace the index:

```sh
maniud doctor --reindex-backups --confirm
```

The confirmed command waits for active service operations, scans complete manifests, and replaces the full index atomically.
It stops on malformed, incomplete, or conflicting backup evidence.

Use an explicit state database only when operating on another state root:

```sh
maniud doctor --reindex-backups --state /srv/maniud/state.db
```

Run `--confirm` only after the read-only scan succeeds against the same path.

## Preserve conflicting evidence

Stop retrying when `apply_failed` has `retryable: false`.
Keep the desired Compose file or Git commit, the maniud version, the complete state and backup directories, and read-only runtime inspection output from existing operator tools.
Do not delete or rename the observed workload because that may remove the evidence needed to recover an unknown effect.

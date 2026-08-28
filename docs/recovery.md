# Recovery guide

[English](recovery.md) | [简体中文](recovery.zh-CN.md)

Keep the complete state directory and its `backups` directory unchanged.
Do not edit files in that directory or move files between its backup folders.

The default state paths are:

```text
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/state.db
${XDG_STATE_HOME:-$HOME/.local/state}/maniud/backups/
```

## Recover an interrupted standalone apply

1. Keep the Compose file used by the interrupted command.
2. Restore access to the same runtime, state directory, host bind paths, and registry credentials.
3. Run `maniud apply photos.yaml` and let maniud continue the recorded operation.

maniud checks the runtime before choosing a safe way to continue an operation whose outcome is unknown.
If the runtime is unavailable or its result is unclear, maniud stops and preserves the recorded operation.

The first SIGINT or SIGTERM stops new effects and lets maniud save recoverable state before exiting with status 130.
A later signal exits immediately and may interrupt that cleanup.

## Recover an interrupted GitOps cycle

Keep the registered checkout, state directory, runtime workload, and backups unchanged.
Restart the supervised `maniud daemon start` process after runtime and registry access have recovered.
The daemon first recovers unfinished operations, then checks for a newer commit.
It validates every service file before applying any of them.

## Recover a failed upgrade

maniud restores only the backup created for the interrupted operation.
Keep that backup directory unchanged, restore runtime connectivity and host filesystem capacity, then rerun the same standalone `apply` or restart the supervised `maniud daemon start` process.
Do not copy another backup into that directory.

## Rebuild the backup index

Scan the backup directory without changing state:

```sh
maniud doctor --reindex-backups
```

If every reported backup belongs to the expected operation, replace the index:

```sh
maniud doctor --reindex-backups --confirm
```

The confirmed command waits for active service operations and replaces the complete index.
It stops if a backup is incomplete, invalid, or conflicts with another record.

Use an explicit state database only when operating on another state root:

```sh
maniud doctor --reindex-backups --state /srv/maniud/state.db
```

Run `--confirm` only after the read-only scan succeeds against the same path.

## Preserve conflicting evidence

Stop retrying when `apply_failed` has `retryable: false`.
Keep the desired Compose file or Git commit, the maniud version, the complete state and backup directories, and read-only runtime inspection output from existing operator tools.
Do not delete or rename the observed workload because that may remove the evidence needed to recover an unknown effect.

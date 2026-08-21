# Error reference

Commands write success results or failures as JSON. Default failures use a
stable code and do not expose private paths, endpoint details, credentials, or
upstream response bodies.

| Code | Exit | Meaning | Operator action |
| --- | ---: | --- | --- |
| `invalid_input` | 1 | The command grammar, Git source, or selected input is invalid. | Check `maniud COMMAND --help`, commit the complete source, and run from a clean worktree. |
| `operation_cancelled` | 130 | SIGINT, SIGTERM, or the calling context cancelled the operation. | Run the same command again. The journal selects recovery before any new effect. |
| `generation_failed` | 1 | `gen` could not validate the source, resolve the image, or publish the output. | If `retryable` is true, retry after registry or network recovery. Otherwise, inspect the source with `--debug`. |
| `apply_failed` | 1 | `apply`, `daemon`, or `doctor` could not prove a safe result. | If `retryable` is true, restore runtime, registry, or state availability and rerun. Otherwise, preserve state and collect bounded debug output. |
| `internal_error` | 1 | The selected application service is unavailable in the binary. | Verify the release artifact and report the version and output. |

`retryable: true` means unchanged input may succeed after a temporary runtime,
registry, rate-limit, or managed-state availability problem clears. A false
value includes rejected or conflicting evidence. Repeated retries do not repair
conflicting evidence.

## Debug output

Put `--debug` before the command to include bounded cause types, messages, and
maniud call sites:

```sh
maniud --debug apply --dry-run services/web.yaml
```

The encoder redacts environment values, credential-bearing URLs, authorization
headers, common secret assignments, and private keys. Review debug JSON before
posting it outside the operations team because source or runtime details that
are not secrets may remain.

## Warnings

Warnings do not change a successful command's exit status.

| Code | Delivery | Meaning |
| --- | --- | --- |
| `daemon_mount_probe_unavailable` | `warnings` in an apply or daemon result | A persistent upgrade or restore used host backup capacity checks without daemon-side mount capacity and filesystem identity proof. Phase one permits this safety downgrade for remote runtime operations. |
| `insecure_remote_engine` | JSON on stderr before Docker access | `DOCKER_HOST` uses unauthenticated plain TCP. The endpoint must be restricted by an operator-controlled VPN and firewall. |

An operation without persistent copy or restore work does not emit
`daemon_mount_probe_unavailable`.

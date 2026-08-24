# Error reference

[English](errors.md) | [简体中文](errors.zh-CN.md)

Every failed command writes one JSON object with a stable `code`, a message, and a `retryable` value.
The process exit status is 1 for a failure and 130 when the operation was cancelled.
The default result omits private paths, endpoint details, credentials, and upstream response bodies.
`gen` may print a plain-language recovery hint on standard error before the JSON object.

```json
{"code":"generation_failed","message":"generation failed","retryable":false}
```

| Code | Meaning | What to do |
| --- | --- | --- |
| `invalid_input` | The command or selected input is invalid. | Check `maniud COMMAND --help` and correct the input. |
| `operation_cancelled` | A signal or calling context cancelled the operation. | Run the same command again so maniud can recover an unfinished transaction. |
| `generation_failed` | `gen` could not validate the image, service, or output. | Follow the standard-error hint, pull the requested image when it is missing, and rerun `gen`. |
| `apply_failed` | `apply`, `daemon`, or `doctor` could not prove a safe result. | Retry only when `retryable` is true; otherwise preserve the current state and inspect `--debug` output. |
| `internal_error` | The binary could not provide the selected command service. | Verify the installed release and report its version with the JSON result. |

`retryable: true` means the same input may succeed after the runtime, registry, rate limit, or state store becomes available again.
When `retryable` is false, correct the input or resolve the conflicting evidence before repeating the command.

## Debug output

Put `--debug` before the command:

```sh
maniud --debug apply --dry-run photos.yaml
```

Debug output has a size limit and redacts environment values, credential-bearing URLs, authorization headers, secret assignments, and private keys.
It may still contain non-secret source or runtime details, so review it before sharing.

## Warnings

Warnings do not change a successful command's exit status.

| Code | Meaning |
| --- | --- |
| `daemon_mount_probe_unavailable` | A persistent copy or restore used host backup checks because the runtime could not prove capacity and filesystem identity at its mount. |
| `insecure_remote_engine` | `DOCKER_HOST` uses unauthenticated plain TCP, so the endpoint should be restricted with an operator-controlled VPN and firewall. |

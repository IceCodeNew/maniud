# Error reference

[English](errors.md) | [简体中文](errors.zh-CN.md)

Every failed command writes one JSON object with a stable `code`, a message, and a `retryable` value.
The process exit status is 1 for a failure and 130 when the operation was cancelled.
The default result omits private paths, endpoint details, credentials, and upstream response bodies.
`gen` may print a plain-language recovery hint on standard error before the JSON object.

```json
{"code":"apply_failed","message":"apply failed","retryable":false}
```

| Code | Meaning | What to do |
| --- | --- | --- |
| `invalid_input` | The command or selected input is invalid. | Check `maniud COMMAND --help` and correct the input. |
| `operation_cancelled` | A signal or calling context cancelled the operation. | Run the same command again so maniud can recover an unfinished operation. |
| `generation_failed` | `gen` could not validate the image, service, or output. | Follow the standard-error hint, pull the requested image when it is missing, and rerun `gen`. |
| `apply_failed` | `apply`, `daemon`, or `doctor` could not prove a safe result. | Retry only when `retryable` is true; otherwise preserve the current state and inspect `--debug` output. |
| `runtime_not_built` | The selected runtime was not compiled into this binary. | Install or build a maniud binary that includes the selected runtime. |
| `tui_unavailable` | `maniud tui` has no interactive terminal input, terminal output, or usable `TERM`. | Run it in an interactive terminal, or validate with `maniud apply --dry-run <compose>` and add `--json` when structured output is required. |
| `internal_error` | The binary could not provide the selected command service. | Verify the installed release and report its version with the JSON result. |

`retryable: true` means the same input may succeed after the runtime, registry, rate limit, or state store becomes available again.
When `retryable` is false, correct the input or resolve the conflicting evidence before repeating the command.

## Compose source diagnostics in the TUI

When a committed Compose source fails validation, `maniud tui` shows its repository-relative path and a stable reason. It also shows the line and column when the YAML parser provides a safe location. The detail page gives one repair action and supports scrolling when the path does not fit on screen.

The TUI does not show source values, raw YAML, parser messages, vendor errors, or absolute paths. `Position: Unavailable` means Compose validation found the problem after YAML parsing, so maniud cannot identify one trustworthy source location.

## Notification failures

Bark requires `BARK_DEVICE_KEY`. To encrypt messages, also set `BARK_ENCRYPTION_KEY` to the 16-, 24-, or 32-character ASCII key configured in the Bark app. Telegram requires both `TELEGRAM_BOT_TOKEN` and `TELEGRAM_CHAT_ID`.

An incomplete or invalid notification configuration stops `apply` or `daemon start` before the operation begins and returns `invalid_input`. Delivery problems written as `maniud notification: ...` are diagnostics: they do not change the operation result or process exit status. Do not repeat a completed operation solely because a notification failed.

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

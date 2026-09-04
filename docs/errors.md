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
| `health_pending` | The newly applied or adopted workload is present, but its health check is still converging. | Run the same command again to resume the existing health check without starting the workload again. |
| `health_degraded` | The workload is running or stopped in a state that requires a health decision. | Run `maniud tui`, review the current evidence, then confirm the health action offered for that transaction. |
| `runtime_not_built` | The selected runtime was not compiled into this binary. | Install or build a maniud binary that includes the selected runtime. |
| `tui_unavailable` | `maniud tui` has no interactive terminal input, terminal output, or usable `TERM`. | Run it in an interactive terminal, or validate with `maniud apply --dry-run <compose>` and add `--json` when structured output is required. |
| `export_failed` | The terminal was restored, but standard output did not accept the complete TUI session export. | Fix or redirect standard output, rerun `maniud tui`, and request the export again. Maniud does not retry the write. |
| `internal_error` | The binary could not provide the selected command service. | Verify the installed release and report its version with the JSON result. |

`retryable: true` means the same input may succeed after the runtime, registry, rate limit, or state store becomes available again.
When `retryable` is false, correct the input or resolve the conflicting evidence before repeating the command.

## Compose source diagnostics in the TUI

When a committed Compose source fails validation, `maniud tui` shows its repository-relative path and a stable reason. It also shows the line and column when the YAML parser provides a safe location. The detail page gives one repair action and supports scrolling when the path does not fit on screen.

The TUI does not show source values, raw YAML, parser messages, vendor errors, or absolute paths. `Position: Unavailable` means Compose validation found the problem after YAML parsing, so maniud cannot identify one trustworthy source location.

| Code | Problem and stable cause | What to do |
| --- | --- | --- |
| `compose_source_invalid` | The selected file failed one of the source checks below. | Open its diagnostic page, apply the listed repair outside Maniud, then restart `maniud tui`. |
| `compose_source_not_found` | The registered Compose file is absent from the fresh repository inventory. | Check the registered repository and relative file path, then reopen the catalog. |
| `compose_source_unavailable` | Maniud could not read enough trusted source state to open the file. | Check repository access and Git status, then restart `maniud tui`. |
| `yaml_syntax_invalid` | The YAML parser rejected the document syntax. | Fix the syntax at the displayed position, then restart `maniud tui`. |
| `yaml_structure_invalid` | The document contains duplicate keys or an invalid YAML mapping or value. | Fix the structure at the displayed position, then restart `maniud tui`. |
| `yaml_feature_unsupported` | The document uses a YAML feature that the Compose boundary cannot preserve safely. | Replace that feature with supported YAML, then restart `maniud tui`. |
| `compose_validation_failed` | The parsed document violates the supported Compose contract or lacks a required variable. | Fix the Compose fields or required variables, then restart `maniud tui`. |

## Repository setup outcomes

These codes identify recoverable failures in the setup slides. Creating a private GitHub repository uses `gh`; cloning or reusing an existing Git repository does not require GitHub.

| Code | Problem and stable cause | What to do |
| --- | --- | --- |
| `repository_setup_invalid_input` | The repository name, remote, or checkout path is invalid. | Review the repository source and checkout path before retrying. |
| `github_repository_create_failed` | `gh` did not create the requested private repository. | Check `gh auth status` and the repository name, then retry. |
| `repository_clone_failed` | Git could not clone the remote into the selected checkout. | Check remote access and choose an empty or reusable checkout path. |
| `repository_registration_failed` | The checkout exists, but Maniud could not register it as trusted desired state. | Inspect the checkout and its committed Compose sources, then retry registration. |
| `repository_setup_unavailable` | The setup role could not establish a trustworthy repository result. | Inspect the repository outside Maniud, then restart `maniud tui`. |

## LLM assistance outcomes

The TUI maps configuration and provider failures to the stable codes below. It does not display provider bodies, dependency errors, credentials, private paths, or rejected values.

After a provider request fails, the question page also shows the processing stage, effective provider/model/origin, and a bounded request outcome. **The request was not sent** means no provider transport attempt began. **The request may have been processed or billed** means an attempt began without a trusted response. **An HTTP response was received** means the provider returned a response before local validation or handling failed. Retrying either of the last two outcomes can create another billed request.

| Code | Problem and stable cause | What to do |
| --- | --- | --- |
| `llm_config_invalid` | A required provider setting is missing or outside its accepted format or range. | Review the provider, model, endpoint, timeout, and effective key. |
| `llm_config_path_invalid` | The protected XDG configuration path has a symlink, wrong owner, or unsafe mode. | Remove symlinks and restore current-user `0700`/`0600` ownership and permissions under `$XDG_CONFIG_HOME/maniud`. |
| `config_reload_failed` | Maniud could not read the current effective configuration before saving. | Fix access to the current configuration, then reload it before saving again. No configuration change was made. |
| `config_save_stale` | The effective configuration changed after the slide loaded it. | Reload the current configuration, review it, and save again. |
| `config_save_outcome_unknown` | The save returned without proof of whether the protected file was published. | Check the visible configuration and key source, then use **Retry Save**. This action does not contact the provider. |
| `config_saved_reload_failed` | The protected file was saved, but the effective values could not be reloaded. | Use **Reload LLM configuration**; do not repeat the save. |
| `llm_question_invalid` | The question is empty or violates the local text and size limits. | Shorten or correct the question before sending it again. |
| `llm_conversation_limit` | The conversation reached its turn or byte budget. | Send the question again to start a new conversation without the previous history. |
| `llm_forbidden_value` | The question contains protected deployment data. | Remove credentials, private paths, full image references, commands, ports, mounts, devices, or runtime IDs from the question. |
| `llm_authentication_failed` | The provider rejected authentication or reported a missing key. | Review the effective API key. |
| `llm_rate_limited` | The provider reported a rate or account-funds limit. | Wait before sending another billed request. |
| `llm_context_limit` | The provider rejected the request because its context was too large. | Shorten the question. |
| `llm_refused` | The provider refused or content-filtered the request. | Revise the question. Maniud does not display a provider refusal body. |
| `llm_empty_response` | The provider returned no usable choice or content. | Ask again only if another billed request is acceptable. |
| `llm_truncated` | The provider stopped because the output reached its length limit. | Ask again only if another billed request is acceptable. |
| `llm_invalid_response` | The response failed the local choice schema, field, citation, or value checks. | Revise the question or use another supported model; no candidate was created. |
| `llm_model_unavailable` | The provider could not find the configured model. | Review the model name and provider. |
| `llm_timeout` | The provider request exceeded its configured deadline. | Review the timeout and provider availability before another billed request. |
| `llm_cancelled` | The current provider request was cancelled. | Return to the question page when you want to start a new request. |
| `llm_provider_failed` | A provider or transport failure did not match a more specific category. | Check provider availability before another billed request. |
| `llm_context_stale` | The Compose source or effective provider configuration changed while the request was in flight. | Review the current Compose source and configuration before sending again. |

## Compose edit and Git outcomes

The TUI keeps a deployment draft available after recoverable validation, history, staging, or commit failures.

| Code | Problem and stable cause | What to do |
| --- | --- | --- |
| `compose_edit_precondition_failed` | The confirmed Compose source or Git state changed before the action ran. | Reload the source and review a fresh diff. |
| `compose_edit_unsupported_source` | The editor cannot preserve the selected Compose source semantics safely. | Edit the file outside Maniud, commit it, then reopen the service. |
| `compose_edit_validation_failed` | The field value or resulting Compose candidate failed local validation. | Correct the value and request a new preview. |
| `compose_edit_publish_failed` | Staging failed after Maniud restored the original Compose file. | Reload the source before retrying the edit. |
| `compose_edit_worktree_unknown` | Maniud cannot prove the published file and Git index state. | Inspect `git status --short`, `git diff`, and `git diff --staged`; recover the checkout outside Maniud, then restart `maniud tui`. |
| `git_commit_failed` | Git did not create the commit; the staged deployment edit remains unchanged. | Resolve the Git or signing failure, then retry the same commit. |
| `parameter_history_unavailable` | Git could not provide bounded first-parent history for the selected file. | Inspect the repository and reload History. |
| `parameter_history_entry_invalid` | The selected revision is no longer a valid restore source. | Reload History and select a current entry. |

`compose_edit_worktree_unknown` blocks further service or deployment file and Git mutations, LLM recommendation previews, and apply until the checkout is recovered.

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

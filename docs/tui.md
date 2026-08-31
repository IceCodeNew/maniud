# TUI guide

`maniud tui` is the interactive entry point for repository setup, service onboarding, dry-run review, and apply. It does not require separate TUI flags.

## Before starting

Prepare the following:

- A supported runtime: Docker Engine, Podman, or nerdctl with containerd.
- Git. A configured global name, email, and signing key let `maniud` create signed commits.
- Registry access for the exact image you want to use. Pull the image into the selected runtime before onboarding it.
- An interactive terminal at least 32 columns by 8 rows. The full layout uses 80 by 24 or more.

Start the interface with:

```sh
maniud tui
```

## Set up the desired-state repository

When no repository is registered, the first page proposes `$HOME/maniud-desired-state`. Edit the path if needed and press Enter to review it. Confirmation pages start on **Back**. Use Tab or an arrow key to select **Set up**, then press Enter.

The setup creates a local Git repository with a `master` branch and stores its registration under the maniud state directory. It does not create a remote. Add an `origin` remote before using the push command printed at the end of an onboarding session.

You can return to repository setup from the home page when registration is missing. Press Esc on the setup page to inspect Compose files without registering a repository.

## Add a service

Choose **Add service** from the home page. Enter either:

- An image URI with a fixed version or digest.
- A complete `docker`, `podman`, or `nerdctl` `run` or `create` command.

`maniud` parses runtime commands as configuration input. It does not execute the pasted command. The selected image must resolve to the same identity in the registry and the local runtime.

The preview shows the runtime, exact image identity, service name, Compose path, and any preparation script. Press Enter to open the write confirmation. After you select the effect, `maniud` writes only the generated files and stages their exact paths.

## Review and commit generated files

The commit page shows a bounded diff and a proposed commit message. Use:

- Up and Down to scroll the diff.
- `d` to open the complete diff view.
- `e` to edit the commit message.
- Tab, Shift+Tab, Left, or Right to switch between **Back** and **Commit**.

`maniud` first uses your global Git identity and requests a signed commit. If signing fails without changing the staged files, a separate confirmation lets you create an unsigned commit. That fallback requires an explicit selection and preserves the same staged tree.

After a successful commit, `maniud` reloads the fresh committed source. It then runs dry-run, snapshot, and evidence checks before displaying the Review page.

## Review and apply

The Review page compares the current and proposed image identities. Long identities stay in the side-by-side table and are shortened in the middle. Press `d` to inspect their full values on the Details page.

Press `e` to edit a deployment parameter. The editor lists the supported CPU, memory, process, lifecycle, and healthcheck fields with their current values. Enter a new value, or press `u` on a removable field to use the Compose default. `maniud` validates the complete in-memory Compose source before it offers a separate write-and-stage confirmation. The commit page then shows the actual staged diff and uses the same signed-commit and explicit unsigned-fallback flow as service onboarding. Committing the file does not apply it to the runtime.

Press `h` to open the 100 most recent first-parent commits that changed the selected Compose file. The history page reports whether a commit contains a signature; it does not verify the signer. Selecting an older file revision creates a fresh validated candidate and, after confirmation, a new restore commit. It does not rewrite Git history. The current file revision cannot produce a no-op restore.

### Ask for LLM deployment recommendations

Press `a` on Review to configure LLM assistance or ask a deployment question. The configuration slides support OpenAI, DeepSeek, and an OpenAI-compatible HTTPS endpoint. They collect the model, a 5–120 second per-attempt timeout, and an API key. Saving changes only `$XDG_CONFIG_HOME/maniud/.env`; it does not contact the provider.

Maniud resolves each setting from the process environment, the current directory's `.env`, then `$XDG_CONFIG_HOME/maniud/.env`. An empty API-key assignment in a higher-priority source hides lower-priority keys. Current-directory files containing an API key must be owned by the current user and must not grant group or other permissions. The XDG directory and file use modes `0700` and `0600` and reject symlinked path components.

Before sending, the confirmation page shows the provider, model, origin, and key source. The provider receives your bounded question and a deployment projection containing supported parameter fields plus limited service, runtime, platform, and action metadata. It does not receive the process environment, credentials, private paths, Compose text, runtime object IDs, or complete image references. A question containing one of those known values is rejected locally.

The request is non-streaming and may make up to three HTTP attempts. A completed response must pass strict JSON, field, citation, and Compose-value validation. When a provider returns more than one valid choice, the TUI lists up to three choices and waits for your selection. Maniud does not select a choice. The selected changes still open the normal Compose preview, stage confirmation, diff, and commit flow; the provider cannot write or commit a file directly.

If saving reports unknown durability, the TUI shows the currently visible non-secret configuration and key source. **Retry Save** repeats only the protected configuration write and does not contact the provider. Provider failures return to the question page with a stable recovery message. Retrying a provider request can incur another charge.

Use these controls on Review:

- Enter opens the apply confirmation.
- `a` opens LLM assistance.
- `e` opens the deployment parameter editor.
- `h` opens deployment history.
- `d` opens Details.
- `r` refreshes dry-run, snapshot, and evidence.
- Esc returns to service selection.
- `q` exits while no operation is running.

Apply confirmation starts on **Back**. Select **Apply** and press Enter only after reviewing the proposed runtime change. `maniud` performs one transactional apply and refreshes the evidence when it finishes.

## Open an existing service

The home page lists valid Compose files from the registered repository. Select one to build a new read-only snapshot and review its current state.

Choose **Open Compose file** to enter another committed Compose path. The file must belong to a clean Git checkout and resolve to a commit so the apply request retains source provenance.

An invalid Compose file appears as a blocked entry instead of preventing the rest of the catalog from opening. Select the blocked entry to read its stable source diagnostic. Fix the file outside the TUI, exit, and run `maniud tui` again to rebuild the catalog.

## Commands printed after exit

Some generated services require a root-owned bind-path preparation script. `maniud` never runs `sudo` inside the TUI. After the alternate screen closes, it prints the exact preparation command when one exists, followed by the Git push command and `maniud tui`.

Review each command before running it. Push requires an `origin` remote. Re-enter the TUI after preparation or push so that it reads the resulting repository state.

## Terminal behavior

The interface has these layout tiers:

| Terminal size | Behavior |
| --- | --- |
| At least 80×24 | Full step rail and side-by-side Review table |
| At least 56×16 | Compact content with the same actions |
| At least 32×8 | Minimal safe controls |
| Smaller | Resize prompt only |

Set `NO_COLOR` to disable color. Set `MANIUD_TUI_ASCII=1` to replace Unicode marks with ASCII. If standard input or output is not a TTY, or `TERM=dumb`, `maniud tui` returns `tui_unavailable` without changing files or runtime state.

For non-interactive checks, use:

```sh
maniud apply --dry-run path/to/compose.yaml
maniud apply --dry-run --json path/to/compose.yaml
```

## Cancellation and recovery

Press Ctrl+C to cancel the session. If an operation is already crossing an external boundary, `maniud` waits for it to reach a stable result before exiting. Pressing `q` during work follows the same rule.

The daemon recovers durable apply transactions from the local journal before fetching Git. A failed or invalid Compose source blocks only that service; the daemon continues processing other registered services. See [Recovery and boundaries](recovery.md) for transaction states and operator actions.

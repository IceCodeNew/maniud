# TUI guide

`maniud tui` is the interactive entry point for repository setup, service onboarding, dry-run review, and apply. It does not require separate TUI flags.

## Before starting

Prepare the following:

- A supported runtime: Docker Engine, Podman, or nerdctl with containerd.
- Git 2.41 or newer. Deployment edits use `--attr-source` to calculate the exact blob against the confirmed Git tree. A configured global name, email, and signing key let `maniud` create signed commits.
- GitHub CLI (`gh`) only when you want `maniud` to create a private GitHub repository. Install and authenticate it first; `gh auth status` must succeed. Without an authenticated `gh`, use an existing Git repository instead.
- Registry access for the exact image you want to use. Pull the image into the selected runtime before onboarding it.
- An interactive terminal at least 32 columns by 8 rows. The full layout uses 80 by 24 or more.

Start the interface with:

```sh
maniud tui
```

## Set up the desired-state repository

When no repository is registered, choose one of these sources:

- **Create private GitHub repository** accepts `OWNER/REPOSITORY`, invokes `gh`, creates the repository, and clones it to the local path you choose.
- **Use existing Git repository** accepts an HTTPS or file remote. It uses Git to clone the remote or reuse a clean checkout at the selected path. This mode does not invoke `gh` and does not require GitHub.

The local checkout page proposes `$HOME/maniud-desired-state`. Edit it if needed and press Enter to review the source and path. Confirmation pages start on **Back**. Use Tab or an arrow key to select the setup action, then press Enter. Maniud stores the completed registration under its state directory.

You can return to repository setup from the home page when registration is missing. Press Esc on the setup page to inspect Compose files without registering a repository.

## Add a service

Choose **Add service** from the home page. Enter either:

- An image URI with a fixed version or digest.
- A complete `docker`, `podman`, or `nerdctl` `run` or `create` command.

`maniud` parses runtime commands as configuration input. It does not execute the pasted command. The selected image must resolve to the same identity in the registry and the local runtime.

The preview identifies the runtime, selected image, service name, Compose path, and any preparation script. It shortens a long image identity to fit the page. Press Enter to open the write confirmation. After you select the effect, `maniud` writes only the generated files and stages their exact paths. Review the complete generated value in the staged diff before committing.

## Review and commit generated files

The commit page shows a bounded diff and a proposed commit message. Use:

- Up and Down to scroll the diff.
- `d` to open the complete diff view.
- `e` to edit the commit message.
- Tab, Shift+Tab, Left, or Right to switch between **Back** and **Commit**.

`maniud` first uses your global Git identity and requests a signed commit. If signing fails without changing the staged files, a separate confirmation lets you create an unsigned commit. That fallback requires an explicit selection and preserves the same staged tree.

After a successful commit, `maniud` reloads the fresh committed source. It then runs dry-run, snapshot, and evidence checks before displaying the Review page.

## Review and apply

The Review page compares the current and proposed image identities. Long identities stay in the side-by-side table and are shortened in the middle. Press `d` to inspect their full values and the session timeline on the Details page.

Review has two actions: **Continue to confirmation** and **Explore options**. Use Tab or Shift+Tab to move the focus and press Enter to select it. Press `o` to open **Explore options** directly. The chooser contains deterministic explanation, LLM deployment assistance, deployment parameter editing, and deployment history.

Choose **Edit deployment parameters** to open the editor. It lists the supported CPU, memory, process, lifecycle, and healthcheck fields with their current values. Enter a new value, or press `u` on a removable field to use the Compose default. A value remains an unsaved draft when you press Esc to return to the field list. Leaving the editor or quitting opens a discard confirmation; selecting **Continue editing** preserves the draft.

`maniud` validates the complete in-memory Compose source, then computes the exact Git blob and diff before it offers a write-and-stage confirmation. The preview shows changed fields in side-by-side Current and Proposed columns and shortens long values in the middle. Press `d` to open a read-only Details page with complete values. The confirmation shows a bounded diff; press `d` to open its complete read-only view. Built-in Git attribute transforms such as `text`, `eol`, `working-tree-encoding`, and `ident` are included in this calculation. External clean or process filters are rejected. After staging, `maniud` verifies that the staged blob and diff still match the confirmation. It then uses the same signed-commit and explicit unsigned-fallback flow as service onboarding. A candidate identical to HEAD is reported as unchanged and does not create a commit. Committing the file does not apply it to the runtime.

Choose **View deployment history** to open the 100 most recent first-parent commits that changed the selected Compose file. The history page reports whether a commit contains a signature; it does not verify the signer. Selecting an older file revision creates a fresh validated candidate and, after confirmation, a new restore commit. It does not rewrite Git history. The current file revision cannot produce a no-op restore.

### Ask LLM about deployment

Choose **Ask LLM about deployment** under **Explore options** to configure LLM assistance or ask a deployment question. The configuration slides support OpenAI, DeepSeek, and an OpenAI-compatible HTTPS endpoint. They collect the model, a 5–120 second per-attempt timeout, and an API key. Esc moves back one slide and keeps unsaved non-secret values. Leaving configuration or quitting opens a discard confirmation. On the API-key slide, Ctrl+D marks the saved key for deletion; a blank entry preserves the current key. Printable `q` and `c` remain ordinary text in text-entry slides. Saving changes modifies only `$XDG_CONFIG_HOME/maniud/.env`; it does not contact the provider.

When the effective configuration is complete, the flow opens the question slide. Press Ctrl+E there to edit the provider settings before sending another question.

Maniud resolves each setting from the process environment, the current directory's `.env`, then `$XDG_CONFIG_HOME/maniud/.env`. An empty API-key assignment in a higher-priority source hides lower-priority keys. Current-directory files containing an API key must be owned by the current user and must not grant group or other permissions. The XDG directory and file use modes `0700` and `0600` and reject symlinked path components.

Before sending, the confirmation page shows the provider, model, origin, and key source. The provider receives your bounded question and a deployment projection containing supported parameter fields plus limited service, runtime, platform, and action metadata. It does not receive the process environment, credentials, private paths, Compose text, runtime object IDs, or complete image references. A question containing one of those known values is rejected locally.

The request is non-streaming and may make up to three HTTP attempts. A response can answer the question, ask one follow-up question, or propose typed Compose changes. Every response must pass strict JSON and shape validation. Proposed changes also pass field, citation, and Compose-value validation. When a provider returns more than one valid response, the TUI lists up to three and waits for your selection. Maniud does not select one. An answer or follow-up question returns to the question page and enters the ephemeral conversation history. Proposed changes open the normal Compose preview, stage confirmation, diff, and commit flow; the provider cannot write or commit a file directly.

If saving reports unknown durability, the TUI shows the currently visible non-secret configuration and key source. **Retry Save** repeats only the protected configuration write and does not contact the provider. Provider failures return to the question page with a stable code, processing stage, effective provider/model/origin, request outcome, and recovery action. The outcome distinguishes a request that did not start, an attempt whose provider effect is unknown, and a received HTTP response. Retrying after an unknown or received outcome can incur another charge. Editing the question or provider configuration clears the displayed failure before the next confirmation. After eight accepted turns or the conversation text limit, resend the question to start a new conversation without the previous history.

Use these controls on Review:

- Tab or Shift+Tab moves focus between **Continue to confirmation** and **Explore options**.
- Enter selects the focused action.
- `o` opens **Explore options** directly. During health convergence, it opens health Details instead.
- `d` opens Details.
- `x` exports the current Details content and exits.
- `r` refreshes dry-run, snapshot, and evidence.
- `?` opens page-specific keyboard help.
- Esc returns to service selection.
- `q` exits while no operation is running.

Apply confirmation starts on **Back**. Select **Apply** and press Enter only after reviewing the proposed runtime change. `maniud` performs one transactional apply and refreshes the evidence when it finishes.

The Details page records application observations for this TUI session. It stores at most 128 entries or 64 KiB, whichever limit it reaches first. The page reports dropped events and marks a stopped timeline as truncated. Observations only add diagnostic context. They cannot advance a page, confirm success, or start a runtime change.

Export is available from Review and Details when no operation is running and the terminal is at least 56×16. After Bubble Tea restores the terminal, `maniud` writes one complete plain-text copy to standard output. The copy contains the full current and proposed image identities plus the bounded timeline. It omits questions, answers, candidate text, API keys, raw errors and response bodies, proxy routes, and unsaved drafts. Maniud does not persist the timeline by default.

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

An uncatchable process exit, power loss, or concurrent Git edit can leave a published file or staged change without a result that `maniud` can prove. The TUI blocks further service or deployment mutations in that session. Follow [Compose edit and Git outcomes](errors.md#compose-edit-and-git-outcomes) before restarting `maniud tui`. Keep a generated `.name.yaml.swp` file: when that draft is the only checkout change and still matches the requested service, the Add service flow offers to continue it.

The daemon recovers durable apply transactions from the local journal before fetching Git. During normal processing, a failed or invalid Compose source blocks only that service, and the daemon continues processing other registered services. If an unfinished operation is tied to that source, the daemon stops the cycle before fetching or starting another effect. See [Recovery and boundaries](recovery.md) for transaction states and operator actions.

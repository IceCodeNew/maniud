# TUI guide

`maniud tui` is the interactive entry point for repository setup, service onboarding, dry-run review, and apply. It does not require separate TUI flags.

## Before starting

Prepare the following:

- A supported runtime: Docker Engine, Podman, or nerdctl with containerd.
- Git. A configured global name, email, and signing key let `maniud` create signed commits.
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

The Review page compares the current and proposed image identities. Long identities stay in the side-by-side table and are shortened in the middle. Press `d` to inspect their full values and the session timeline on the Details page.

Use these controls on Review:

- Enter opens the apply confirmation.
- `d` opens Details.
- `x` exports the current Details content and exits.
- `r` refreshes dry-run, snapshot, and evidence.
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

An uncatchable process exit, power loss, or concurrent Git edit can leave a published file or staged change without a result that `maniud` can prove. The next TUI session blocks editing and apply while the checkout is in that state. Inspect `git status --short`, `git diff`, and `git diff --staged`, then finish or restore the repository manually. Keep a generated `.name.yaml.swp` file: when that draft is the only checkout change and still matches the requested service, the Add service flow offers to continue it.

The daemon recovers durable apply transactions from the local journal before fetching Git. A failed or invalid Compose source blocks only that service; the daemon continues processing other registered services. See [Recovery and boundaries](recovery.md) for transaction states and operator actions.

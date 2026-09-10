# TUI style contract

This document defines the visual and interaction contract for `maniud tui`.
It applies to every slide rendered inside the single Bubble Tea session.

## Regions

The full layout uses these regions:

1. A one-line header identifies maniud and shows relevant session context such
   as the project, runtime, and healthcheck state. Each fact appears once.
2. An 18-cell left rail identifies the active flow and location. A one-cell
   divider followed by a one-cell gap makes its total width 20 cells.
   The rail never shows dynamic paths, image references, or other values that
   can change its width.
3. The body owns the slide title, one short description, object identity, and
   task content. Only the body scrolls.
4. A fixed dominant-status region states the current outcome or blocker. A
   fixed action region contains at most one filled primary action.
5. A fixed footer lists only the keys available on the current slide.

The rail is read-only. Linear flows show their real steps and distinguish
completed, current, and pending steps. Non-linear pages show the flow name,
current location, and return target without inventing future steps.

## Responsive tiers

Both terminal dimensions determine the tier. If either dimension misses a
tier's minimum, the renderer uses the next smaller tier.

| Tier | Minimum size | Navigation and content |
| --- | --- | --- |
| Full | 80×24 | Left rail, comparison table, fixed status, action, and footer |
| Compact | 56×16 | Top location line instead of the rail; comparison fields may stack |
| Hard floor | 32×8 | Safety navigation, dominant status, and the next non-effect action only |
| Resize only | Below 32×8 | A request to enlarge the terminal; no task action |

Full layout keeps Current and Proposed values side by side. Compact layout may
stack the same fields when two useful columns do not fit. Hard-floor and
resize-only transitions invalidate unexecuted effect authorization, provider
tokens, and secret drafts. Returning to a larger tier starts from a fresh
review.

The status, primary action, and footer remain visible while the body scrolls.
The renderer removes optional description and metadata before it removes a
required value, blocker, or navigation action.

## Comparison and details

Review pages show changed fields only. They group deployment changes under
Resources, Lifecycle, and Health & safety when those groups apply. Empty groups
do not render.

Each Current or Proposed summary receives a terminal-cell budget. A value that
exceeds that budget uses one middle ellipsis (`…` in Unicode mode, `...` in
ASCII mode). The renderer preserves a two-thirds prefix and a one-third suffix
from the remaining cells, without splitting a terminal grapheme. It never
truncates by bytes or rune count. A budget too small for useful before-and-after
content causes a responsive-tier change instead of destructive clipping.

`d` opens a read-only Details page. Details renders the complete canonical
Current and Proposed values plus the bounded session timeline, wraps them to
the body width, and scrolls vertically. It does not use ellipsis or horizontal
scrolling and contains no mutation control. `d` or `Esc` returns to Review. A
stale projection closes Details and reloads Review before another action can
run.

The timeline holds at most 128 typed observations or 64 KiB. The first reached
limit stops appends and marks the projection as truncated. Details also reports
the process-local event drop count. Review may show one latest correlated
observation only when its operation generation, project, service, runtime,
transaction, and evidence identities match the active snapshot where present.
Stale and late observations remain in Details and exported output. Observations
never drive navigation, confirmation, mutation, or result state.

`x` on an idle Review or Details page freezes the same plain-text projection
used by Details and quits. Export is unavailable below the compact layout floor
and during an operation. The CLI writes the frozen value once to standard
output after the alternate screen is restored. Export contains no ANSI or
unsaved input and is not persisted by default.

## Status and actions

The fixed status region reports one dominant state with a precise title. Its
second line may report a separate observation problem and one recovery action.
A completed mutation remains visible if a later reload fails.

Ready status uses the shield-shaped status marker. Full Review layouts enclose
the dominant status and its supporting lines in an amber outline that spans the
body width. Compact and hard-floor layouts keep the same status concise without
the card. Other states use their own marker and text. Validation, capability,
pending, degraded, and blocked states keep distinct labels. The status region
never repeats a value already shown in the comparison.

An empty state replaces the normal status content. It names the missing object
and offers one next action; it does not render empty tables or placeholder
controls.

Normal slides initially focus the primary action or first input. Effect
confirmation slides initially focus Back or Cancel. The effect action is the
next Tab target, so Enter cannot produce an effect until the user moves focus.
Reloading a confirmation restores focus to Back or Cancel.

## Keyboard behavior

Navigation mode uses these keys:

- `Tab` and `Shift+Tab` move between focusable regions.
- Arrow keys move or scroll within the focused region.
- `Enter` activates the focused control.
- `Esc` returns or cancels the current non-settled operation.
- `d` enters or exits Details when the footer advertises it.
- `x` exports and exits when Review or Details advertises it.
- `?` opens contextual help, and `q` requests a safe quit.

Editing mode suspends letter shortcuts and labels the mode in the footer.
Effect actions have no global letter shortcut. The first quit or cancellation
request waits for an active effect to settle; a second process signal exits
immediately with status 130.

## Semantic roles

The renderer assigns style by role rather than by component:

| Role | Rich color | ANSI-16 | Monochrome cue |
| --- | --- | --- | --- |
| Brand, current location, focus, primary action, ready outline | Amber | Yellow | `>` focus prefix and brackets |
| Completed | Green | Green | `[x]` and `completed` |
| Information | Cyan | Cyan | `[i]` and state text |
| Warning | Yellow | Bright yellow | `[!]` and state text |
| Blocked or failed | Red | Red | `[FAIL]` and state text |
| Proposed value | High-contrast foreground | Bright white | `Proposed` label |
| Secondary text and pending steps | Muted foreground | Bright black | Label and shape |

Amber does not imply success by itself. Every semantic color has a marker and
text. One slide contains at most one filled primary action. The design does not
use gradients, shadows, decorative badges, icon tiles, or nested cards.

`NO_COLOR` disables ANSI styling. Focus, state, changed values, and available
actions remain distinguishable through labels, markers, spacing, and borders.

## Unicode and ASCII chrome

Unicode chrome is enabled only when the terminal reports UTF-8 and the selected
symbols occupy one cell. Unknown capability uses ASCII. Setting
`MANIUD_TUI_ASCII=1` forces ASCII.

| Meaning | Unicode | ASCII |
| --- | --- | --- |
| Completed step | `✓` | `[x]` |
| Current step | `●` | `[*]` |
| Pending step | `○` | `[ ]` |
| Step connector or divider | `│` | `|` |
| Forward action | `→` | `>` |
| Ready status | `⬟` | `[OK]` |
| Information | `●` | `[i]` |
| Warning | `▲` | `[!]` |
| Blocked or failed | `×` | `[FAIL]` |

Dynamic values use canonical terminal text. Control sequences, bidirectional
controls, and noncharacters never reach the renderer as active terminal input.
Plain command output and exported diagnostics use ASCII labels and contain no
ANSI sequences.

## Copy

Titles name the task or object. Descriptions state what the user can inspect or
do on the slide in one sentence. Status text states the effective result and
whether a runtime, file, Git, or network effect has started.

Warnings appear only when a choice changes authorization, scope, recovery, or
cost. An uncertain effect, an operation that cannot be interrupted safely, and
a retry that may incur another provider charge state that consequence directly.
The interface does not use generic reassurance such as “safe” for a result that
only proves validation or capability.

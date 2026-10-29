# review-relay

<img src="internal/server/assets/icon.svg" width="96" alt="">

A lightweight, standalone review viewer whose inline comments are shared live with local coding agents. It is the standalone counterpart of the [Review Relay](https://github.com/statiolake/vscode-review-relay) VS Code extension, meant to sit next to a terminal editor such as Vim.

- File tree and read-only preview in a native window (the OS WebView; no browser or Electron)
- Add, edit, reply to and delete comments on a line or a dragged range
- Diff mode: pass `git diff` arguments to see only changed files, inline or side by side
- Resolve threads you are done with; they fold away and agents skip them
- Per-file "reviewed" checkboxes that clear themselves when the file changes again
- Refreshes automatically while keeping the scroll position
- Agents read and write the same comments through a CLI built into the same binary
- Follows the OS light / dark appearance

## Install

```sh
go install github.com/statiolake/review-relay@latest
```

The window uses cgo, so a C toolchain is required (Xcode Command Line Tools on macOS, where the app talks to Cocoa and WKWebView directly; `gcc`, `libgtk-3-dev` and `libwebkit2gtk-4.0-dev` on Linux; MinGW-w64 on Windows).

## Usage

```sh
review-relay              # browse the files of the current repository / directory
review-relay HEAD~        # review changes since the previous commit (plus uncommitted work)
review-relay main...HEAD  # review a branch
review-relay --           # plain `git diff` (unstaged changes)
```

### zsh completion

```sh
eval "$(review-relay completion zsh)"   # after compinit, e.g. in ~/.zshrc
```

Flags and agent commands are completed by review-relay itself; everything else is completed by zsh's bundled git completion as `git diff` (revisions, `X..Y`/`X...Y`, `--cached`, `--` pathspecs).

Arguments are passed to `git diff` unchanged. When the diff's new side is the work tree, untracked files are listed as added.

| Option | Description |
| --- | --- |
| `--devtools` | Enable the web inspector |
| `--wait` | Return only once the review's window has closed (macOS; see below) |
| `--no-window` | Do not open a window; print the URL to open in a browser (every platform; no app involved) |

### macOS: one resident app

On macOS every review is a window of one Review Relay app:

- The first `review-relay` starts the app in the background, detached from the terminal (closing the terminal does not close it), opens the review's window and returns. Later invocations hand their review to the running app and return at once.
- Each review (workspace root + `git diff` arguments) has its own window, titled with the repository name and the diff. Running the same review again, from anywhere in the repository and with equivalent arguments (`--staged` = `--cached`, a trailing `--` is ignored), brings its window forward instead of opening another one. One workspace holds one review, so a different diff of an already open workspace is refused (its window comes forward); close that window first.
- `--wait` blocks until the review's window closes (also by quitting the app) and exits 0, or non-zero if the app died. Use it as an editor-style hook, e.g. in a script that continues once you are done.
- ⌘W closes the review's window: the review stops and agents no longer find it. Its comments are stored, so opening it again restores them. ⌘Q quits the app and every review.
- Closing the last window leaves the app in the Dock. Clicking the Dock icon then reopens the most recent review; **File > Open Recent** lists the last ten.
- Window positions and sizes are remembered per review.
- Each request carries the invoking shell's directory and its `GIT_*` variables (e.g. `GIT_DIR`, `GIT_WORK_TREE`), so the app runs git as that shell would.
- If the running app is another build (you updated the binary), an app with no windows open quits and the new binary starts in its place; an app with windows keeps serving and prints a note to quit it (⌘Q) to pick up the new build. An app too old to understand the request asks you to quit it.
- The app listens on `$TMPDIR/review-relay-<uid>/app.sock` (a directory only you can read) and logs to `app.log` next to it.

On Linux and Windows each `review-relay` is its own window and the command returns when that window closes, as before; `--wait` is accepted and changes nothing.

In the viewer:

- Click a line number, or drag across line numbers, to start a thread. Shift-click extends the selection. ⌘Enter (Ctrl+Enter on other platforms) submits, Esc cancels.
- In a diff, unchanged stretches are folded; click a fold to expand it. A bar in the left margin marks each thread's lines: amber while it waits for the agent, violet once the agent replied.
- **Resolve** at the bottom of a thread marks it done: it shrinks to one line, its margin bar turns gray, and it leaves the Waiting and Answered lists (All still shows it, with **Unresolve** to reopen it). A new reply, from you or the agent, reopens it.
- The right panel lists threads (Waiting / Answered / All) and holds the overall comment. **Copy agent instructions** at its bottom copies what an agent needs to connect.
- Code is syntax highlighted for common languages.

Keyboard (Vim-like normal mode; `?` shows all). `⌘` shortcuts use Ctrl on Linux and Windows; on macOS, Ctrl keeps its text-editing meaning in input fields.

| Keys | Action |
| --- | --- |
| `j` `k`, `gg` `G`, `Ctrl-d` `Ctrl-u` | Move the current line (counts work: `5j`) |
| `{` `}` | Previous / next file |
| `[` `]` | Previous / next change |
| `Ctrl-p` `Ctrl-n` | Previous / next thread across files, following the panel filter (resolved threads only under All) |
| `V` | Select lines |
| `a` | Add a thread on the current line or selection |
| `r` `e` `x` `dd` | On the current line's thread: reply / edit a comment / delete a comment / delete the whole thread (several threads or comments are numbered to choose) |
| `s` | Resolve the current line's thread, or reopen a resolved one |
| `zo` `zc` `zR` `zm` | Show / hide the thread on the current line (a resolved thread expands first); show / hide all |
| `Space` | Toggle reviewed for the file |
| `/` or `⌘F`, then `n` `p` | Find in file; Enter accepts, `n` / `p` step through matches |
| `⌘E` | Go to file (fuzzy) |

Comments always belong to lines of the current file; the diff is only a visual aid. Only one viewer runs per workspace.

App shortcuts on macOS: ⌘W closes the review, ⌘Q quits Review Relay, ⌘M minimizes; File > Open Recent reopens a past review.

## Connect a coding agent

Use **Copy Agent Instructions** and paste the result into the agent's chat. Agents use the same executable:

```text
review-relay comments list [--path PATH] [--unresolved]
review-relay comments add --path PATH --line N [--end-line N] --body TEXT
review-relay comments reply COMMENT_ID --path PATH --line N [--end-line N] --body TEXT
review-relay comments resolve COMMENT_ID
review-relay comments unresolve COMMENT_ID
review-relay comments remove COMMENT_ID
review-relay comments clear
review-relay navigate --comment COMMENT_ID
review-relay navigate --path PATH --line N [--end-line N]
```

The CLI finds the running viewer from the current directory (or `--workspace`). Paths are relative to the workspace root and line numbers are zero-based. A reply moves its whole thread to the given location and reopens it if it was resolved. A resolved thread's root comment has `"resolved": true`; `--unresolved` lists only open threads.

The HTTP API (`/health`, `/v1/comments`, `/v1/comments/:id/replies`, `/v1/comments/:id/resolved`, `/v1/navigate`) listens only on `127.0.0.1` and rejects requests from other browser origins or with a foreign `Host` header.

## Data

Reviews are stored outside the workspace, in `<user config dir>/review-relay/workspaces/<hash>/`, so they never end up in a commit. If the stored data is corrupted, the viewer refuses to start and names the file to move away.

## License

MIT; see [LICENSE](LICENSE).

The bundled [highlight.js](https://highlightjs.org/) (`internal/server/assets/highlight/`) is distributed under the BSD 3-Clause License; see the `LICENSE` file in that directory.

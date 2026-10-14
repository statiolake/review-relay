# review-relay

A lightweight, standalone review viewer whose inline comments are shared live with local coding agents. It is the standalone counterpart of the [Review Relay](https://github.com/statiolake/vscode-review-relay) VS Code extension, meant to sit next to a terminal editor such as Vim.

- File tree and read-only preview in a native window (the OS WebView; no browser or Electron)
- Add, edit, reply to and delete comments on a line or a dragged range
- Diff mode: pass `git diff` arguments to see only changed files, inline or side by side
- Per-file "reviewed" checkboxes that clear themselves when the file changes again
- Refreshes automatically while keeping the scroll position
- Agents read and write the same comments through a CLI built into the same binary
- Follows the OS light / dark appearance

## Install

```sh
go install github.com/statiolake/review-relay@latest
```

The WebView binding uses cgo, so a C toolchain is required (Xcode Command Line Tools on macOS; `gcc` and `libwebkit2gtk-4.1-dev` on Linux; MinGW-w64 on Windows).

## Usage

```sh
review-relay              # browse the files of the current repository / directory
review-relay HEAD~        # review changes since the previous commit (plus uncommitted work)
review-relay main...HEAD  # review a branch
review-relay --           # plain `git diff` (unstaged changes)
```

Arguments are passed to `git diff` unchanged. When the diff's new side is the work tree, untracked files are listed as added.

| Option | Description |
| --- | --- |
| `--devtools` | Enable the web inspector |
| `--no-window` | Do not open a window; print the URL to open in a browser |

In the viewer:

- Click a line number, or drag across line numbers, to start a thread. Shift-click extends the selection. ⌘Enter (Ctrl+Enter on other platforms) submits, Esc cancels.
- In a diff, unchanged stretches are folded; click a fold to expand it. A bar in the left margin marks each thread's lines: amber while it waits for the agent, violet once the agent replied.
- The right panel lists threads (Waiting / Answered / All) and holds the overall comment. **Copy agent instructions** at its bottom copies what an agent needs to connect.
- Code is syntax highlighted for common languages.

Keyboard (Vim-like normal mode; `?` shows all). `⌘` shortcuts use Ctrl on Linux and Windows; on macOS, Ctrl keeps its text-editing meaning in input fields.

| Keys | Action |
| --- | --- |
| `j` `k`, `gg` `G`, `Ctrl-d` `Ctrl-u` | Move the current line (counts work: `5j`) |
| `{` `}` | Previous / next file |
| `[` `]` | Previous / next change |
| `Ctrl-p` `Ctrl-n` | Previous / next thread across files, following the panel filter |
| `V` | Select lines |
| `a` | Add a thread on the current line or selection |
| `r` `e` `x` `dd` | On the current line's thread: reply / edit a comment / delete a comment / delete the whole thread (several threads or comments are numbered to choose) |
| `zo` `zc` `zR` `zm` | Show / hide the thread on the current line; show / hide all |
| `Space` | Toggle reviewed for the file |
| `/` or `⌘F`, then `n` `p` | Find in file; Enter accepts, `n` / `p` step through matches |
| `⌘E` | Go to file (fuzzy) |

Comments always belong to lines of the current file; the diff is only a visual aid. Only one viewer runs per workspace.

## Connect a coding agent

Use **Copy Agent Instructions** and paste the result into the agent's chat. Agents use the same executable:

```text
review-relay comments list [--path PATH]
review-relay comments add --path PATH --line N [--end-line N] --body TEXT
review-relay comments reply COMMENT_ID --path PATH --line N [--end-line N] --body TEXT
review-relay comments remove COMMENT_ID
review-relay comments clear
review-relay navigate --comment COMMENT_ID
review-relay navigate --path PATH --line N [--end-line N]
```

The CLI finds the running viewer from the current directory (or `--workspace`). Paths are relative to the workspace root and line numbers are zero-based. A reply moves its whole thread to the given location.

The HTTP API (`/health`, `/v1/comments`, `/v1/comments/:id/replies`, `/v1/navigate`) listens only on `127.0.0.1` and rejects requests from other browser origins or with a foreign `Host` header.

## Data

Reviews are stored outside the workspace, in `<user config dir>/review-relay/workspaces/<hash>/`, so they never end up in a commit. If the stored data is corrupted, the viewer refuses to start and names the file to move away.

## License

MIT; see [LICENSE](LICENSE).

The bundled [highlight.js](https://highlightjs.org/) (`internal/server/assets/highlight/`) is distributed under the BSD 3-Clause License; see the `LICENSE` file in that directory.

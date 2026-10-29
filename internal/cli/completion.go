package cli

import (
	"fmt"
	"io"
	"strings"
)

// Flag describes one command-line flag for shell completion. Value is the
// zsh _arguments spec of its argument (for example "path:_files"); empty for
// boolean flags.
type Flag struct {
	Name  string // without the leading dashes
	Desc  string
	Value string
}

// ViewerOptions are the flags of the viewer form (main.go).
var ViewerOptions = []Flag{
	{Name: "devtools", Desc: "enable the web inspector"},
	{Name: "wait", Desc: "return only once the review's window has closed"},
	{Name: "no-window", Desc: "do not open a window; print the viewer URL"},
}

// GlobalFlags are the agent commands' flags that precede the command.
var GlobalFlags = []Flag{
	{Name: "endpoint", Desc: "viewer API endpoint URL", Value: "url:"},
	{Name: "workspace", Desc: "directory the viewer was started in", Value: "directory:_files -/"},
}

var (
	pathFlag    = Flag{"path", "path relative to the workspace root", "path:_files"}
	lineFlag    = Flag{"line", "zero-based line number", "line:"}
	endLineFlag = Flag{"end-line", "zero-based last line of a range", "line:"}
	bodyFlag    = Flag{"body", "comment text", "text:"}
	authorFlag  = Flag{"author", "author name", "name:"}
)

type subcommand struct {
	Name, Desc string
	Arg        string // positional argument spec, if any
	Flags      []Flag
}

var commentSubcommands = []subcommand{
	{"list", "list comments", "", []Flag{pathFlag, {"unresolved", "only unresolved comments", ""}}},
	{"add", "add a comment", "", []Flag{pathFlag, lineFlag, endLineFlag, bodyFlag, authorFlag}},
	{"reply", "reply to a comment", "comment ID", []Flag{pathFlag, lineFlag, endLineFlag, bodyFlag, authorFlag}},
	{"resolve", "mark a comment resolved", "comment ID", nil},
	{"unresolve", "mark a comment unresolved", "comment ID", nil},
	{"remove", "remove a comment", "comment ID", nil},
	{"clear", "remove all comments", "", nil},
}

var topSubcommands = []subcommand{
	{"health", "check that the viewer is running", "", nil},
	{"comments", "list and edit review comments", "", nil},
	{"navigate", "move the viewer to a comment or line", "", []Flag{{"comment", "comment ID", "id:"}, pathFlag, lineFlag, endLineFlag}},
	{"completion", "print a shell completion script", "", nil},
}

// WriteCompletion writes the completion script for shell to w.
func WriteCompletion(w io.Writer, shell string) error {
	if shell != "zsh" {
		return fmt.Errorf("completion: unsupported shell %q (supported: zsh)", shell)
	}
	_, err := io.WriteString(w, zshScript())
	return err
}

func q(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// spec renders a flag as an _arguments spec.
func (f Flag) spec() string {
	s := "--" + f.Name
	if f.Value != "" {
		s += "=[" + f.Desc + "]:" + f.Value
	} else {
		s += "[" + f.Desc + "]"
	}
	return q(s)
}

// describe renders a flag as a _describe entry.
func (f Flag) describe() string { return q("--" + f.Name + ":" + f.Desc) }

func argSpecs(sc subcommand) string {
	var specs []string
	if sc.Arg != "" {
		specs = append(specs, q("1:"+sc.Arg+":"))
	}
	for _, f := range sc.Flags {
		specs = append(specs, f.spec())
	}
	return strings.Join(specs, " ")
}

func describeSubs(subs []subcommand) string {
	var out []string
	for _, s := range subs {
		out = append(out, q(s.Name+":"+s.Desc))
	}
	return strings.Join(out, " ")
}

func zshScript() string {
	var own, global []string
	for _, f := range ViewerOptions {
		own = append(own, f.describe())
	}
	own = append(own, q("--help:show usage"))
	for _, f := range GlobalFlags {
		global = append(global, f.describe())
	}
	var topCases, commentCases strings.Builder
	for _, sc := range topSubcommands {
		if sc.Name == "comments" || (sc.Flags == nil && sc.Arg == "") {
			continue
		}
		fmt.Fprintf(&topCases, "    %s) _arguments -S %s ;;\n", sc.Name, argSpecs(sc))
	}
	for _, sc := range commentSubcommands {
		if sc.Flags == nil && sc.Arg == "" {
			continue
		}
		fmt.Fprintf(&commentCases, "        %s) _arguments -S %s ;;\n", sc.Name, argSpecs(sc))
	}
	return strings.NewReplacer(
		"@OWN@", strings.Join(own, " "),
		"@GLOBAL@", strings.Join(global, " "),
		"@TOP@", describeSubs(topSubcommands),
		"@COMMENTS@", describeSubs(commentSubcommands),
		"@COMMANDS@", func() string {
			var n []string
			for _, s := range topSubcommands {
				n = append(n, s.Name)
			}
			return strings.Join(n, "|")
		}(),
		"@TOPCASES@", strings.TrimRight(topCases.String(), "\n"),
		"@COMMENTCASES@", strings.TrimRight(commentCases.String(), "\n"),
	).Replace(zshTemplate)
}

const zshTemplate = `#compdef review-relay
# zsh completion for review-relay. Usage: eval "$(review-relay completion zsh)"

_review-relay() {
  local -a own_flags global_flags top_cmds comment_cmds
  own_flags=(@OWN@)
  global_flags=(@GLOBAL@)
  top_cmds=(@TOP@)
  comment_cmds=(@COMMENTS@)

  case ${words[CURRENT-1]} in
    --endpoint|-endpoint) _message -e urls 'viewer endpoint URL'; return ;;
    --workspace|-workspace) _files -/; return ;;
  esac

  # Agent form: [--workspace PATH | --endpoint URL] COMMAND ...
  local i=2 cmd=
  while (( i < CURRENT )); do
    case ${words[i]} in
      --endpoint|--workspace|-endpoint|-workspace) (( i += 2 )); continue ;;
      --endpoint=*|--workspace=*|-endpoint=*|-workspace=*) ;;
      -*) break ;;
      *) cmd=${words[i]}; break ;;
    esac
    (( i++ ))
  done

  case $cmd in
    @COMMANDS@)
      words=("${(@)words[i,-1]}")
      (( CURRENT -= i - 1 ))
      case $cmd in
        comments)
          if (( CURRENT == 2 )); then
            _describe -t commands 'comments command' comment_cmds
            return
          fi
          local sub=${words[2]}
          words=("${(@)words[2,-1]}")
          (( CURRENT-- ))
          case $sub in
@COMMENTCASES@
          esac ;;
@TOPCASES@
      esac
      return ;;
  esac

  # Viewer form: review-relay [OPTIONS] [--] DIFF-ARGS, completed as git diff.
  local j viewer_opts=1
  for (( j = 2; j < CURRENT; j++ )); do
    [[ ${words[j]} == -(devtools|wait|no-window|-devtools|-wait|-no-window) ]] || { viewer_opts=0; break; }
  done
  if (( i == CURRENT )) && [[ ${words[CURRENT]} != -* ]]; then
    _describe -t commands 'agent command' top_cmds
  fi
  if [[ -z ${words[CURRENT]} || ${words[CURRENT]} == -* ]]; then
    if (( viewer_opts )); then
      _describe -t options 'review-relay option' own_flags
    fi
    if (( i == CURRENT )); then
      _describe -t options 'agent option' global_flags
    fi
  fi

  words=(git diff "${(@)words[2,-1]}")
  (( CURRENT++ ))
  # service must stay "git": _git only dispatches "diff" to _git-diff (with
  # words shifted so that options keep completing) on that path.
  service=git
  _git
}

compdef _review-relay review-relay
`

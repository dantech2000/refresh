package commands

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

const bashCompletionScript = `#!/bin/bash
# bash completion for refresh.
# Install: refresh completion bash > /usr/local/etc/bash_completion.d/refresh
_refresh_bash_autocomplete() {
  if [[ "${COMP_WORDS[0]}" != "source" ]]; then
    local cur opts
    COMPREPLY=()
    cur="${COMP_WORDS[COMP_CWORD]}"
    if [[ "$cur" == "-"* ]]; then
      opts=$("${COMP_WORDS[@]:0:$COMP_CWORD}" "${cur}" --generate-bash-completion 2>/dev/null)
    else
      opts=$("${COMP_WORDS[@]:0:$COMP_CWORD}" --generate-bash-completion 2>/dev/null)
    fi
    COMPREPLY=($(compgen -W "${opts}" -- "${cur}"))
    return 0
  fi
}
complete -o bashdefault -o default -o nospace -F _refresh_bash_autocomplete refresh
`

const zshCompletionScript = `#compdef refresh
# zsh completion for refresh.
# Install: refresh completion zsh > "${fpath[1]}/_refresh"
_refresh() {
  local -a opts
  local cur
  cur=${words[-1]}
  if [[ "$cur" == "-"* ]]; then
    opts=("${(@f)$(${(@)words[1,-2]} ${cur} --generate-bash-completion 2>/dev/null)}")
  else
    opts=("${(@f)$(${(@)words[1,-2]} --generate-bash-completion 2>/dev/null)}")
  fi
  if [[ -n "${opts[1]}" ]]; then
    _describe 'values' opts
  else
    _files
  fi
}
compdef _refresh refresh
`

// CompletionCommand returns the `completion` command, which prints a shell
// completion script for bash, zsh, or fish.
func CompletionCommand() *cli.Command {
	return &cli.Command{
		Name:      "completion",
		Usage:     "Output shell completion script (bash, zsh, or fish)",
		ArgsUsage: "<bash|zsh|fish>",
		Description: `Generate a shell completion script for refresh.

Examples:
   # bash (add to ~/.bashrc or a bash_completion.d directory)
   source <(refresh completion bash)

   # zsh (write somewhere on your $fpath)
   refresh completion zsh > "${fpath[1]}/_refresh"

   # fish
   refresh completion fish > ~/.config/fish/completions/refresh.fish`,
		Action: func(c *cli.Context) error {
			switch c.Args().First() {
			case "bash":
				_, _ = fmt.Fprint(c.App.Writer, bashCompletionScript)
			case "zsh":
				_, _ = fmt.Fprint(c.App.Writer, zshCompletionScript)
			case "fish":
				script, err := c.App.ToFishCompletion()
				if err != nil {
					return fmt.Errorf("generating fish completion: %w", err)
				}
				_, _ = fmt.Fprintln(c.App.Writer, script)
			default:
				return fmt.Errorf("unsupported or missing shell %q (supported: bash, zsh, fish)", c.Args().First())
			}
			return nil
		},
	}
}

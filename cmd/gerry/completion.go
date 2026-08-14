package main

import (
	"fmt"
	"os"
)

// gerry completion — static shell completions for subcommands and their
// flags. Kept handwritten and boring on purpose: the CLI surface is small
// and the stdlib flag package has no reflection-friendly registry.

var completionSpec = map[string][]string{
	"serve":      {"--config"},
	"claim":      {"--zone", "--label", "--owner", "--kind", "--hold", "--port-pool"},
	"port":       {"--owner", "--pool", "-q"},
	"avail":      {"--zone", "--label"},
	"ls":         {"--zone", "--owner"},
	"release":    {"--id"},
	"rename":     {"--id", "--label"},
	"zone":       {"add"},
	"run":        {"--owner", "--pool"},
	"dev":        {"-f"},
	"init":       {"--name", "--zone"},
	"status":     {},
	"service":    {"install", "uninstall", "status", "restart"},
	"up":         {"-f"},
	"down":       {"-f"},
	"conflicts":  {},
	"mcp":        {},
	"ca-export":  {"--dir"},
	"completion": {"bash", "zsh", "fish"},
	"version":    {},
}

func cmdCompletion(args []string) error {
	shell := ""
	if len(args) > 0 {
		shell = args[0]
	}
	subs := ""
	for s := range completionSpec {
		subs += s + " "
	}
	switch shell {
	case "bash":
		fmt.Printf(`_gerry() {
  local cur prev subs
  cur="${COMP_WORDS[COMP_CWORD]}"
  subs="%s"
  if [ $COMP_CWORD -eq 1 ]; then
    COMPREPLY=($(compgen -W "$subs" -- "$cur")); return
  fi
  case "${COMP_WORDS[1]}" in
`, subs)
		for sub, flags := range completionSpec {
			fmt.Printf("    %s) COMPREPLY=($(compgen -W \"%s\" -- \"$cur\"));;\n", sub, join(flags))
		}
		fmt.Print(`  esac
}
complete -F _gerry gerry
`)
	case "zsh":
		fmt.Printf(`#compdef gerry
_gerry() {
  local -a subs
  subs=(%s)
  if (( CURRENT == 2 )); then
    _describe 'command' subs; return
  fi
  case "$words[2]" in
`, subs)
		for sub, flags := range completionSpec {
			fmt.Printf("    %s) _arguments '*:flag:(%s)';;\n", sub, join(flags))
		}
		fmt.Print(`  esac
}
_gerry "$@"
`)
	case "fish":
		for sub, flags := range completionSpec {
			fmt.Printf("complete -c gerry -n '__fish_use_subcommand' -a '%s'\n", sub)
			for _, f := range flags {
				fmt.Printf("complete -c gerry -n '__fish_seen_subcommand_from %s' -a '%s'\n", sub, f)
			}
		}
	default:
		fmt.Fprintln(os.Stderr, `usage: gerry completion bash|zsh|fish
  bash: gerry completion bash >> ~/.bashrc            (or bash-completion dir)
  zsh:  gerry completion zsh > "${fpath[1]}/_gerry"
  fish: gerry completion fish > ~/.config/fish/completions/gerry.fish`)
		os.Exit(2)
	}
	return nil
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}

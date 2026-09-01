// Package cli owns command dispatch and human diagnostics.
// Machine-readable run results are intentionally not emitted here yet.
package cli

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/Ameb8/agent-run/internal/contract"
)

type Command struct {
	Path    []string
	Execute func(args []string) error
}

type App struct {
	commands []Command
	stderr   io.Writer
}

func New(stderr io.Writer) *App {
	return &App{stderr: stderr, commands: registeredCommands()}
}

// Run dispatches a command. Diagnostics only ever go to stderr; stdout is
// reserved for the single JSON run result introduced by the runtime layer.
func (a *App) Run(args []string) int {
	command, commandArgs, ok := a.find(args)
	if !ok {
		a.diagnostic("unknown command")
		return 1
	}
	if err := command.Execute(commandArgs); err != nil {
		a.diagnostic(err.Error())
		return 1
	}
	return 0
}

func (a *App) find(args []string) (Command, []string, bool) {
	for _, command := range a.commands {
		if len(args) < len(command.Path) || !equalPath(args[:len(command.Path)], command.Path) {
			continue
		}
		return command, args[len(command.Path):], true
	}
	return Command{}, nil, false
}

func (a *App) diagnostic(message string) {
	_, _ = fmt.Fprintf(a.stderr, "agentrun: %s\n", message)
}

func equalPath(left, right []string) bool {
	return strings.Join(left, "\x00") == strings.Join(right, "\x00")
}

func registeredCommands() []Command {
	return []Command{
		stub("run"),
		stub("list"),
		stub("validate"),
		stub("inspect"),
		stub("auth", "login", "openai-subscription"),
		stub("auth", "logout", "openai-subscription"),
		stub("version"),
		stub("doctor"),
	}
}

func stub(path ...string) Command {
	return Command{
		Path: path,
		Execute: func(_ []string) error {
			return &contract.CommandError{
				Category: contract.ErrorConfiguration,
				Message:  "command is not implemented",
			}
		},
	}
}

// IsCommandError lets future command handlers distinguish handled failures
// without matching user-facing diagnostic text.
func IsCommandError(err error) (*contract.CommandError, bool) {
	var commandErr *contract.CommandError
	return commandErr, errors.As(err, &commandErr)
}

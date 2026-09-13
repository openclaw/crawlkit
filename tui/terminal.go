package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/term"
	"github.com/mattn/go-isatty"
)

func Run(ctx context.Context, opts Options) error {
	if opts.Stdin == nil {
		opts.Stdin = os.Stdin
	}
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if len(opts.Items) == 0 {
		msg := opts.EmptyMessage
		if msg == "" {
			msg = "no rows"
		}
		_, err := fmt.Fprintln(opts.Stdout, msg)
		return err
	}
	input, ok := opts.Stdin.(*os.File)
	if !ok || !isatty.IsTerminal(input.Fd()) {
		return ErrNotTerminal
	}
	output, ok := opts.Stdout.(*os.File)
	if !ok || !isatty.IsTerminal(output.Fd()) {
		return ErrNotTerminal
	}
	defer restoreTerminalOutput(output)
	model := newModel(opts)
	if width, height, ok := terminalSize(input, output); ok {
		model.width = width
		model.height = height
		model.ensureVisible()
	} else {
		model.height = 12
	}
	runCtx, stopSignals := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer stopSignals()
	model.ctx = runCtx
	program := tea.NewProgram(
		model,
		tea.WithContext(runCtx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
		tea.WithMouseAllMotion(),
	)
	done := make(chan struct{})
	go func() {
		select {
		case <-runCtx.Done():
			program.Kill()
		case <-done:
		}
	}()
	_, err := program.Run()
	close(done)
	if errors.Is(err, tea.ErrProgramKilled) && runCtx.Err() != nil {
		return nil
	}
	return err
}

func restoreTerminalOutput(output io.Writer) {
	if output == nil {
		return
	}
	_, _ = io.WriteString(output, terminalRestoreSequence)
}

func terminalSize(input, output *os.File) (int, int, bool) {
	for _, file := range []*os.File{output, input} {
		if file == nil {
			continue
		}
		width, height, err := term.GetSize(file.Fd())
		if err == nil && width > 0 && height > 0 {
			return width, height, true
		}
	}
	return 0, 0, false
}

package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/owainlewis/machinist/internal/config"
	"github.com/owainlewis/machinist/internal/managedworker"
	"github.com/spf13/cobra"
)

func newHerdrCommand(options *commandOptions) *cobra.Command {
	herdr := &cobra.Command{Use: "herdr", Short: "Submit and track interactive work in Herdr"}
	herdr.AddCommand(newHerdrSubmitCommand(options))
	herdr.AddCommand(newHerdrPickerCommand(options))
	herdr.AddCommand(newHerdrWatchCommand(options))
	return herdr
}

func newHerdrSubmitCommand(options *commandOptions) *cobra.Command {
	var commandName, prompt, model, repository string
	submit := &cobra.Command{
		Use: "submit", Short: "Queue an interactive Herdr workflow", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return submitSelectionWithMode(command.Context(), options, commandName, prompt, model, repository, "herdr", "herdr-plugin")
		},
	}
	submit.Flags().StringVar(&commandName, "command", "", "command name from the control plane")
	submit.Flags().StringVar(&prompt, "prompt", "", "work request")
	submit.Flags().StringVar(&model, "model", "", "model or configured alias")
	submit.Flags().StringVar(&repository, "repo", "", "configured repository name")
	_ = submit.MarkFlagRequired("command")
	_ = submit.MarkFlagRequired("prompt")
	_ = submit.MarkFlagRequired("repo")
	return submit
}

func newHerdrPickerCommand(options *commandOptions) *cobra.Command {
	return &cobra.Command{
		Use: "picker", Short: "Choose and submit an interactive workflow", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error { return runHerdrPicker(command.Context(), options) },
	}
}

func runHerdrPicker(ctx context.Context, options *commandOptions) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	client, err := managedworker.NewClient(worker)
	if err != nil {
		return err
	}
	var catalog submitCatalog
	if err := client.Get(ctx, "/api/v1/catalog", &catalog); err != nil {
		return fmt.Errorf("read control plane catalog: %w", err)
	}
	if len(catalog.Commands) == 0 || len(catalog.Repositories) == 0 {
		return errors.New("no commands or repositories are currently registered")
	}
	reader := bufio.NewReader(options.stdin)
	fmt.Fprintln(options.stdout, "Machinist × Herdr — new interactive workflow")
	fmt.Fprintln(options.stdout, "The agent opens in an editable Herdr workspace and remains visible after completion.")
	fmt.Fprintln(options.stdout)
	defaultCommand := preferred(catalog.Commands, "foreman")
	if slices.Contains(catalog.Commands, "implement") {
		defaultCommand = "implement"
	}
	commandName, err := chooseValue(reader, options.stdout, "Command", catalog.Commands, defaultCommand)
	if err != nil {
		return err
	}
	repository, err := chooseValue(reader, options.stdout, "Repository", catalog.Repositories, catalog.Repositories[0])
	if err != nil {
		return err
	}
	fmt.Fprint(options.stdout, "Model/alias (blank uses profile default): ")
	model, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	fmt.Fprintln(options.stdout, "Paste the task. Finish with a line containing only a single period (.).")
	var promptLines []string
	for {
		line, readErr := reader.ReadString('\n')
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if line == "." {
			break
		}
		if line != "" || readErr == nil {
			promptLines = append(promptLines, line)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	prompt := strings.TrimSpace(strings.Join(promptLines, "\n"))
	if prompt == "" {
		return errors.New("task prompt cannot be empty")
	}
	return submitSelectionWithMode(ctx, options, commandName, prompt, strings.TrimSpace(model), repository, "herdr", "herdr-plugin")
}

func chooseValue(reader *bufio.Reader, output io.Writer, label string, values []string, defaultValue string) (string, error) {
	for index, value := range values {
		fmt.Fprintf(output, "  %d. %s\n", index+1, value)
	}
	fmt.Fprintf(output, "%s [%s]: ", label, defaultValue)
	text, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return defaultValue, nil
	}
	if number, parseErr := strconv.Atoi(text); parseErr == nil && number >= 1 && number <= len(values) {
		return values[number-1], nil
	}
	if slices.Contains(values, text) {
		return text, nil
	}
	return "", fmt.Errorf("unknown %s %q", strings.ToLower(label), text)
}

func preferred(values []string, wanted string) string {
	if slices.Contains(values, wanted) {
		return wanted
	}
	return values[0]
}

type herdrStatus struct {
	Jobs []struct {
		ID            string `json:"id"`
		Repository    string `json:"repository"`
		Command       string `json:"command"`
		ExecutionMode string `json:"execution_mode"`
		Origin        string `json:"origin"`
		State         string `json:"state"`
		Runs          []struct {
			State      string `json:"state"`
			WorkerName string `json:"worker_name"`
			Attempts   []struct {
				Terminal *struct {
					Session     string `json:"session"`
					WorkspaceID string `json:"workspace_id"`
					PaneID      string `json:"pane_id"`
					AgentName   string `json:"agent_name"`
				} `json:"terminal"`
			} `json:"attempts"`
		} `json:"runs"`
	} `json:"jobs"`
	Workers []struct {
		Name       string   `json:"name"`
		Connected  bool     `json:"connected"`
		Transports []string `json:"transports"`
	} `json:"workers"`
}

func newHerdrWatchCommand(options *commandOptions) *cobra.Command {
	var once bool
	watch := &cobra.Command{
		Use: "watch", Short: "Show the Machinist task board in a Herdr pane", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error { return watchHerdr(command.Context(), options, once) },
	}
	watch.Flags().BoolVar(&once, "once", false, "render one snapshot and exit")
	return watch
}

func watchHerdr(ctx context.Context, options *commandOptions, once bool) error {
	worker, err := config.LoadWorker(options.configPath)
	if err != nil {
		return err
	}
	client, err := managedworker.NewClient(worker)
	if err != nil {
		return err
	}
	for {
		var status herdrStatus
		if err := client.Get(ctx, "/api/v1/status", &status); err != nil {
			return err
		}
		if !once {
			fmt.Fprint(options.stdout, "\x1b[2J\x1b[H")
		}
		renderHerdrStatus(options.stdout, status)
		if once {
			return nil
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func renderHerdrStatus(output io.Writer, status herdrStatus) {
	fmt.Fprintln(output, "MACHINIST TASKS  ·  live Herdr view")
	fmt.Fprintln(output, "STATE       COMMAND          REPOSITORY               TERMINAL")
	shown := 0
	for _, job := range status.Jobs {
		if job.ExecutionMode != "herdr" && job.Origin != "herdr-plugin" {
			continue
		}
		terminal := "waiting for Herdr worker"
		if len(job.Runs) > 0 && len(job.Runs[0].Attempts) > 0 {
			binding := job.Runs[0].Attempts[len(job.Runs[0].Attempts)-1].Terminal
			if binding != nil {
				terminal = fmt.Sprintf("%s / %s / %s", binding.Session, binding.WorkspaceID, binding.AgentName)
			}
		}
		fmt.Fprintf(output, "%-11s %-16s %-24s %s\n", job.State, job.Command, job.Repository, terminal)
		shown++
	}
	if shown == 0 {
		fmt.Fprintln(output, "No Herdr workflows yet. Use the ‘New Machinist workflow’ action.")
	}
	fmt.Fprintf(output, "\nWorkers: ")
	for _, worker := range status.Workers {
		if slices.Contains(worker.Transports, "herdr") {
			fmt.Fprintf(output, "%s=%t ", worker.Name, worker.Connected)
		}
	}
	fmt.Fprintln(output, "\nRefresh: 2s · Ctrl-C closes this view")
}

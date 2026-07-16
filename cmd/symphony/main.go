// Command symphony is the Symphony daemon entrypoint and host lifecycle
// (SPEC §17.7). It wires the concrete adapters (the composition root) and runs
// the orchestrator until interrupted.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tomi/my-symphony/internal/agent"
	"github.com/tomi/my-symphony/internal/agent/claude"
	"github.com/tomi/my-symphony/internal/config"
	"github.com/tomi/my-symphony/internal/domain"
	"github.com/tomi/my-symphony/internal/httpserver"
	"github.com/tomi/my-symphony/internal/logging"
	"github.com/tomi/my-symphony/internal/orchestrator"
	"github.com/tomi/my-symphony/internal/status"
	"github.com/tomi/my-symphony/internal/tracker"
	"github.com/tomi/my-symphony/internal/tracker/linear"
	"github.com/tomi/my-symphony/internal/watcher"
	"github.com/tomi/my-symphony/internal/workflow"
	"github.com/tomi/my-symphony/internal/workspace"
)

func main() {
	os.Exit(run())
}

func run() int {
	port := flag.Int("port", -1, "HTTP server port (overrides server.port; -1 disables unless server.port is set)")
	showStatus := flag.Bool("status", false, "render a periodic terminal status surface")
	flag.Parse()

	logger := logging.New()

	// Resolve workflow path: explicit positional arg, else ./WORKFLOW.md (SPEC §5.1).
	workflowPath := "WORKFLOW.md"
	if args := flag.Args(); len(args) > 0 {
		workflowPath = args[0]
	}
	absPath, err := filepath.Abs(workflowPath)
	if err != nil {
		logger.Error("resolve workflow path failed", "outcome", "failed", "error", err.Error())
		return 1
	}

	def, err := workflow.Load(absPath)
	if err != nil {
		logger.Error("workflow load failed", "outcome", "failed", "error", err.Error())
		return 1
	}
	cfg, err := config.New(def.Config, filepath.Dir(absPath))
	if err != nil {
		logger.Error("config build failed", "outcome", "failed", "error", err.Error())
		return 1
	}

	factories := buildFactories(logger)

	orch, err := orchestrator.New(cfg, def.PromptTemplate, factories, logger)
	if err != nil {
		logger.Error("orchestrator init failed", "outcome", "failed", "error", err.Error())
		return 1
	}

	// Root context cancelled on SIGINT/SIGTERM (SPEC §17.7).
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Workflow watch (SPEC §6.2).
	w := watcher.New(absPath, orch.Events(), logger)
	go w.Run(ctx)

	// Optional terminal status surface (SPEC §13.4).
	if *showStatus {
		surface := status.New(orch, os.Stdout, 5*time.Second)
		go surface.Run(ctx)
	}

	// Optional HTTP server: CLI --port overrides server.port (SPEC §13.7).
	if effPort, enabled := resolvePort(*port, cfg); enabled {
		srv := httpserver.New(orch, logger)
		if err := srv.Start(ctx, effPort); err != nil {
			logger.Error("http server start failed", "outcome", "failed", "error", err.Error())
			return 1
		}
	}

	if err := orch.Run(ctx); err != nil {
		logger.Error("orchestrator exited abnormally", "outcome", "failed", "error", err.Error())
		return 1
	}
	logger.Info("shutdown complete", "outcome", "completed")
	return 0
}

// resolvePort applies CLI-over-config precedence for the HTTP extension (SPEC §13.7).
func resolvePort(cliPort int, cfg *config.Config) (int, bool) {
	if cliPort >= 0 {
		return cliPort, true
	}
	if cfg.Server.PortSet {
		return cfg.Server.Port, true
	}
	return 0, false
}

// buildFactories constructs the composition-root factories that build effective
// adapters from a config, so dynamic reload can re-apply them (SPEC §3.2, §6.2).
func buildFactories(logger *logging.Logger) orchestrator.Factories {
	return orchestrator.Factories{
		Tracker: func(cfg *config.Config) (tracker.Client, error) {
			return linear.New(linear.Options{
				Endpoint:     cfg.Tracker.Endpoint,
				APIKey:       cfg.Tracker.APIKey,
				ProjectSlug:  cfg.Tracker.ProjectSlug,
				ActiveStates: cfg.Tracker.ActiveStates,
			})
		},
		Workspace: func(cfg *config.Config) orchestrator.Workspace {
			return workspace.New(cfg.Workspace.Root, workspace.Hooks{
				AfterCreate:  cfg.Hooks.AfterCreate,
				BeforeRun:    cfg.Hooks.BeforeRun,
				AfterRun:     cfg.Hooks.AfterRun,
				BeforeRemove: cfg.Hooks.BeforeRemove,
				TimeoutMs:    cfg.Hooks.TimeoutMs,
			}, logger)
		},
		Runner: func(cfg *config.Config, template string, ws orchestrator.Workspace, tr tracker.Client, issue domain.Issue) agent.Runner {
			wm, ok := ws.(agent.WorkspaceManager)
			if !ok {
				// The concrete workspace.Manager satisfies agent.WorkspaceManager;
				// this guards against a mismatched factory wiring.
				panic("workspace does not implement agent.WorkspaceManager")
			}
			// Resolve per-status overrides from the dispatched issue's state
			// (SPEC §5.3.7). Unset overrides fall back to the global values.
			state := issue.State
			client := claude.NewClient(claude.Config{
				Command:           cfg.Claude.Command,
				Model:             cfg.ModelForState(state),
				ReasoningEffort:   cfg.ReasoningEffortForState(state),
				ResumeAcrossTurns: cfg.Claude.ResumeAcrossTurns,
				TurnTimeoutMs:     cfg.Claude.TurnTimeoutMs,
				ReadTimeoutMs:     cfg.Claude.ReadTimeoutMs,
			})
			return agent.NewRunner(agent.RunnerConfig{
				Workspace:      wm,
				Backend:        claude.NewBackend(client),
				Tracker:        tr,
				Template:       cfg.PromptForState(state, template),
				PromptOverride: cfg.HasPromptOverride(state),
				ActiveStates:   cfg.Tracker.ActiveStates,
				MaxTurns:       cfg.MaxTurnsForState(state),
				Logger:         logger,
			})
		},
	}
}

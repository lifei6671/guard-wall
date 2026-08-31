// Package logging owns process-local logging configuration.
package logging

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/lifei6671/guard-wall/internal/config"
)

// ErrRestartRequired reports a reload that changes any restart-bound YAML
// field. The Runtime remains unchanged when this error is returned.
var ErrRestartRequired = errors.New("logging reload rejected: process restart required")

// Runtime atomically publishes the active logging level and owns the last
// successfully applied Phase 1 configuration snapshot. It implements
// slog.Leveler and can be passed directly to slog.HandlerOptions.Level.
type Runtime struct {
	mu      sync.Mutex
	current config.Config
	level   slog.LevelVar
}

// NewRuntime creates a logging runtime from a schema-validated configuration.
func NewRuntime(initial config.Config) (*Runtime, error) {
	level, err := parseLevel(initial.Logging.Level)
	if err != nil {
		return nil, fmt.Errorf("new logging runtime: %w", err)
	}

	runtime := &Runtime{current: initial}
	runtime.level.Set(level)
	return runtime, nil
}

// Level returns the currently active slog level.
func (r *Runtime) Level() slog.Level {
	return r.level.Level()
}

// Reload atomically applies logging.level when every other Phase 1 YAML field
// is unchanged. A restart-bound change rejects the complete candidate so a new
// logging level cannot become partially active.
func (r *Runtime) Reload(next config.Config) error {
	level, err := parseLevel(next.Logging.Level)
	if err != nil {
		return fmt.Errorf("reload logging: %w", err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	restartCandidate := next
	restartCandidate.Logging.Level = r.current.Logging.Level
	if restartCandidate != r.current {
		return ErrRestartRequired
	}

	r.level.Set(level)
	r.current = next
	return nil
}

func parseLevel(value string) (slog.Level, error) {
	switch value {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("unsupported logging level %q", value)
	}
}

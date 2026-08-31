package logging

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lifei6671/guard-wall/internal/config"
)

func TestNewRuntimeLevels(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  slog.Level
	}{
		{name: "debug", value: "debug", want: slog.LevelDebug},
		{name: "info", value: "info", want: slog.LevelInfo},
		{name: "warn", value: "warn", want: slog.LevelWarn},
		{name: "error", value: "error", want: slog.LevelError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			initial := validConfig()
			initial.Logging.Level = test.value
			runtime, err := NewRuntime(initial)
			if err != nil {
				t.Fatalf("NewRuntime(): %v", err)
			}
			if got := runtime.Level(); got != test.want {
				t.Fatalf("Level() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestNewRuntimeRejectsInvalidLevel(t *testing.T) {
	for _, level := range []string{"", "Info", "trace"} {
		t.Run(level, func(t *testing.T) {
			initial := validConfig()
			initial.Logging.Level = level
			if _, err := NewRuntime(initial); err == nil {
				t.Fatal("NewRuntime() error = nil, want invalid level")
			}
		})
	}
}

func TestRuntimeProvidesSlogLeveler(t *testing.T) {
	policy, err := config.LookupFieldPolicy("logging.level")
	if err != nil {
		t.Fatalf("LookupFieldPolicy(): %v", err)
	}
	if !policy.HotReload || policy.RestartRequired {
		t.Fatalf("logging.level policy = %+v, want hot reload only", policy)
	}

	runtime := newTestRuntime(t)
	handler := slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: runtime})
	ctx := context.Background()

	if handler.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("debug unexpectedly enabled at info")
	}
	if !handler.Enabled(ctx, slog.LevelInfo) {
		t.Fatal("info unexpectedly disabled")
	}

	next := validConfig()
	next.Logging.Level = "debug"
	if err := runtime.Reload(next); err != nil {
		t.Fatalf("Reload(): %v", err)
	}
	if !handler.Enabled(ctx, slog.LevelDebug) {
		t.Fatal("debug was not enabled after reload")
	}
}

func TestRuntimeReloadsLoggingLevel(t *testing.T) {
	runtime := newTestRuntime(t)
	tests := []struct {
		value string
		want  slog.Level
	}{
		{value: "debug", want: slog.LevelDebug},
		{value: "warn", want: slog.LevelWarn},
		{value: "error", want: slog.LevelError},
		{value: "info", want: slog.LevelInfo},
	}

	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			next := validConfig()
			next.Logging.Level = test.value
			if err := runtime.Reload(next); err != nil {
				t.Fatalf("Reload(): %v", err)
			}
			if got := runtime.Level(); got != test.want {
				t.Fatalf("Level() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestRuntimeRejectsEveryRestartBoundField(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		mutate func(*config.Config)
	}{
		{name: "schema version", path: "schema_version", mutate: func(value *config.Config) { value.SchemaVersion++ }},
		{name: "raw queue capacity", path: "runtime.raw_queue_capacity", mutate: func(value *config.Config) { value.Runtime.RawQueueCapacity++ }},
		{name: "event queue capacity", path: "runtime.event_queue_capacity", mutate: func(value *config.Config) { value.Runtime.EventQueueCapacity++ }},
		{name: "reconcile queue capacity", path: "runtime.reconcile_queue_capacity", mutate: func(value *config.Config) { value.Runtime.ReconcileQueueCapacity++ }},
		{name: "shutdown timeout", path: "runtime.shutdown_timeout", mutate: func(value *config.Config) { value.Runtime.ShutdownTimeout++ }},
		{name: "checkpoint interval", path: "source.checkpoint_interval", mutate: func(value *config.Config) { value.Source.CheckpointInterval++ }},
		{name: "checkpoint record threshold", path: "source.checkpoint_record_threshold", mutate: func(value *config.Config) { value.Source.CheckpointRecordThreshold++ }},
		{name: "database path", path: "store.database_path", mutate: func(value *config.Config) { value.Store.DatabasePath = "/var/lib/guard/next.db" }},
		{name: "listen address", path: "web.listen_address", mutate: func(value *config.Config) { value.Web.ListenAddress = "127.0.0.1:9090" }},
		{name: "allow remote HTTP", path: "web.security.allow_remote_http", mutate: func(value *config.Config) { value.Web.Security.AllowRemoteHTTP = true }},
		{name: "SMTP credential file", path: "smtp.credential_file", mutate: func(value *config.Config) { value.SMTP.CredentialFile = "/run/guard/next-secret" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy, err := config.LookupFieldPolicy(test.path)
			if err != nil {
				t.Fatalf("LookupFieldPolicy(%q): %v", test.path, err)
			}
			if policy.HotReload || !policy.RestartRequired {
				t.Fatalf("%s policy = %+v, want restart only", test.path, policy)
			}

			runtime := newTestRuntime(t)
			next := validConfig()
			next.Logging.Level = "debug"
			test.mutate(&next)

			err = runtime.Reload(next)
			if !errors.Is(err, ErrRestartRequired) {
				t.Fatalf("Reload() error = %v, want ErrRestartRequired", err)
			}
			if got := runtime.Level(); got != slog.LevelInfo {
				t.Fatalf("Level() = %v after rejected reload, want info", got)
			}
			if strings.Contains(err.Error(), next.SMTP.CredentialFile) {
				t.Fatal("restart error exposed a configuration value")
			}
		})
	}
}

func TestRuntimeRejectedReloadDoesNotAdvanceSnapshot(t *testing.T) {
	runtime := newTestRuntime(t)
	rejected := validConfig()
	rejected.Store.DatabasePath = "/var/lib/guard/rejected.db"
	rejected.Logging.Level = "debug"
	if err := runtime.Reload(rejected); !errors.Is(err, ErrRestartRequired) {
		t.Fatalf("rejected Reload() error = %v, want ErrRestartRequired", err)
	}

	accepted := validConfig()
	accepted.Logging.Level = "warn"
	if err := runtime.Reload(accepted); err != nil {
		t.Fatalf("accepted Reload(): %v", err)
	}
	if got := runtime.Level(); got != slog.LevelWarn {
		t.Fatalf("Level() = %v, want warn", got)
	}
}

func TestRuntimeInvalidReloadDoesNotChangeLevel(t *testing.T) {
	runtime := newTestRuntime(t)
	next := validConfig()
	next.Logging.Level = "trace"
	if err := runtime.Reload(next); err == nil {
		t.Fatal("Reload() error = nil, want invalid level")
	}
	if got := runtime.Level(); got != slog.LevelInfo {
		t.Fatalf("Level() = %v after invalid reload, want info", got)
	}
}

func TestRuntimeConcurrentReloadAndRead(t *testing.T) {
	runtime := newTestRuntime(t)
	levels := []string{"debug", "info", "warn", "error"}
	var wait sync.WaitGroup

	for worker := 0; worker < 16; worker++ {
		wait.Add(1)
		go func(offset int) {
			defer wait.Done()
			for index := 0; index < 250; index++ {
				next := validConfig()
				next.Logging.Level = levels[(offset+index)%len(levels)]
				if err := runtime.Reload(next); err != nil {
					t.Errorf("Reload(): %v", err)
					return
				}
				switch runtime.Level() {
				case slog.LevelDebug, slog.LevelInfo, slog.LevelWarn, slog.LevelError:
				default:
					t.Errorf("Level() returned an invalid intermediate value: %v", runtime.Level())
					return
				}
			}
		}(worker)
	}

	wait.Wait()
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	runtime, err := NewRuntime(validConfig())
	if err != nil {
		t.Fatalf("NewRuntime(): %v", err)
	}
	return runtime
}

func validConfig() config.Config {
	return config.Config{
		SchemaVersion: 1,
		Runtime: config.Runtime{
			RawQueueCapacity:       100,
			EventQueueCapacity:     100,
			ReconcileQueueCapacity: 100,
			ShutdownTimeout:        config.Duration(30 * time.Second),
		},
		Source: config.Source{
			CheckpointInterval:        config.Duration(time.Second),
			CheckpointRecordThreshold: 100,
		},
		Store:   config.Store{DatabasePath: "/var/lib/guard/guard.db"},
		Logging: config.Logging{Level: "info"},
		Web: config.Web{
			ListenAddress: "127.0.0.1:8080",
			Security:      config.WebSecurity{AllowRemoteHTTP: false},
		},
		SMTP: config.SMTP{CredentialFile: "/run/guard/smtp-credential"},
	}
}

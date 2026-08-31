// Package notification owns notification worker runtime boundaries.
package notification

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/lifei6671/guard-wall/internal/config"
)

var (
	// ErrSMTPNotReady reports that the SMTP worker never crossed its readiness
	// barrier. Credential acquisition and pre-ready worker failures use this
	// category.
	ErrSMTPNotReady = errors.New("smtp worker is not ready")
	// ErrSMTPRuntimeStarted reports an attempt to reuse the one-shot runtime.
	ErrSMTPRuntimeStarted = errors.New("smtp runtime has already started")
)

// SMTPWorker runs for the lifetime of one SMTP runtime. It must call markReady
// only after its own initialization succeeds. Credential bytes remain valid
// until Run returns and must not be retained afterward.
type SMTPWorker interface {
	Run(ctx context.Context, credential []byte, markReady func()) error
}

type credentialReadFunc func(context.Context, string, uint32) ([]byte, error)

type smtpWorkerError struct {
	cause    error
	notReady bool
}

func (e *smtpWorkerError) Error() string {
	if e.notReady {
		return "run smtp runtime: worker failed before readiness"
	}
	return "run smtp runtime: worker stopped after readiness"
}

func (e *smtpWorkerError) Is(target error) bool {
	if target == ErrSMTPNotReady {
		return e.notReady
	}
	return errors.Is(e.cause, target)
}

// SMTPRuntime gates worker readiness on the authoritative credential file.
// A runtime is single-use; Ready is closed at most once and only while the
// worker is actively running after a successful secure credential read.
type SMTPRuntime struct {
	mu             sync.Mutex
	worker         SMTPWorker
	readCredential credentialReadFunc
	ready          chan struct{}
	started        bool
}

// NewSMTPRuntime constructs a runtime bound directly to the secure Config
// credential reader. It never consults environment variables or SQLite.
func NewSMTPRuntime(worker SMTPWorker) (*SMTPRuntime, error) {
	return newSMTPRuntime(worker, config.ReadCredentialFile)
}

func newSMTPRuntime(worker SMTPWorker, reader credentialReadFunc) (*SMTPRuntime, error) {
	if worker == nil {
		return nil, fmt.Errorf("new smtp runtime: worker is required")
	}
	if reader == nil {
		return nil, fmt.Errorf("new smtp runtime: credential reader is required")
	}
	return &SMTPRuntime{
		worker:         worker,
		readCredential: reader,
		ready:          make(chan struct{}),
	}, nil
}

// Ready returns the one-shot startup barrier. It closes only after the secure
// credential read succeeds and the worker explicitly announces readiness.
func (r *SMTPRuntime) Ready() <-chan struct{} {
	return r.ready
}

// Run securely loads the YAML-referenced credential and owns one worker run.
// Credential bytes are cleared when the worker stops or startup fails.
func (r *SMTPRuntime) Run(ctx context.Context, smtp config.SMTP, guardGID uint32) error {
	if ctx == nil {
		return fmt.Errorf("run smtp runtime: %w: context is required", ErrSMTPNotReady)
	}
	if smtp.CredentialFile == "" {
		return fmt.Errorf("run smtp runtime: %w: credential file is required", ErrSMTPNotReady)
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run smtp runtime: %w: %w", ErrSMTPNotReady, err)
	}

	r.mu.Lock()
	if r.started {
		r.mu.Unlock()
		return ErrSMTPRuntimeStarted
	}
	r.started = true
	r.mu.Unlock()

	credential, err := r.readCredential(ctx, smtp.CredentialFile, guardGID)
	if err != nil {
		clear(credential)
		return fmt.Errorf("run smtp runtime: %w: credential unavailable: %w", ErrSMTPNotReady, err)
	}
	defer clear(credential)

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("run smtp runtime: %w: %w", ErrSMTPNotReady, err)
	}

	active := true
	becameReady := false
	markReady := func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !active || becameReady {
			return
		}
		becameReady = true
		close(r.ready)
	}

	workerErr := r.worker.Run(ctx, credential, markReady)
	r.mu.Lock()
	active = false
	ready := becameReady
	r.mu.Unlock()

	if workerErr != nil {
		return &smtpWorkerError{cause: workerErr, notReady: !ready}
	}
	if !ready {
		return fmt.Errorf("run smtp runtime: %w: worker exited before announcing readiness", ErrSMTPNotReady)
	}
	return nil
}

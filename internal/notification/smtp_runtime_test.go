package notification

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/config"
)

type smtpWorkerFunc func(context.Context, []byte, func()) error

func (f smtpWorkerFunc) Run(ctx context.Context, credential []byte, markReady func()) error {
	return f(ctx, credential, markReady)
}

func TestSMTPRuntimeGatesReadyOnCredentialAndWorker(t *testing.T) {
	const (
		credentialPath = "/etc/guard/smtp.credential"
		guardGID       = uint32(998)
	)
	secret := []byte("smtp-password=must-be-cleared")
	workerCalled := false
	worker := smtpWorkerFunc(func(ctx context.Context, credential []byte, markReady func()) error {
		workerCalled = true
		if ctx.Err() != nil {
			t.Fatalf("worker context error = %v", ctx.Err())
		}
		if string(credential) != "smtp-password=must-be-cleared" {
			t.Fatalf("worker credential = %q", credential)
		}
		markReady()
		markReady()
		return nil
	})
	reader := func(ctx context.Context, path string, gid uint32) ([]byte, error) {
		if ctx.Err() != nil || path != credentialPath || gid != guardGID {
			t.Fatalf("credential read ctx=%v path=%q gid=%d", ctx.Err(), path, gid)
		}
		return secret, nil
	}
	runtime, err := newSMTPRuntime(worker, reader)
	if err != nil {
		t.Fatal(err)
	}

	if err := runtime.Run(context.Background(), config.SMTP{CredentialFile: credentialPath}, guardGID); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !workerCalled {
		t.Fatal("worker was not called")
	}
	assertReadyClosed(t, runtime.Ready(), true)
	assertCleared(t, secret)
}

func TestSMTPRuntimePreReadyFailuresDoNotStartOrSignalReady(t *testing.T) {
	errRead := errors.New("credential read failed")
	errWorker := errors.New("worker initialization failed")
	tests := []struct {
		name        string
		ctx         func() context.Context
		credential  string
		reader      credentialReadFunc
		worker      smtpWorkerFunc
		want        error
		wantRead    bool
		wantWorker  bool
		wantCleared bool
	}{
		{
			name: "missing credential path",
			ctx:  context.Background,
			want: ErrSMTPNotReady,
		},
		{
			name: "context canceled before read",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			credential: "/credential",
			want:       context.Canceled,
		},
		{
			name:       "secure read fails",
			ctx:        context.Background,
			credential: "/credential",
			reader: func(context.Context, string, uint32) ([]byte, error) {
				return nil, errRead
			},
			want:     errRead,
			wantRead: true,
		},
		{
			name:       "worker fails before ready",
			ctx:        context.Background,
			credential: "/credential",
			reader: func(context.Context, string, uint32) ([]byte, error) {
				return []byte("secret"), nil
			},
			worker: func(context.Context, []byte, func()) error {
				return errWorker
			},
			want:        errWorker,
			wantRead:    true,
			wantWorker:  true,
			wantCleared: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			readCalled := false
			workerCalled := false
			var contents []byte
			reader := func(ctx context.Context, path string, gid uint32) ([]byte, error) {
				readCalled = true
				if test.reader == nil {
					t.Fatal("credential reader was called")
				}
				result, err := test.reader(ctx, path, gid)
				contents = result
				return result, err
			}
			worker := smtpWorkerFunc(func(ctx context.Context, credential []byte, markReady func()) error {
				workerCalled = true
				if test.worker == nil {
					t.Fatal("SMTP worker was called")
				}
				return test.worker(ctx, credential, markReady)
			})
			runtime, err := newSMTPRuntime(worker, reader)
			if err != nil {
				t.Fatal(err)
			}

			err = runtime.Run(test.ctx(), config.SMTP{CredentialFile: test.credential}, 998)
			if !errors.Is(err, ErrSMTPNotReady) || !errors.Is(err, test.want) {
				t.Fatalf("Run() error = %v, want SMTP not ready and %v", err, test.want)
			}
			if readCalled != test.wantRead || workerCalled != test.wantWorker {
				t.Fatalf("readCalled=%v workerCalled=%v", readCalled, workerCalled)
			}
			assertReadyClosed(t, runtime.Ready(), false)
			if test.wantCleared {
				assertCleared(t, contents)
			}
		})
	}
}

func TestSMTPRuntimeCancellationAfterReadClearsCredentialWithoutStartingWorker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	secret := []byte("secret")
	runtime, err := newSMTPRuntime(unexpectedSMTPWorker(t), func(context.Context, string, uint32) ([]byte, error) {
		cancel()
		return secret, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runtime.Run(ctx, config.SMTP{CredentialFile: "/credential"}, 998)
	if !errors.Is(err, ErrSMTPNotReady) || !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v", err)
	}
	assertReadyClosed(t, runtime.Ready(), false)
	assertCleared(t, secret)
}

func TestSMTPRuntimeClearsPartialCredentialOnReadError(t *testing.T) {
	errRead := errors.New("partial credential read failed")
	partial := []byte("partial-secret")
	runtime, err := newSMTPRuntime(unexpectedSMTPWorker(t), func(context.Context, string, uint32) ([]byte, error) {
		return partial, errRead
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
	if !errors.Is(err, ErrSMTPNotReady) || !errors.Is(err, errRead) {
		t.Fatalf("Run() error = %v", err)
	}
	assertReadyClosed(t, runtime.Ready(), false)
	assertCleared(t, partial)
}

func TestSMTPRuntimePostReadyFailurePreservesWorkerError(t *testing.T) {
	errWorker := errors.New("worker stopped")
	secret := []byte("secret")
	worker := smtpWorkerFunc(func(ctx context.Context, credential []byte, markReady func()) error {
		markReady()
		return errWorker
	})
	runtime, err := newSMTPRuntime(worker, func(context.Context, string, uint32) ([]byte, error) {
		return secret, nil
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
	if !errors.Is(err, errWorker) || errors.Is(err, ErrSMTPNotReady) {
		t.Fatalf("Run() error = %v", err)
	}
	assertReadyClosed(t, runtime.Ready(), true)
	assertCleared(t, secret)
}

func TestSMTPRuntimeWorkerErrorsDoNotExposeCredential(t *testing.T) {
	errWorker := errors.New("worker failure category")
	tests := []struct {
		name      string
		markReady bool
	}{
		{name: "before ready"},
		{name: "after ready", markReady: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			secret := []byte("worker-must-not-leak-this-secret")
			worker := smtpWorkerFunc(func(_ context.Context, credential []byte, markReady func()) error {
				if test.markReady {
					markReady()
				}
				return fmt.Errorf("authentication rejected %s: %w", credential, errWorker)
			})
			runtime, err := newSMTPRuntime(worker, func(context.Context, string, uint32) ([]byte, error) {
				return secret, nil
			})
			if err != nil {
				t.Fatal(err)
			}

			err = runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
			if !errors.Is(err, errWorker) {
				t.Fatalf("Run() lost worker error identity: %v", err)
			}
			if errors.Is(err, ErrSMTPNotReady) != !test.markReady {
				t.Fatalf("Run() readiness classification = %v", err)
			}
			if strings.Contains(err.Error(), "worker-must-not-leak-this-secret") {
				t.Fatalf("Run() leaked credential: %v", err)
			}
			assertReadyClosed(t, runtime.Ready(), test.markReady)
			assertCleared(t, secret)
		})
	}
}

func TestSMTPRuntimeReadyStateOverridesWorkerNotReadyClassification(t *testing.T) {
	errWorker := errors.New("worker failure category")
	tests := []struct {
		name      string
		markReady bool
	}{
		{name: "before ready"},
		{name: "after ready", markReady: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			worker := smtpWorkerFunc(func(_ context.Context, _ []byte, markReady func()) error {
				if test.markReady {
					markReady()
				}
				return errors.Join(ErrSMTPNotReady, errWorker)
			})
			runtime, err := newSMTPRuntime(worker, func(context.Context, string, uint32) ([]byte, error) {
				return []byte("secret"), nil
			})
			if err != nil {
				t.Fatal(err)
			}

			err = runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
			if errors.Is(err, ErrSMTPNotReady) != !test.markReady {
				t.Fatalf("Run() readiness classification = %v", err)
			}
			if !errors.Is(err, errWorker) {
				t.Fatalf("Run() lost non-readiness worker classification: %v", err)
			}
			assertReadyClosed(t, runtime.Ready(), test.markReady)
		})
	}
}

func TestSMTPRuntimeRejectsMissingWorkerAndReuse(t *testing.T) {
	if runtime, err := NewSMTPRuntime(nil); runtime != nil || err == nil {
		t.Fatalf("NewSMTPRuntime(nil) runtime=%v error=%v", runtime, err)
	}

	reads := 0
	runtime, err := newSMTPRuntime(
		smtpWorkerFunc(func(context.Context, []byte, func()) error { return nil }),
		func(context.Context, string, uint32) ([]byte, error) {
			reads++
			return []byte("secret"), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	firstErr := runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
	if !errors.Is(firstErr, ErrSMTPNotReady) {
		t.Fatalf("first Run() error = %v", firstErr)
	}
	if err := runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998); !errors.Is(err, ErrSMTPRuntimeStarted) {
		t.Fatalf("second Run() error = %v", err)
	}
	if reads != 1 {
		t.Fatalf("credential reads = %d, want 1", reads)
	}
}

func TestSMTPRuntimeConcurrentRunAdmitsOnlyOneCredentialRead(t *testing.T) {
	readStarted := make(chan struct{})
	allowRead := make(chan struct{})
	runtime, err := newSMTPRuntime(
		smtpWorkerFunc(func(_ context.Context, _ []byte, markReady func()) error {
			markReady()
			return nil
		}),
		func(context.Context, string, uint32) ([]byte, error) {
			close(readStarted)
			<-allowRead
			return []byte("secret"), nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	firstResult := make(chan error, 1)
	go func() {
		firstResult <- runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
	}()
	<-readStarted
	secondErr := runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
	if !errors.Is(secondErr, ErrSMTPRuntimeStarted) {
		t.Fatalf("concurrent Run() error = %v", secondErr)
	}
	close(allowRead)
	if firstErr := <-firstResult; firstErr != nil {
		t.Fatalf("first Run() error = %v", firstErr)
	}
	assertReadyClosed(t, runtime.Ready(), true)
}

func TestSMTPRuntimeIgnoresReadyCallbackAfterWorkerReturns(t *testing.T) {
	var lateReady func()
	runtime, err := newSMTPRuntime(
		smtpWorkerFunc(func(_ context.Context, _ []byte, markReady func()) error {
			lateReady = markReady
			return nil
		}),
		func(context.Context, string, uint32) ([]byte, error) { return []byte("secret"), nil },
	)
	if err != nil {
		t.Fatal(err)
	}

	err = runtime.Run(context.Background(), config.SMTP{CredentialFile: "/credential"}, 998)
	if !errors.Is(err, ErrSMTPNotReady) {
		t.Fatalf("Run() error = %v", err)
	}
	lateReady()
	assertReadyClosed(t, runtime.Ready(), false)
}

func TestSMTPRuntimeCredentialErrorsDoNotExposeConfiguredPath(t *testing.T) {
	const path = "/etc/guard/sensitive-smtp-name"
	runtime, err := newSMTPRuntime(unexpectedSMTPWorker(t), func(context.Context, string, uint32) ([]byte, error) {
		return nil, config.ErrCredentialFileRead
	})
	if err != nil {
		t.Fatal(err)
	}

	err = runtime.Run(context.Background(), config.SMTP{CredentialFile: path}, 998)
	if !errors.Is(err, config.ErrCredentialFileRead) || strings.Contains(err.Error(), path) {
		t.Fatalf("Run() error = %v", err)
	}
}

func unexpectedSMTPWorker(t *testing.T) smtpWorkerFunc {
	t.Helper()
	return func(context.Context, []byte, func()) error {
		t.Fatal("SMTP worker was called")
		return nil
	}
}

func assertReadyClosed(t *testing.T, ready <-chan struct{}, wantClosed bool) {
	t.Helper()
	select {
	case <-ready:
		if !wantClosed {
			t.Fatal("Ready channel is closed")
		}
	default:
		if wantClosed {
			t.Fatal("Ready channel is open")
		}
	}
}

func assertCleared(t *testing.T, contents []byte) {
	t.Helper()
	for index, value := range contents {
		if value != 0 {
			t.Fatalf("credential byte %d was not cleared", index)
		}
	}
}

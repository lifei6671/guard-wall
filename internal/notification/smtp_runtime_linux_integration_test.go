//go:build linux && integration

package notification

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lifei6671/guard-wall/internal/config"
)

const linuxIntegrationCredential = "smtp-password=integration-fixture"

func TestSMTPRuntimeLinuxCredentialFileToReady(t *testing.T) {
	fixtureDir := os.Getenv("GUARD_CREDENTIAL_FIXTURE_DIR")
	if fixtureDir == "" {
		t.Fatal("GUARD_CREDENTIAL_FIXTURE_DIR is required")
	}
	guardGIDValue := os.Getenv("GUARD_GID")
	guardGID, err := strconv.ParseUint(guardGIDValue, 10, 32)
	if err != nil {
		t.Fatalf("GUARD_GID %q is invalid: %v", guardGIDValue, err)
	}

	tests := []struct {
		name      string
		path      string
		wantReady bool
		wantErr   error
	}{
		{name: "root guard 0640", path: "smtp-0640.credential", wantReady: true},
		{name: "root guard 0440", path: "smtp-0440.credential", wantReady: true},
		{name: "root guard 0600 is unreadable", path: "smtp-0600.credential", wantErr: config.ErrCredentialFileRead},
		{name: "mode 0644", path: "smtp-0644.credential", wantErr: config.ErrCredentialFileMode},
		{name: "wrong group", path: "smtp-wrong-group.credential", wantErr: config.ErrCredentialFileOwner},
		{name: "non-root owner", path: "smtp-non-root-owner.credential", wantErr: config.ErrCredentialFileOwner},
		{name: "symbolic link", path: "smtp-link.credential", wantErr: config.ErrCredentialFileSymlink},
		{name: "directory", path: "smtp-directory.credential", wantErr: config.ErrCredentialFileNotRegular},
		{name: "oversized", path: "smtp-oversized.credential", wantErr: config.ErrCredentialFileTooLarge},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			workerCalls := 0
			worker := linuxIntegrationSMTPWorker(func(_ context.Context, credential []byte, markReady func()) error {
				workerCalls++
				if string(credential) != linuxIntegrationCredential {
					t.Fatalf("worker credential = %q", credential)
				}
				markReady()
				return nil
			})
			runtime, err := NewSMTPRuntime(worker)
			if err != nil {
				t.Fatalf("NewSMTPRuntime(): %v", err)
			}

			credentialPath := filepath.Join(fixtureDir, test.path)
			runErr := runtime.Run(
				context.Background(),
				config.SMTP{CredentialFile: credentialPath},
				uint32(guardGID),
			)

			if test.wantReady {
				if runErr != nil {
					t.Fatalf("Run(): %v", runErr)
				}
				if workerCalls != 1 {
					t.Fatalf("worker calls = %d, want 1", workerCalls)
				}
				if !integrationReadyClosed(runtime.Ready()) {
					t.Fatal("Ready remained open after successful secure read")
				}
				return
			}

			if !errors.Is(runErr, ErrSMTPNotReady) || !errors.Is(runErr, test.wantErr) {
				t.Fatalf("Run() error = %v, want ErrSMTPNotReady and %v", runErr, test.wantErr)
			}
			if workerCalls != 0 {
				t.Fatalf("worker calls = %d, want 0", workerCalls)
			}
			if integrationReadyClosed(runtime.Ready()) {
				t.Fatal("Ready closed after credential rejection")
			}
			if strings.Contains(runErr.Error(), fixtureDir) ||
				strings.Contains(runErr.Error(), linuxIntegrationCredential) {
				t.Fatalf("credential failure exposed fixture data: %v", runErr)
			}
		})
	}
}

type linuxIntegrationSMTPWorker func(context.Context, []byte, func()) error

func (worker linuxIntegrationSMTPWorker) Run(
	ctx context.Context,
	credential []byte,
	markReady func(),
) error {
	return worker(ctx, credential, markReady)
}

func integrationReadyClosed(ready <-chan struct{}) bool {
	select {
	case <-ready:
		return true
	default:
		return false
	}
}

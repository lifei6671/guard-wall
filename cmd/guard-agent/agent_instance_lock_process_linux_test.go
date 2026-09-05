//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestAgentInstanceLockAcrossProcesses(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, release := range []string{"close", "exit", "kill"} {
		t.Run(release, func(t *testing.T) {
			directory := t.TempDir()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := func(mode, database string) *exec.Cmd {
				cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAgentInstanceLockProcessHelper$")
				cmd.Env = append(os.Environ(), "GUARD_LOCK_TEST_MODE="+mode, "GUARD_LOCK_TEST_DATABASE="+filepath.Join(directory, database))
				return cmd
			}
			holder := command("hold", "guard.db")
			holder.Stderr = os.Stderr
			stdin, err := holder.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			defer stdin.Close()
			stdout, err := holder.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := holder.Start(); err != nil {
				t.Fatal(err)
			}
			waited := false
			defer func() {
				if !waited {
					cancel()
					var exitErr *exec.ExitError
					if err := holder.Wait(); err != nil && !errors.As(err, &exitErr) {
						t.Errorf("reap holder: %v", err)
					}
				}
			}()
			reader := bufio.NewReader(stdout)
			expectLine := func(want string) {
				t.Helper()
				line, err := reader.ReadString('\n')
				if err != nil || line != want+"\n" {
					t.Fatalf("holder response = %q, error = %v; want %q", line, err, want)
				}
			}
			probe := func(mode string) {
				t.Helper()
				if output, err := command(mode, "other.db").CombinedOutput(); err != nil {
					t.Fatalf("contender %s: %v\n%s", mode, err, output)
				}
			}
			expectLine("locked")
			probe("busy")
			if release == "kill" {
				if err := holder.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := fmt.Fprintln(stdin, release); err != nil {
					t.Fatal(err)
				}
				if release == "close" {
					expectLine("released")
					// 原进程仍存活时，新进程应能取得同目录下另一数据库的锁。
					probe("available")
					if _, err := fmt.Fprintln(stdin, "exit"); err != nil {
						t.Fatal(err)
					}
				}
			}
			waitErr := holder.Wait()
			waited = true
			if release == "kill" {
				var exitErr *exec.ExitError
				if !errors.As(waitErr, &exitErr) {
					t.Fatalf("killed holder exit = %v", waitErr)
				}
			} else if waitErr != nil {
				t.Fatalf("holder exit: %v", waitErr)
			}
			probe("available")
		})
	}
}

func TestAgentInstanceLockProcessHelper(t *testing.T) {
	mode := os.Getenv("GUARD_LOCK_TEST_MODE")
	if mode == "" {
		return
	}
	lock, err := acquireAgentInstanceLock(os.Getenv("GUARD_LOCK_TEST_DATABASE"))
	if mode == "busy" {
		if !errors.Is(err, errGuardAgentAlreadyRunning) {
			t.Fatalf("contended acquisition = %v; want already running", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if mode == "available" {
		if err := lock.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if mode != "hold" {
		t.Fatalf("unknown helper mode %q", mode)
	}
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read holder command: %v", err)
		}
		switch line {
		case "close\n":
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			if _, err := fmt.Fprintln(os.Stdout, "released"); err != nil {
				t.Fatal(err)
			}
		case "exit\n":
			// 直接退出，验证内核释放进程尚未主动关闭的目录锁。
			os.Exit(0)
		default:
			t.Fatalf("unknown holder command %q", line)
		}
	}
}

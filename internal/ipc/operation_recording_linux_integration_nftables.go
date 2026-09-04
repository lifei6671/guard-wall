//go:build linux && integration && nftables

package ipc

import (
	"fmt"
	"os"
)

const integrationOperationLogPathEnv = "GUARD_INTEGRATION_OPERATION_LOG"

// recordCompletedEnforcerOperation writes only the completed operation,
// optional closed mutation domain, and serving process ID for the isolated
// Docker integration fixture. It is excluded from normal builds and never
// records request payloads.
func recordCompletedEnforcerOperation(request Request) {
	path := os.Getenv(integrationOperationLogPathEnv)
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	if apply, ok := request.(ApplyManagedPlanRequest); ok {
		_, _ = fmt.Fprintf(file, "%d %s %s\n", os.Getpid(), request.Operation(), apply.Plan().Domain())
	} else {
		_, _ = fmt.Fprintf(file, "%d %s\n", os.Getpid(), request.Operation())
	}
	_ = file.Close()
}

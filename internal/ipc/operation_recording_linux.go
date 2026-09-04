//go:build linux && !(integration && nftables)

package ipc

// recordCompletedEnforcerOperation keeps the production IPC loop free of
// test-observation side effects. The isolated nftables integration build
// replaces it with a private recorder after a complete response write.
func recordCompletedEnforcerOperation(Request) {}

package keeper

import "fmt"

// preStopPollAttempts and preStopPollInterval bound how long the PreStop hook waits for
// the outgoing leader to hand off Raft leadership before the kubelet is allowed to send
// SIGTERM. 10 * 0.5s = 5s, comfortably inside the pod's default 30s
// terminationGracePeriodSeconds. This cap only matters when http_control has stopped
// answering entirely (a hung server): a healthy handover finishes in well under a
// second even under heavy write load, and a server that answers with a non-leader role
// or an error exits the loop immediately.
const (
	preStopPollAttempts = 10
	preStopPollInterval = "0.5" // seconds, passed to `sleep`
)

// buildPreStopScript returns the PreStop hook script run inside the Keeper container.
// It asks the current replica to yield Raft leadership (the FLWYieldLeadership four
// letter word, sent over the HTTP commands API), then polls the same http_control
// port's /ready endpoint until the replica's role is no longer "leader", capped at
// preStopPollAttempts * preStopPollInterval. It talks to http_control specifically
// (not the client port) because that port stays plain HTTP even when TLS is required
// for client connections; see templateContainer's readinessProbe for the same
// reasoning.
//
// Requires /bin/bash: /dev/tcp/... is a bash builtin, not POSIX sh, and the image ships
// no curl. On an image without bash, the exec fails immediately and the kubelet records
// a FailedPreStopHook event; termination proceeds without hanging.
//
// On Keeper versions without the commands API (pre-26.3), the yield-leadership request
// 404s harmlessly (swallowed below) and the script falls straight into the /ready poll,
// which exists on every supported version, so behavior degrades to "wait once, then
// proceed" rather than failing, and no cr.Status.Version gate is needed here. A gate
// would also be wrong for another reason: status.version is only populated
// asynchronously from mntr on running replicas, so a template that depends on it would
// change after pod startup and roll every freshly created cluster once.
func buildPreStopScript() string {
	return fmt.Sprintf(`h() { exec 3<>/dev/tcp/127.0.0.1/%[1]d; printf 'GET %%s HTTP/1.0\r\n\r\n' "$1" >&3; cat <&3; exec 3<&-; }
h '/api/v1/commands?command=%[2]s' >/dev/null 2>&1
for i in $(seq 1 %[3]d); do h /ready 2>/dev/null | grep -q '"role":"leader"' || exit 0; sleep %[4]s; done
`, PortHTTPControl, FLWYieldLeadership, preStopPollAttempts, preStopPollInterval)
}

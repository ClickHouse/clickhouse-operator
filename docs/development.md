# Development Guide

## Logging severity

Pick the severity of an operator log message by who has to act on it:

- `debug`: tracing messages that are only needed to debug operator behavior.
- `info`: verbose messages that may be useful for a user to check. They answer questions like why the next replica is not updating, or what the operator is doing now.
- `warning`: something went wrong and reconciliation is off the happy path. The issue is transient, or user attention may be required to fix it. It is safe to alert on the warning rate without being paged every minute.
- `error`: internal operator error, likely not related to a single cluster, such as Kubernetes API connectivity or authentication failures, or a violated base invariant. It is safe to alert on the existence of such messages and be paged only when there is a real reason.

An unreachable replica is a valid state from the operator's standpoint, so it never logs at `error`.
The e2e suite fails a spec when the operator logs at `error` during it.

Per-reconcile mechanics, such as entering a reconcile, step tracing and object dumps, log at `debug`.

As the [Kubernetes logging conventions](https://github.com/kubernetes/community/blob/master/contributors/devel/sig-instrumentation/logging.md) recommend, don't log an error and then return it: wrap the returned error with the context instead.
controller-runtime logs every error that `Reconcile` returns at `error`, as `Reconciler error`.

## Logging fields

Pass identifiers as structured fields, not inside the message:

- `replica_id` and `shard_id` for replica and shard IDs, `replicas` for a list of replica IDs.
- The lowercase kind for a child resource name, such as `statefulset`, `configmap` or `persistentvolumeclaim`. Code that handles any kind uses `kind` and `resource`.
- `name` and `namespace` belong to the reconciled cluster, so child resources never use them.

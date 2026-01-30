package controllerutil

import (
	"context"

	"github.com/go-logr/logr"
	"go.uber.org/zap"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

// Logger is a Logger implementation using zap.Logger.
type Logger struct {
	l *zap.Logger
}

// NewLogger creates a new Logger.
func NewLogger(l *zap.Logger) Logger {
	return Logger{l: l}
}

// Named creates a named child logger.
func (l Logger) Named(name string) Logger {
	return Logger{l: l.l.Named(name)}
}

// WithContext creates a child logger with fields from the context and object.
func (l Logger) WithContext(ctx context.Context, object client.Object) Logger {
	return Logger{l: l.l.With(
		zap.String("reconcile_id", string(controller.ReconcileIDFromContext(ctx))),
		zap.String("namespace", object.GetNamespace()),
		zap.String("name", object.GetName()),
	)}
}

// With creates a child logger with additional key-value pairs.
func (l Logger) With(keysAndVals ...any) Logger {
	return Logger{
		l: l.l.With(l.toFields(keysAndVals...)...),
	}
}

// Debug logs a debug message.
func (l Logger) Debug(msg string, keysAndVals ...any) {
	l.l.Debug(msg, l.toFields(keysAndVals...)...)
}

// Info logs an info message.
func (l Logger) Info(msg string, keysAndVals ...any) {
	l.l.Info(msg, l.toFields(keysAndVals...)...)
}

// Warn logs a warning message.
func (l Logger) Warn(msg string, keysAndVals ...any) {
	l.l.Warn(msg, l.toFields(keysAndVals...)...)
}

// Error logs an error message with an error.
func (l Logger) Error(err error, msg string, keysAndVals ...any) {
	l.l.Error(msg, append(l.toFields(keysAndVals...), zap.String("error", err.Error()))...)
}

// Fatal logs an error message and exists.
func (l Logger) Fatal(err error, msg string, keysAndVals ...any) {
	l.l.Fatal(msg, append(l.toFields(keysAndVals...), zap.String("error", err.Error()))...)
}

// Panic logs an error message and panics.
func (l Logger) Panic(err error, msg string, keysAndVals ...any) {
	l.l.Panic(msg, append(l.toFields(keysAndVals...), zap.String("error", err.Error()))...)
}

func (l Logger) toFields(args ...any) []zap.Field {
	if len(args)%2 != 0 {
		l.l.Fatal("odd number of keys and values", zap.Any("keys", args))
		return nil
	}

	result := make([]zap.Field, 0, len(args)/2)
	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			l.l.Fatal("non string key found", zap.Any("key", args[i]))
			return nil
		}

		// Handle types that implement logr.Marshaler: log the replacement
		// object instead of the original one.
		val := args[i+1]
		if marshaler, ok := val.(logr.Marshaler); ok {
			val = marshaler.MarshalLog()
		}

		result = append(result, zap.Any(key, val))
	}

	return result
}

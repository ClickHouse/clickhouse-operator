package controller

import (
	"time"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
	"github.com/ClickHouse/clickhouse-operator/internal/upgrade"
)

// Dependencies holds the runtime objects shared by the controllers, replaceable in tests.
type Dependencies struct {
	Logger  controllerutil.Logger
	Checker *upgrade.Checker
	Dialer  controllerutil.DialContextFunc
}

// Settings holds the reconciliation behavior knobs shared by the controllers.
type Settings struct {
	EnablePDB           bool
	EnableNetworkPolicy bool
	ResyncPeriod        time.Duration
}

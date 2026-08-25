package testutil

import (
	"time"

	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// PollInterval is the default polling interval for Eventually-style waits.
const PollInterval = 100 * time.Millisecond

// Env bundles the suite-level handles shared by the test helpers.
type Env struct {
	Client client.Client
	Config *rest.Config
	Dialer controllerutil.DialContextFunc
}

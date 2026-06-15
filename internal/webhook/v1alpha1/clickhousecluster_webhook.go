package v1alpha1

import (
	"context"
	"errors"
	"fmt"
	"slices"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	chv1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// reservedClickHousePort* values mirror the port constants defined in
// internal/controller/clickhouse/constants.go. They are duplicated here because
// importing that package from the webhook would introduce a controller→webhook
// import cycle. Keep these values in sync with the controller package.
const (
	reservedClickHousePortHTTP             = 8123
	reservedClickHousePortHTTPSecure       = 8443
	reservedClickHousePortNative           = 9000
	reservedClickHousePortNativeSecure     = 9440
	reservedClickHousePortInterserver      = 9009
	reservedClickHousePortPrometheusScrape = 9363
	reservedClickHousePortManagement       = 9001
)

// SetupClickHouseWebhookWithManager registers the webhook for ClickHouseCluster in the manager.
func SetupClickHouseWebhookWithManager(mgr ctrl.Manager, log controllerutil.Logger) error {
	wh := &ClickHouseClusterWebhook{
		Log: log.Named("clickhouse-webhook"),
	}

	err := ctrl.NewWebhookManagedBy(mgr, &chv1.ClickHouseCluster{}).
		WithValidator(wh).
		WithDefaulter(wh).
		Complete()
	if err != nil {
		return fmt.Errorf("setup ClickHouseCluster webhook: %w", err)
	}

	return nil
}

// ClickHouseClusterWebhook implements a validating and mutating webhook for ClickHouseCluster.
// +kubebuilder:webhook:path=/mutate-clickhouse-com-v1alpha1-clickhousecluster,mutating=true,failurePolicy=ignore,sideEffects=None,groups=clickhouse.com,resources=clickhouseclusters,verbs=create;update,versions=v1alpha1,name=mclickhousecluster-v1alpha1.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-clickhouse-com-v1alpha1-clickhousecluster,mutating=false,failurePolicy=ignore,sideEffects=None,groups=clickhouse.com,resources=clickhouseclusters,verbs=create;update,versions=v1alpha1,name=vclickhousecluster-v1alpha1.kb.io,admissionReviewVersions=v1
type ClickHouseClusterWebhook struct {
	Log controllerutil.Logger
}

var _ admission.Defaulter[*chv1.ClickHouseCluster] = &ClickHouseClusterWebhook{}
var _ admission.Validator[*chv1.ClickHouseCluster] = &ClickHouseClusterWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind ClickHouseCluster.
func (w *ClickHouseClusterWebhook) Default(_ context.Context, cluster *chv1.ClickHouseCluster) error {
	w.Log.Info("Fill defaults", "name", cluster.Name, "namespace", cluster.Namespace)
	cluster.Spec.WithDefaults()

	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateCreate(_ context.Context, cluster *chv1.ClickHouseCluster) (admission.Warnings, error) {
	warns, errs := w.validateImpl(cluster)

	return warns, errors.Join(errs...)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateUpdate(_ context.Context, oldCluster, newCluster *chv1.ClickHouseCluster) (admission.Warnings, error) {
	w.Log.Info("Validate update spec", "name", newCluster.Name, "namespace", newCluster.Namespace)

	warns, errs := w.validateImpl(newCluster)
	if oldCluster.Spec.Shards != nil && newCluster.Spec.Shards != nil &&
		*oldCluster.Spec.Shards > *newCluster.Spec.Shards {
		warns = append(warns, "Decreasing the number of shards is a destructive operation. It removes shards with all their data.")
	}

	if err := validateDataVolumeSpecChanges(
		oldCluster.Spec.DataVolumeClaimSpec,
		newCluster.Spec.DataVolumeClaimSpec,
	); err != nil {
		errs = append(errs, err)
	}

	if err := validateAdditionalVolumeClaimTemplatesChanges(
		oldCluster.Spec.AdditionalVolumeClaimTemplates,
		newCluster.Spec.AdditionalVolumeClaimTemplates,
	); err != nil {
		errs = append(errs, err)
	}

	return warns, errors.Join(errs...)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateDelete(context.Context, *chv1.ClickHouseCluster) (admission.Warnings, error) {
	return nil, nil
}

func (w *ClickHouseClusterWebhook) validateImpl(obj *chv1.ClickHouseCluster) (admission.Warnings, []error) {
	w.Log.Info("Validating spec", "name", obj.Name, "namespace", obj.Namespace)

	var (
		warns admission.Warnings
		errs  []error
	)

	if err := obj.Spec.Settings.TLS.Validate(); err != nil {
		errs = append(errs, err)
	}

	if err := obj.Spec.PodDisruptionBudget.Validate(); err != nil {
		errs = append(errs, err)
	}

	additionalVolumeErrs := validateAdditionalVolumeClaimTemplates(obj.Spec.DataVolumeClaimSpec, obj.Spec.AdditionalVolumeClaimTemplates)
	errs = append(errs, additionalVolumeErrs...)

	reservedNames := slices.Clone(internal.ReservedClickHouseVolumeNames)
	for _, addl := range obj.Spec.AdditionalVolumeClaimTemplates {
		reservedNames = append(reservedNames, addl.Name)
	}

	volumeWarns, volumeErrs := validateVolumes(
		obj.Spec.PodTemplate.Volumes,
		obj.Spec.ContainerTemplate.VolumeMounts,
		reservedNames,
		internal.ClickHouseDataPath,
		obj.Spec.DataVolumeClaimSpec != nil,
	)
	warns = append(warns, volumeWarns...)
	errs = append(errs, volumeErrs...)

	if obj.Spec.Settings.DefaultUserPassword == nil {
		warns = append(warns, ".spec.settings.defaultUserPassword is empty, 'default' user will be without password ")
	} else {
		if err := obj.Spec.Settings.DefaultUserPassword.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	seenPorts := make(map[int32]int, len(obj.Spec.AdditionalPorts))
	for i, p := range obj.Spec.AdditionalPorts {
		if prev, ok := seenPorts[p.Port]; ok {
			errs = append(errs, fmt.Errorf("spec.additionalPorts[%d].port: %d duplicates spec.additionalPorts[%d].port", i, p.Port, prev))
		} else {
			seenPorts[p.Port] = i
		}
	}

	// Reject additionalPorts that collide with ports the operator may bind on its own.
	// All TLS-related ports are reserved unconditionally so flipping settings.tls.enabled
	// later cannot break a previously valid cluster.
	reservedPorts := map[int32]string{
		reservedClickHousePortHTTP:             "HTTP",
		reservedClickHousePortHTTPSecure:       "HTTPS",
		reservedClickHousePortNative:           "native TCP",
		reservedClickHousePortNativeSecure:     "native TLS",
		reservedClickHousePortInterserver:      "interserver",
		reservedClickHousePortPrometheusScrape: "Prometheus metrics",
		reservedClickHousePortManagement:       "management",
	}

	reservedPortNames := map[string]struct{}{
		"http":        {},
		"http-secure": {},
		"tcp":         {},
		"tcp-secure":  {},
		"interserver": {},
		"prometheus":  {},
		"management":  {},
	}

	for i, p := range obj.Spec.AdditionalPorts {
		if purpose, taken := reservedPorts[p.Port]; taken {
			errs = append(errs, fmt.Errorf(
				"spec.additionalPorts[%d].port: %d is reserved for the operator-managed %s port",
				i, p.Port, purpose,
			))
		}

		if _, taken := reservedPortNames[p.Name]; taken {
			errs = append(errs, fmt.Errorf(
				"spec.additionalPorts[%d].name: %q is reserved by the operator",
				i, p.Name,
			))
		}
	}

	return warns, errs
}

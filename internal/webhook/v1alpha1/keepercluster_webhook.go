package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	chv1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
	"github.com/ClickHouse/clickhouse-operator/internal"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

// KeeperClusterWebhook implements a validating and mutating webhook for KeeperCluster.
// +kubebuilder:webhook:path=/mutate-clickhouse-com-v1alpha1-keepercluster,mutating=true,failurePolicy=ignore,sideEffects=None,groups=clickhouse.com,resources=keeperclusters,verbs=create;update,versions=v1alpha1,name=mkeepercluster.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-clickhouse-com-v1alpha1-keepercluster,mutating=false,failurePolicy=ignore,sideEffects=None,groups=clickhouse.com,resources=keeperclusters,verbs=create;update,versions=v1alpha1,name=vkeepercluster.kb.io,admissionReviewVersions=v1
type KeeperClusterWebhook struct {
	Log controllerutil.Logger
}

var _ admission.Defaulter[*chv1.KeeperCluster] = &KeeperClusterWebhook{}
var _ admission.Validator[*chv1.KeeperCluster] = &KeeperClusterWebhook{}

// SetupKeeperWebhookWithManager registers the webhook for KeeperCluster in the manager.
func SetupKeeperWebhookWithManager(mgr ctrl.Manager, log controllerutil.Logger) error {
	wh := &KeeperClusterWebhook{
		Log: log.Named("keeper-webhook"),
	}

	err := ctrl.NewWebhookManagedBy(mgr, &chv1.KeeperCluster{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
	if err != nil {
		return fmt.Errorf("setup KeeperCluster webhook: %w", err)
	}

	return nil
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) Default(_ context.Context, cluster *chv1.KeeperCluster) error {
	w.Log.Info("Filling defaults", "name", cluster.Name, "namespace", cluster.Namespace)
	cluster.Spec.WithDefaults()

	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) ValidateCreate(_ context.Context, cluster *chv1.KeeperCluster) (warnings admission.Warnings, err error) {
	warns, errs := w.validateImpl(cluster)

	return warns, errors.Join(errs...)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) ValidateUpdate(_ context.Context, oldCluster, newCluster *chv1.KeeperCluster) (warnings admission.Warnings, err error) {
	warns, errs := w.validateImpl(newCluster)

	if err := validateDataVolumeSpecChanges(
		oldCluster.Spec.DataVolumeClaimSpec,
		newCluster.Spec.DataVolumeClaimSpec,
	); err != nil {
		errs = append(errs, err)
	}

	return warns, errors.Join(errs...)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) ValidateDelete(context.Context, *chv1.KeeperCluster) (warnings admission.Warnings, err error) {
	return nil, nil
}

func (w *KeeperClusterWebhook) validateImpl(obj *chv1.KeeperCluster) (admission.Warnings, []error) {
	w.Log.Info("Validating spec", "name", obj.Name, "namespace", obj.Namespace)

	var (
		warns admission.Warnings
		errs  []error
	)

	volumeWarns, volumeErrs := validateVolumes(
		obj.Spec.PodTemplate.Volumes,
		obj.Spec.ContainerTemplate.VolumeMounts,
		internal.ReservedKeeperVolumeNames,
		internal.KeeperDataPath,
		obj.Spec.DataVolumeClaimSpec != nil,
	)
	warns = append(warns, volumeWarns...)
	errs = append(errs, volumeErrs...)

	if err := obj.Spec.Settings.TLS.Validate(); err != nil {
		errs = append(errs, err)
	}

	return warns, errs
}

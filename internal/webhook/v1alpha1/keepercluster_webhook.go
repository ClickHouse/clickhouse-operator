package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
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

var _ webhook.CustomDefaulter = &KeeperClusterWebhook{}
var _ webhook.CustomValidator = &KeeperClusterWebhook{}

// SetupKeeperWebhookWithManager registers the webhook for KeeperCluster in the manager.
func SetupKeeperWebhookWithManager(mgr ctrl.Manager, log controllerutil.Logger) error {
	wh := &KeeperClusterWebhook{
		Log: log.Named("keeper-webhook"),
	}

	err := ctrl.NewWebhookManagedBy(mgr).
		For(&chv1.KeeperCluster{}).
		WithDefaulter(wh).
		WithValidator(wh).
		Complete()
	if err != nil {
		return fmt.Errorf("setup KeeperCluster webhook: %w", err)
	}

	return nil
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) Default(_ context.Context, obj runtime.Object) error {
	keeperCluster, ok := obj.(*chv1.KeeperCluster)
	if !ok {
		return fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	w.Log.Info("Filling defaults", "name", keeperCluster.Name, "namespace", keeperCluster.Namespace)
	keeperCluster.Spec.WithDefaults()

	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) ValidateCreate(_ context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	keeperCluster, ok := obj.(*chv1.KeeperCluster)
	if !ok {
		return nil, fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	return nil, w.validateImpl(keeperCluster)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) ValidateUpdate(_ context.Context, _, newObj runtime.Object) (warnings admission.Warnings, err error) {
	keeperCluster, ok := newObj.(*chv1.KeeperCluster)
	if !ok {
		return nil, fmt.Errorf("unexpected object type received %s", newObj.GetObjectKind().GroupVersionKind())
	}

	return nil, w.validateImpl(keeperCluster)
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) ValidateDelete(context.Context, runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func (w *KeeperClusterWebhook) validateImpl(obj *chv1.KeeperCluster) error {
	w.Log.Info("Validating spec", "name", obj.Name, "namespace", obj.Namespace)
	errs := ValidateCustomVolumeMounts(obj.Spec.PodTemplate.Volumes, obj.Spec.ContainerTemplate.VolumeMounts, internal.ReservedKeeperVolumeNames)

	if err := obj.Spec.Settings.TLS.Validate(); err != nil {
		errs = append(errs, err)
	}

	return errors.Join(errs...)
}

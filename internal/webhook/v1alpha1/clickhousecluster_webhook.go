package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/ClickHouse/clickhouse-operator/internal"
	"github.com/ClickHouse/clickhouse-operator/internal/controllerutil"

	chv1 "github.com/ClickHouse/clickhouse-operator/api/v1alpha1"
)

// SetupClickHouseWebhookWithManager registers the webhook for ClickHouseCluster in the manager.
func SetupClickHouseWebhookWithManager(mgr ctrl.Manager, log controllerutil.Logger) error {
	wh := &ClickHouseClusterWebhook{
		Log: log.Named("clickhouse-webhook"),
	}

	err := ctrl.NewWebhookManagedBy(mgr).
		For(&chv1.ClickHouseCluster{}).
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

var _ webhook.CustomDefaulter = &ClickHouseClusterWebhook{}
var _ webhook.CustomValidator = &ClickHouseClusterWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind ClickHouseCluster.
func (w *ClickHouseClusterWebhook) Default(_ context.Context, obj runtime.Object) error {
	cluster, ok := obj.(*chv1.ClickHouseCluster)
	if !ok {
		return fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	w.Log.Info("Fill defaults", "name", cluster.Name, "namespace", cluster.Namespace)
	cluster.Spec.WithDefaults()

	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateCreate(_ context.Context, obj runtime.Object) (admission.Warnings, error) {
	cluster, ok := obj.(*chv1.ClickHouseCluster)
	if !ok {
		return nil, fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	return w.validateImpl(cluster)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateUpdate(_ context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	oldCluster, ok := oldObj.(*chv1.ClickHouseCluster)
	if !ok {
		return nil, fmt.Errorf("unexpected old object type received %s", oldObj)
	}

	newCluster, ok := newObj.(*chv1.ClickHouseCluster)
	if !ok {
		return nil, fmt.Errorf("unexpected new object type received %s", newObj.GetObjectKind().GroupVersionKind())
	}

	warns, err := w.validateImpl(newCluster)
	if *oldCluster.Spec.Shards > *newCluster.Spec.Shards {
		warns = append(warns, "Decreasing the number of shards is a destructive operation. It removes shards with all their data.")
	}

	return warns, err
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (w *ClickHouseClusterWebhook) validateImpl(obj *chv1.ClickHouseCluster) (admission.Warnings, error) {
	w.Log.Info("Validating spec", "name", obj.Name, "namespace", obj.Namespace)

	var (
		warns admission.Warnings
		errs  []error
	)

	if obj.Spec.KeeperClusterRef == nil || obj.Spec.KeeperClusterRef.Name == "" {
		errs = append(errs, errors.New("keeperClusterRef name must not be empty"))
	}

	if err := obj.Spec.Settings.TLS.Validate(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, ValidateCustomVolumeMounts(obj.Spec.PodTemplate.Volumes, obj.Spec.ContainerTemplate.VolumeMounts, internal.ReservedClickHouseVolumeNames)...)

	if obj.Spec.Settings.DefaultUserPassword == nil {
		warns = append(warns, ".spec.settings.defaultUserPassword is empty, 'default' user will be without password ")
	} else {
		if err := obj.Spec.Settings.DefaultUserPassword.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	return warns, errors.Join(errs...)
}

/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	"context"
	"errors"
	"fmt"

	"github.com/clickhouse-operator/internal/controller/clickhouse"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	chv1 "github.com/clickhouse-operator/api/v1alpha1"
)

// log is for logging in this package.
var clickhouseWebhookLog = logf.Log.WithName("clickhouse-webhook")

// SetupClickHouseWebhookWithManager registers the webhook for ClickHouseCluster in the manager.
func SetupClickHouseWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&chv1.ClickHouseCluster{}).
		WithValidator(&ClickHouseClusterWebhook{}).
		WithDefaulter(&ClickHouseClusterWebhook{}).
		Complete()
}

// +kubebuilder:webhook:path=/mutate-clickhouse-com-v1alpha1-clickhousecluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=clickhouse.com,resources=clickhouseclusters,verbs=create;update,versions=v1alpha1,name=mclickhousecluster-v1alpha1.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-clickhouse-com-v1alpha1-clickhousecluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=clickhouse.com,resources=clickhouseclusters,verbs=create;update,versions=v1alpha1,name=vclickhousecluster-v1alpha1.kb.io,admissionReviewVersions=v1

type ClickHouseClusterWebhook struct{}

var _ webhook.CustomDefaulter = &ClickHouseClusterWebhook{}
var _ webhook.CustomValidator = &ClickHouseClusterWebhook{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind ClickHouseCluster.
func (w *ClickHouseClusterWebhook) Default(ctx context.Context, obj runtime.Object) error {
	cluster, ok := obj.(*chv1.ClickHouseCluster)
	if !ok {
		return fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	clickhouseWebhookLog.Info("default", "name", cluster.Name, "namespace", cluster.Namespace)
	cluster.Spec.WithDefaults()
	return nil
}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	return w.validateImpl(obj.(*chv1.ClickHouseCluster))
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type ClickHouseCluster.
func (w *ClickHouseClusterWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	return w.validateImpl(newObj.(*chv1.ClickHouseCluster))
}

func (w *ClickHouseClusterWebhook) ValidateDelete(context.Context, runtime.Object) (admission.Warnings, error) {
	return nil, nil
}

func (w *ClickHouseClusterWebhook) validateImpl(obj *chv1.ClickHouseCluster) (admission.Warnings, error) {
	var warns admission.Warnings
	var errs []error
	if obj.Spec.KeeperClusterRef == nil || obj.Spec.KeeperClusterRef.Name == "" {
		errs = append(errs, fmt.Errorf("keeperClusterRef name must not be empty"))
	}

	if err := obj.Spec.Settings.TLS.Validate(); err != nil {
		errs = append(errs, err)
	}

	errs = append(errs, ValidateCustomVolumeMounts(obj.Spec.PodTemplate.Volumes, obj.Spec.ContainerTemplate.VolumeMounts, clickhouse.ReservedVolumeNames)...)

	if obj.Spec.Settings.DefaultUserPassword == nil {
		warns = append(warns, ".spec.settings.defaultUserPassword is empty, 'default' user will be without password ")
	} else {
		if err := obj.Spec.Settings.DefaultUserPassword.Validate(); err != nil {
			errs = append(errs, err)
		}
	}

	return warns, errors.Join(errs...)
}

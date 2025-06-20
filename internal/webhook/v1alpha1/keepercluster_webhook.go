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
	"fmt"

	chv1 "github.com/clickhouse-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// log is for logging in this package.
var keeperWebhookLog = logf.Log.WithName("keeper-webhook")

// +kubebuilder:webhook:path=/mutate-clickhouse-com-v1alpha1-keepercluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=clickhouse.com,resources=keeperclusters,verbs=create;update,versions=v1alpha1,name=mkeepercluster.kb.io,admissionReviewVersions=v1
// +kubebuilder:webhook:path=/validate-clickhouse-com-v1alpha1-keepercluster,mutating=false,failurePolicy=fail,sideEffects=None,groups=clickhouse.com,resources=keeperclusters,verbs=create;update,versions=v1alpha1,name=vkeepercluster.kb.io,admissionReviewVersions=v1

type KeeperClusterWebhook struct{}

var _ webhook.CustomDefaulter = &KeeperClusterWebhook{}
var _ webhook.CustomValidator = &KeeperClusterWebhook{}

// SetupKeeperWebhookWithManager registers the webhook for KeeperCluster in the manager.
func SetupKeeperWebhookWithManager(mgr ctrl.Manager) error {
	webhook := &KeeperClusterWebhook{}
	return ctrl.NewWebhookManagedBy(mgr).
		For(&chv1.KeeperCluster{}).
		WithDefaulter(webhook).
		WithValidator(webhook).
		Complete()
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) Default(ctx context.Context, obj runtime.Object) error {
	keeperCluster, ok := obj.(*chv1.KeeperCluster)
	if !ok {
		return fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	keeperWebhookLog.Info("default", "name", keeperCluster.Name, "namespace", keeperCluster.Namespace)
	keeperCluster.Spec.WithDefaults()
	return nil
}

func (w *KeeperClusterWebhook) ValidateCreate(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, w.validateImpl(obj.(*chv1.KeeperCluster))
}

func (w *KeeperClusterWebhook) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, w.validateImpl(newObj.(*chv1.KeeperCluster))
}

func (w *KeeperClusterWebhook) ValidateDelete(ctx context.Context, obj runtime.Object) (warnings admission.Warnings, err error) {
	return nil, nil
}

func (w *KeeperClusterWebhook) validateImpl(obj *chv1.KeeperCluster) error {
	return obj.Spec.Settings.TLS.Validate()
}

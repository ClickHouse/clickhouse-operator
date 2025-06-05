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

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
)

// log is for logging in this package.
var keeperWebhookLog = logf.Log.WithName("keeper-webhook")

// +kubebuilder:webhook:path=/mutate-clickhouse-com-v1alpha1-keepercluster,mutating=true,failurePolicy=fail,sideEffects=None,groups=clickhouse.com,resources=keeperclusters,verbs=create;update,versions=v1alpha1,name=mkeepercluster.kb.io,admissionReviewVersions=v1

type KeeperClusterWebhook struct{}

var _ webhook.CustomDefaulter = &KeeperClusterWebhook{}

// SetupWebhookWithManager will setup the manager to manage the webhooks.
func (w *KeeperClusterWebhook) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).
		For(&KeeperCluster{}).
		WithDefaulter(w).
		Complete()
}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the type.
func (w *KeeperClusterWebhook) Default(ctx context.Context, obj runtime.Object) error {
	keeperCluster, ok := obj.(*KeeperCluster)
	if !ok {
		return fmt.Errorf("unexpected object type received %s", obj.GetObjectKind().GroupVersionKind())
	}

	keeperWebhookLog.Info("default", "name", keeperCluster.Name, "namespace", keeperCluster.Namespace)
	keeperCluster.Spec.WithDefaults()
	return nil
}

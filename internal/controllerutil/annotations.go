package controllerutil

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	AnnotationSpecHash    = "checksum/spec"
	AnnotationConfigHash  = "checksum/configuration"
	AnnotationRestartedAt = "kubectl.kubernetes.io/restartedAt"

	AnnotationStatefulSetVersion = "clickhouse.com/statefulset-version"
)

// AddHashWithKeyToAnnotations adds given spec hash to object's annotations with given key.
func AddHashWithKeyToAnnotations(obj client.Object, key string, specHash string) {
	annotations := obj.GetAnnotations()
	if annotations == nil {
		obj.SetAnnotations(map[string]string{
			key: specHash,
		})
	} else {
		annotations[key] = specHash
	}
}

// GetSpecHashFromObject retrieves spec hash from object's annotations.
func GetSpecHashFromObject(found client.Object) string {
	annotations := found.GetAnnotations()
	if annotations == nil || len(annotations[AnnotationSpecHash]) == 0 {
		return ""
	}

	return annotations[AnnotationSpecHash]
}

// GetConfigHashFromObject retrieves config hash from object's annotations.
func GetConfigHashFromObject(found client.Object) string {
	annotations := found.GetAnnotations()
	if annotations == nil || len(annotations[AnnotationConfigHash]) == 0 {
		return ""
	}

	return annotations[AnnotationConfigHash]
}

// AddObjectConfigHash adds given config hash to object's annotations.
func AddObjectConfigHash(obj client.Object, hash string) {
	AddHashWithKeyToAnnotations(obj, AnnotationConfigHash, hash)
}

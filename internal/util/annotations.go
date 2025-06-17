package util

import (
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	AnnotationSpecHash    = "checksum/spec"
	AnnotationConfigHash  = "checksum/configuration"
	AnnotationRestartedAt = "kubectl.kubernetes.io/restartedAt"

	AnnotationServerVersion      = "clickhouse.com/server-version"
	AnnotationStatefulSetVersion = "clickhouse.com/statefulset-version"
)

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

func GetSpecHashFromObject(found client.Object) string {
	annotations := found.GetAnnotations()
	if annotations == nil || len(annotations[AnnotationSpecHash]) == 0 {
		return ""
	}
	return annotations[AnnotationSpecHash]
}

func GetConfigHashFromObject(found client.Object) string {
	annotations := found.GetAnnotations()
	if annotations == nil || len(annotations[AnnotationConfigHash]) == 0 {
		return ""
	}
	return annotations[AnnotationConfigHash]
}

func AddObjectConfigHash(obj client.Object, hash string) {
	AddHashWithKeyToAnnotations(obj, AnnotationConfigHash, hash)
}

package clickhouse

import (
	"errors"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	chctrl "github.com/ClickHouse/clickhouse-operator/internal/controller"
	ctrlutil "github.com/ClickHouse/clickhouse-operator/internal/controllerutil"
)

var _ = Describe("UpdateStage", func() {
	rev := chctrl.RevisionState{
		StatefulSetRevision:   "sts-rev",
		ConfigurationRevision: "cfg-rev",
		RestartConfigRevision: "restart-rev",
		ReloadConfigRevision:  "reload-rev",
	}

	settledReplica := func() replicaState {
		sts := &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{
				Name:       "test-0-0",
				Generation: 1,
			},
			Spec: appsv1.StatefulSetSpec{
				Template: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{ctrlutil.AnnotationConfigHash: rev.RestartConfigRevision},
					},
				},
			},
			Status: appsv1.StatefulSetStatus{
				ObservedGeneration: 1,
				ReadyReplicas:      1,
				CurrentRevision:    "rev-1",
				UpdateRevision:     "rev-1",
			},
		}
		ctrlutil.AddSpecHashToObject(sts, rev.StatefulSetRevision)

		cfg := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "test-0-0-configmap"}}
		ctrlutil.AddObjectConfigHash(cfg, rev.ConfigurationRevision)

		return replicaState{
			replicaProbe: replicaProbe{
				Version:                   "26.5.1.1",
				ReloadConfigRevision:      rev.ReloadConfigRevision,
				UsersReloadConfigRevision: rev.ReloadConfigRevision,
			},
			ReplicaState: chctrl.ReplicaState{
				ReplicaResources: chctrl.ReplicaResources{STS: sts, CFG: cfg},
			},
		}
	}

	It("should report UpToDate for a settled healthy replica", func() {
		Expect(settledReplica().UpdateStage(rev)).To(Equal(chctrl.StageUpToDate))
	})

	It("should treat a reload failure as NotReadyUpToDate, not Error", func() {
		replica := settledReplica()
		replica.ReloadConfigRevision = ""
		replica.UsersReloadConfigRevision = ""
		replica.ReloadError = errors.New("dial tcp: connect: connection refused")

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageNotReadyUpToDate))
	})

	It("should keep a pending template change at HasDiff when the probe fails", func() {
		replica := settledReplica()
		replica.replicaProbe = replicaProbe{}
		replica.ReloadError = errors.New("dial tcp: i/o timeout")
		ctrlutil.AddSpecHashToObject(replica.STS, "next-sts-rev")

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageHasDiff))
	})

	It("should report Updating while waiting for a reload result", func() {
		replica := settledReplica()
		replica.replicaProbe = replicaProbe{}

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageUpdating))
	})

	It("should wait for an existing StatefulSet update before a new diff", func() {
		replica := settledReplica()
		replica.STS.Status.ObservedGeneration = 0
		ctrlutil.AddSpecHashToObject(replica.STS, "next-sts-rev")

		Expect(replica.UpdateStage(rev)).To(Equal(chctrl.StageUpdating))
	})
})

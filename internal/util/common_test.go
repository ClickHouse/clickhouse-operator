package util

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	ctrl "sigs.k8s.io/controller-runtime"
)

type InternalStruct struct {
	String string
	Number int
}

type SampleStruct struct {
	String  string
	Number  int
	Pointer *int
	Array   []int
	Map     map[int]string
	Struct  InternalStruct
}

func TestApplyDefault(t *testing.T) {
	varForPointer := 42
	allSet := SampleStruct{
		String:  "default",
		Number:  7,
		Pointer: &varForPointer,
		Array:   []int{1, 2, 3},
		Map:     map[int]string{1: "a", 2: "b", 3: "c"},
		Struct: InternalStruct{
			String: "internal",
			Number: 14,
		},
	}

	t.Run("all default values", func(t *testing.T) {
		source := SampleStruct{}
		require.NoError(t, ApplyDefault(&source, allSet))
		require.Equal(t, source, allSet)
	})

	t.Run("nothing to update", func(t *testing.T) {
		source := allSet
		defaults := SampleStruct{
			String: "another",
			Map:    map[int]string{1: "diff"},
			Struct: InternalStruct{
				Number: 77,
			},
		}
		require.NoError(t, ApplyDefault(&source, defaults))
		require.Equal(t, source, allSet)
	})

	t.Run("update several", func(t *testing.T) {
		source := allSet
		defaults := allSet

		source.String = ""
		source.Struct.Number = 100
		source.Map[7] = "custom"

		require.NoError(t, ApplyDefault(&source, defaults))
		require.Equal(t, source.String, allSet.String)
		require.Equal(t, source.Struct.Number, 100)
		require.Equal(t, source.Map[7], "custom")
	})
}

func TestUpdateResult(t *testing.T) {
	t.Run("update empty", func(t *testing.T) {
		result := ctrl.Result{}
		update := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, &update)
		require.True(t, result.Requeue)
		require.Equal(t, update.RequeueAfter, result.RequeueAfter)
	})

	t.Run("do not update with empty", func(t *testing.T) {
		result := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, nil)
		require.Equal(t, ctrl.Result{RequeueAfter: time.Second}, result)
	})

	t.Run("update to closer", func(t *testing.T) {
		result := ctrl.Result{RequeueAfter: time.Minute}
		update := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, &update)
		require.True(t, result.Requeue)
		require.Equal(t, time.Second, result.RequeueAfter)
	})

	t.Run("already better", func(t *testing.T) {
		result := ctrl.Result{RequeueAfter: time.Second}
		update := ctrl.Result{RequeueAfter: time.Minute}
		UpdateResult(&result, &update)
		require.True(t, result.Requeue)
		require.Equal(t, time.Second, result.RequeueAfter)
	})

	t.Run("update to minimal", func(t *testing.T) {
		result := ctrl.Result{RequeueAfter: time.Second}
		update := ctrl.Result{Requeue: true}
		UpdateResult(&result, &update)
		require.True(t, result.Requeue)
		require.Equal(t, time.Duration(0), result.RequeueAfter)
	})

	t.Run("already minimal", func(t *testing.T) {
		result := ctrl.Result{Requeue: true}
		update := ctrl.Result{RequeueAfter: time.Second}
		UpdateResult(&result, &update)
		require.True(t, result.Requeue)
		require.Equal(t, time.Duration(0), result.RequeueAfter)
	})
}

package controller

import (
	"context"
	"reflect"
	"testing"

	enginev1alpha1 "github.com/shadow-diff/monarch/api/v1alpha1"
)

func TestRunBootGatesPreservesOrderAndSkips(t *testing.T) {
	t.Parallel()
	var calls []string
	gate := func(name string, skip bool) bootGate {
		return bootGate{
			name: name,
			skip: skip,
			reconcile: func(context.Context) error {
				calls = append(calls, "reconcile "+name)
				return nil
			},
			ready: func(context.Context) (bool, workloadWaitReason, error) {
				calls = append(calls, "ready "+name)
				return true, workloadWaitReason{}, nil
			},
		}
	}

	r := &ShadowTestReconciler{}
	boot := enginev1alpha1.ComponentStatus{}
	done, result, err := r.runBootGates(context.Background(), &enginev1alpha1.ShadowTest{}, "shadow-test", &boot,
		[]bootGate{gate("first", false), gate("skipped", true), gate("last", false)})
	if err != nil || !done || result.Requeue || result.RequeueAfter != 0 {
		t.Fatalf("done=%v result=%+v err=%v", done, result, err)
	}
	want := []string{"reconcile first", "ready first", "reconcile last", "ready last"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls = %v, want %v", calls, want)
	}
}

func TestReplayBootGateOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		st   *enginev1alpha1.ShadowTest
		want []string
	}{
		{
			name: "http",
			st:   &enginev1alpha1.ShadowTest{},
			want: []string{"Shop", "Igris", "shadow Deployments"},
		},
		{
			name: "rabbitmq ingress",
			st: &enginev1alpha1.ShadowTest{Spec: enginev1alpha1.ShadowTestSpec{
				Inputs: []enginev1alpha1.InputSpec{{Driver: "rabbitmq_message"}},
			}},
			want: []string{"Shop", "igris-rabbitmq", "egress-relay-rabbitmq", "shadow Deployments"},
		},
		{
			name: "http with rabbitmq dependency",
			st: &enginev1alpha1.ShadowTest{Spec: enginev1alpha1.ShadowTestSpec{
				Dependencies: []enginev1alpha1.DependencySpec{{Name: "broker", Type: "rabbitmq"}},
			}},
			want: []string{"shadow dependencies", "Shop", "Igris", "egress-relay-rabbitmq", "shadow Deployments"},
		},
	}

	r := &ShadowTestReconciler{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var got []string
			for _, gate := range r.replayBootGates(tt.st, "shadow-test", nil, nil) {
				if !gate.skip {
					got = append(got, gate.name)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("gate order = %v, want %v", got, tt.want)
			}
		})
	}
}

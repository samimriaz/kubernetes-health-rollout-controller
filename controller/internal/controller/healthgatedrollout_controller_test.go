/*
Copyright 2026.

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

package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	rolloutv1alpha1 "github.com/samimriaz/kubernetes-health-rollout-controller/controller/api/v1alpha1"
)

type signalProviderFunc func(context.Context, *rolloutv1alpha1.HealthGatedRolloutSpec) (SignalResult, error)

func (function signalProviderFunc) Evaluate(ctx context.Context, spec *rolloutv1alpha1.HealthGatedRolloutSpec) (SignalResult, error) {
	return function(ctx, spec)
}

func TestReconcileHoldsWhenMetricsAreUnavailable(t *testing.T) {
	reconciler, key := newTestReconciler(t, signalProviderFunc(func(context.Context, *rolloutv1alpha1.HealthGatedRolloutSpec) (SignalResult, error) {
		return SignalResult{}, ErrNoData
	}))

	reconcileOnce(t, reconciler, key)
	rollout := getRollout(t, reconciler, key)
	if rollout.Status.Phase != phaseHolding {
		t.Fatalf("phase = %q, want %q", rollout.Status.Phase, phaseHolding)
	}
	assertReplicas(t, reconciler, key.Namespace, "inference-canary", 1)
}

func TestReconcileAdvancesHealthyCanary(t *testing.T) {
	reconciler, key := newTestReconciler(t, staticSignals(SignalResult{RequestCount: 20, ErrorRate: 0.01, Latency: 0.1}))

	reconcileOnce(t, reconciler, key)
	rollout := getRollout(t, reconciler, key)
	if rollout.Status.CurrentStep != 1 {
		t.Fatalf("current step = %d, want 1", rollout.Status.CurrentStep)
	}
	assertReplicas(t, reconciler, key.Namespace, "inference-stable", 1)
}

func TestReconcileRollsBackAfterConsecutiveFailures(t *testing.T) {
	reconciler, key := newTestReconciler(t, staticSignals(SignalResult{RequestCount: 20, ErrorRate: 0.5, Latency: 1}))

	reconcileOnce(t, reconciler, key)
	rollout := getRollout(t, reconciler, key)
	if rollout.Status.ConsecutiveFailures != 1 {
		t.Fatalf("failures after first sample = %d, want 1", rollout.Status.ConsecutiveFailures)
	}
	assertReplicas(t, reconciler, key.Namespace, "inference-canary", 1)

	reconcileOnce(t, reconciler, key)
	rollout = getRollout(t, reconciler, key)
	if rollout.Status.ConsecutiveFailures != 1 {
		t.Fatalf("failures after immediate reconcile = %d, want 1", rollout.Status.ConsecutiveFailures)
	}

	reconciler.Now = func() time.Time { return time.Unix(1_015, 0) }
	reconcileOnce(t, reconciler, key)
	rollout = getRollout(t, reconciler, key)
	if rollout.Status.Phase != phaseRolledBack {
		t.Fatalf("phase = %q, want %q", rollout.Status.Phase, phaseRolledBack)
	}
	assertReplicas(t, reconciler, key.Namespace, "inference-canary", 0)
	assertReplicas(t, reconciler, key.Namespace, "inference-stable", 1)
}

func TestReconcileRollsBackForAdditionalSignal(t *testing.T) {
	signals := SignalResult{
		RequestCount: 20,
		ErrorRate:    0,
		Latency:      0.1,
		AdditionalChecks: []MetricCheckResult{{
			Name: "simulated GPU temperature", Value: 96, MaxValue: 85,
		}},
	}
	reconciler, key := newTestReconciler(t, staticSignals(signals))

	reconcileOnce(t, reconciler, key)
	reconciler.Now = func() time.Time { return time.Unix(1_015, 0) }
	reconcileOnce(t, reconciler, key)

	rollout := getRollout(t, reconciler, key)
	if rollout.Status.Phase != phaseRolledBack {
		t.Fatalf("phase = %q, want %q", rollout.Status.Phase, phaseRolledBack)
	}
	if got := rollout.Status.Conditions[0].Message; !strings.Contains(got, "simulated GPU temperature") {
		t.Fatalf("condition message %q does not identify the failed GPU signal", got)
	}
}
func newTestReconciler(t *testing.T, provider SignalProvider) (*HealthGatedRolloutReconciler, types.NamespacedName) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := rolloutv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	replicas := int32(1)
	key := types.NamespacedName{Namespace: "workload", Name: "inference-rollout"}
	rollout := &rolloutv1alpha1.HealthGatedRollout{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Generation: 1},
		Spec: rolloutv1alpha1.HealthGatedRolloutSpec{
			StableDeployment: "inference-stable", CanaryDeployment: "inference-canary", RollbackStableReplicas: 1,
			Steps:         []rolloutv1alpha1.RolloutStep{{StableReplicas: 9, CanaryReplicas: 1}, {StableReplicas: 1, CanaryReplicas: 1}},
			PrometheusURL: "http://prometheus", RequestCountQuery: "requests", ErrorRateQuery: "errors", LatencyQuery: "latency",
			MinimumRequestCount: 10, MaxErrorRate: resource.MustParse("0.05"), MaxLatencySeconds: resource.MustParse("0.5"),
			FailureThreshold: 2, EvaluationIntervalSeconds: 15, CooldownSeconds: 300,
			AdditionalChecks: []rolloutv1alpha1.MetricCheck{{
				Name: "simulated GPU temperature", Query: "gpu_temperature", MaxValue: resource.MustParse("85"),
			}},
		},
		Status: rolloutv1alpha1.HealthGatedRolloutStatus{ObservedGeneration: 1, Phase: phaseProgressing},
	}
	stable := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "inference-stable", Namespace: key.Namespace}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
	canary := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "inference-canary", Namespace: key.Namespace}, Spec: appsv1.DeploymentSpec{Replicas: &replicas}}
	testClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(rollout).WithObjects(rollout, stable, canary).Build()
	return &HealthGatedRolloutReconciler{Client: testClient, Scheme: scheme, SignalProvider: provider, Now: func() time.Time { return time.Unix(1_000, 0) }}, key
}

func staticSignals(signals SignalResult) SignalProvider {
	return signalProviderFunc(func(context.Context, *rolloutv1alpha1.HealthGatedRolloutSpec) (SignalResult, error) {
		return signals, nil
	})
}

func reconcileOnce(t *testing.T, reconciler *HealthGatedRolloutReconciler, key types.NamespacedName) {
	t.Helper()
	if _, err := reconciler.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatal(err)
	}
}

func getRollout(t *testing.T, reconciler *HealthGatedRolloutReconciler, key types.NamespacedName) *rolloutv1alpha1.HealthGatedRollout {
	t.Helper()
	rollout := &rolloutv1alpha1.HealthGatedRollout{}
	if err := reconciler.Get(context.Background(), key, rollout); err != nil {
		t.Fatal(err)
	}
	return rollout
}

func assertReplicas(t *testing.T, reconciler *HealthGatedRolloutReconciler, namespace, name string, want int32) {
	t.Helper()
	deployment := &appsv1.Deployment{}
	if err := reconciler.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		t.Fatal(err)
	}
	if got := *deployment.Spec.Replicas; got != want {
		t.Fatalf("%s replicas = %d, want %d", name, got, want)
	}
}

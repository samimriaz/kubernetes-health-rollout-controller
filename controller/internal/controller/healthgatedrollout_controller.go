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
	"fmt"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/metrics"

	rolloutv1alpha1 "github.com/samimriaz/kubernetes-health-rollout-controller/controller/api/v1alpha1"
)

// HealthGatedRolloutReconciler reconciles a HealthGatedRollout object
type HealthGatedRolloutReconciler struct {
	client.Client
	Scheme         *runtime.Scheme
	SignalProvider SignalProvider
	Recorder       record.EventRecorder
	Now            func() time.Time
}

const (
	phaseProgressing = "Progressing"
	phaseHolding     = "Holding"
	phasePromoted    = "Promoted"
	phaseRolledBack  = "RolledBack"
)

var rollbackCounter = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "health_gated_rollout_rollbacks_total",
	Help: "Number of health-gated canary rollbacks.",
}, []string{"namespace", "name"})

func init() {
	metrics.Registry.MustRegister(rollbackCounter)
}

// +kubebuilder:rbac:groups=rollout.healthrollout.io,resources=healthgatedrollouts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rollout.healthrollout.io,resources=healthgatedrollouts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=rollout.healthrollout.io,resources=healthgatedrollouts/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.25.0/pkg/reconcile
func (r *HealthGatedRolloutReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	rollout := &rolloutv1alpha1.HealthGatedRollout{}
	if err := r.Get(ctx, req.NamespacedName, rollout); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	interval := time.Duration(rollout.Spec.EvaluationIntervalSeconds) * time.Second
	if interval == 0 {
		interval = 15 * time.Second
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}

	if err := validateSpec(&rollout.Spec); err != nil {
		return ctrl.Result{}, err
	}

	if rollout.Status.ObservedGeneration != rollout.Generation {
		if err := r.applyStep(ctx, rollout, 0); err != nil {
			return ctrl.Result{}, err
		}
		rollout.Status = rolloutv1alpha1.HealthGatedRolloutStatus{
			ObservedGeneration: rollout.Generation,
			Phase:              phaseProgressing,
			CurrentStep:        0,
		}
		setCondition(rollout, metav1.ConditionTrue, "RolloutStarted", "Canary rollout started")
		return ctrl.Result{RequeueAfter: interval}, r.Status().Update(ctx, rollout)
	}

	if rollout.Status.Phase == phasePromoted {
		return ctrl.Result{}, nil
	}
	if rollout.Status.Phase == phaseRolledBack && rollout.Status.RollbackTime != nil {
		cooldown := time.Duration(rollout.Spec.CooldownSeconds) * time.Second
		remaining := cooldown - now().Sub(rollout.Status.RollbackTime.Time)
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
		if err := r.applyStep(ctx, rollout, 0); err != nil {
			return ctrl.Result{}, err
		}
		rollout.Status.Phase = phaseProgressing
		rollout.Status.CurrentStep = 0
		rollout.Status.ConsecutiveFailures = 0
		rollout.Status.RollbackTime = nil
		setCondition(rollout, metav1.ConditionTrue, "CooldownComplete", "Retrying canary rollout")
		return ctrl.Result{RequeueAfter: interval}, r.Status().Update(ctx, rollout)
	}
	if rollout.Status.LastEvaluationTime != nil {
		remaining := interval - now().Sub(rollout.Status.LastEvaluationTime.Time)
		if remaining > 0 {
			return ctrl.Result{RequeueAfter: remaining}, nil
		}
	}

	provider := r.SignalProvider
	if provider == nil {
		provider = NewPrometheusSignalProvider()
	}
	signals, err := provider.Evaluate(ctx, &rollout.Spec)
	evaluatedAt := metav1.NewTime(now())
	rollout.Status.LastEvaluationTime = &evaluatedAt
	if err != nil {
		logger.Info("Holding rollout because Prometheus data is unavailable", "error", err)
		rollout.Status.Phase = phaseHolding
		setCondition(rollout, metav1.ConditionUnknown, "MetricsUnavailable", err.Error())
		return ctrl.Result{RequeueAfter: interval}, r.Status().Update(ctx, rollout)
	}
	if signals.RequestCount < float64(rollout.Spec.MinimumRequestCount) {
		rollout.Status.Phase = phaseHolding
		setCondition(rollout, metav1.ConditionUnknown, "InsufficientSamples",
			fmt.Sprintf("Observed %.0f of %d required requests", signals.RequestCount, rollout.Spec.MinimumRequestCount))
		return ctrl.Result{RequeueAfter: interval}, r.Status().Update(ctx, rollout)
	}

	maxErrorRate := rollout.Spec.MaxErrorRate.AsApproximateFloat64()
	maxLatency := rollout.Spec.MaxLatencySeconds.AsApproximateFloat64()
	breaches := make([]string, 0, len(signals.AdditionalChecks)+2)
	if signals.ErrorRate > maxErrorRate {
		breaches = append(breaches, fmt.Sprintf("error rate %.4f > %.4f", signals.ErrorRate, maxErrorRate))
	}
	if signals.Latency > maxLatency {
		breaches = append(breaches, fmt.Sprintf("latency %.4fs > %.4fs", signals.Latency, maxLatency))
	}
	for _, check := range signals.AdditionalChecks {
		if check.Value > check.MaxValue {
			breaches = append(breaches, fmt.Sprintf("%s %.4f > %.4f", check.Name, check.Value, check.MaxValue))
		}
	}
	unhealthy := len(breaches) > 0
	if unhealthy {
		rollout.Status.Phase = phaseProgressing
		rollout.Status.ConsecutiveFailures++
		message := "Health thresholds exceeded: " + strings.Join(breaches, ", ")
		setCondition(rollout, metav1.ConditionFalse, "HealthThresholdExceeded", message)
		if rollout.Status.ConsecutiveFailures < rollout.Spec.FailureThreshold {
			return ctrl.Result{RequeueAfter: interval}, r.Status().Update(ctx, rollout)
		}
		if err := r.rollback(ctx, rollout); err != nil {
			return ctrl.Result{}, err
		}
		rollout.Status.Phase = phaseRolledBack
		rollout.Status.RollbackTime = &evaluatedAt
		setCondition(rollout, metav1.ConditionFalse, "RolloutRolledBack", message)
		rollbackCounter.WithLabelValues(rollout.Namespace, rollout.Name).Inc()
		if r.Recorder != nil {
			r.Recorder.Event(rollout, corev1.EventTypeWarning, "RolloutRolledBack", message)
		}
		return ctrl.Result{RequeueAfter: time.Duration(rollout.Spec.CooldownSeconds) * time.Second}, r.Status().Update(ctx, rollout)
	}

	rollout.Status.ConsecutiveFailures = 0
	rollout.Status.Phase = phaseProgressing
	nextStep := rollout.Status.CurrentStep + 1
	if int(nextStep) >= len(rollout.Spec.Steps) {
		rollout.Status.Phase = phasePromoted
		setCondition(rollout, metav1.ConditionTrue, "RolloutPromoted", "All canary steps passed their health checks")
		if r.Recorder != nil {
			r.Recorder.Event(rollout, corev1.EventTypeNormal, "RolloutPromoted", "All canary steps passed their health checks")
		}
		return ctrl.Result{}, r.Status().Update(ctx, rollout)
	}
	if err := r.applyStep(ctx, rollout, nextStep); err != nil {
		return ctrl.Result{}, err
	}
	rollout.Status.CurrentStep = nextStep
	setCondition(rollout, metav1.ConditionTrue, "StepAdvanced", fmt.Sprintf("Advanced to rollout step %d", nextStep))
	return ctrl.Result{RequeueAfter: interval}, r.Status().Update(ctx, rollout)

	// Reconciliation deliberately treats failed or empty Prometheus queries as a hold.
	// It never promotes or rolls back without an actual health sample.
}

func validateSpec(spec *rolloutv1alpha1.HealthGatedRolloutSpec) error {
	if len(spec.Steps) == 0 {
		return fmt.Errorf("spec.steps must not be empty")
	}
	if spec.MaxErrorRate.Sign() < 0 || spec.MaxErrorRate.AsApproximateFloat64() > 1 {
		return fmt.Errorf("spec.maxErrorRate must be between 0 and 1")
	}
	if spec.MaxLatencySeconds.Sign() <= 0 {
		return fmt.Errorf("spec.maxLatencySeconds must be greater than 0")
	}
	for _, check := range spec.AdditionalChecks {
		if check.MaxValue.Sign() < 0 {
			return fmt.Errorf("spec.additionalChecks[%s].maxValue must not be negative", check.Name)
		}
	}
	return nil
}

func (r *HealthGatedRolloutReconciler) applyStep(
	ctx context.Context,
	rollout *rolloutv1alpha1.HealthGatedRollout,
	stepIndex int32,
) error {
	step := rollout.Spec.Steps[stepIndex]
	if err := r.scaleDeployment(ctx, rollout.Namespace, rollout.Spec.StableDeployment, step.StableReplicas); err != nil {
		return err
	}
	return r.scaleDeployment(ctx, rollout.Namespace, rollout.Spec.CanaryDeployment, step.CanaryReplicas)
}

func (r *HealthGatedRolloutReconciler) rollback(ctx context.Context, rollout *rolloutv1alpha1.HealthGatedRollout) error {
	if err := r.scaleDeployment(ctx, rollout.Namespace, rollout.Spec.StableDeployment, rollout.Spec.RollbackStableReplicas); err != nil {
		return err
	}
	return r.scaleDeployment(ctx, rollout.Namespace, rollout.Spec.CanaryDeployment, 0)
}

func (r *HealthGatedRolloutReconciler) scaleDeployment(
	ctx context.Context,
	namespace, name string,
	replicas int32,
) error {
	deployment := &appsv1.Deployment{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("deployment %s/%s not found", namespace, name)
		}
		return err
	}
	if deployment.Spec.Replicas != nil && *deployment.Spec.Replicas == replicas {
		return nil
	}
	deployment.Spec.Replicas = &replicas
	return r.Update(ctx, deployment)
}

func setCondition(rollout *rolloutv1alpha1.HealthGatedRollout, status metav1.ConditionStatus, reason, message string) {
	apimeta.SetStatusCondition(&rollout.Status.Conditions, metav1.Condition{
		Type:               "Ready",
		Status:             status,
		ObservedGeneration: rollout.Generation,
		Reason:             reason,
		Message:            message,
	})
}

// SetupWithManager sets up the controller with the Manager.
func (r *HealthGatedRolloutReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&rolloutv1alpha1.HealthGatedRollout{}).
		Named("healthgatedrollout").
		Complete(r)
}

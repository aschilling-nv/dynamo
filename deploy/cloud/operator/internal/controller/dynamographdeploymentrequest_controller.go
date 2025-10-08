/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 * http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package controller

import (
	"context"
	"errors"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/yaml"

	nvidiacomv1alpha1 "github.com/ai-dynamo/dynamo/deploy/cloud/operator/api/v1alpha1"
	commonController "github.com/ai-dynamo/dynamo/deploy/cloud/operator/internal/controller_common"
)

const (
	// State constants
	StateEmpty             = ""
	StatePending           = "Pending"
	StateProfiling         = "Profiling"
	StateDeploying         = "Deploying"
	StateReady             = "Ready"
	StateDeploymentDeleted = "DeploymentDeleted"
	StateFailed            = "Failed"

	// Condition types
	ConditionTypeValidation      = "Validation"
	ConditionTypeProfiling       = "Profiling"
	ConditionTypeSpecGenerated   = "SpecGenerated"
	ConditionTypeDeploymentReady = "DeploymentReady"

	// Event reasons
	EventReasonInitialized          = "Initialized"
	EventReasonValidationFailed     = "ValidationFailed"
	EventReasonProfilingJobCreated  = "ProfilingJobCreated"
	EventReasonProfilingJobFailed   = "ProfilingJobFailed"
	EventReasonAIConfiguratorFailed = "AIConfiguratorFailed"
	EventReasonSpecGenerated        = "SpecGenerated"
	EventReasonSpecChangeRejected   = "SpecChangeRejected"
	EventReasonDeploymentCreated    = "DeploymentCreated"
	EventReasonDeploymentReady      = "DeploymentReady"
	EventReasonDeploymentDegraded   = "DeploymentDegraded"
	EventReasonDeploymentDeleted    = "DeploymentDeleted"

	// Label keys
	LabelApp       = "app"
	LabelDGDR      = "dgdr"
	LabelDGDRName  = "dgdr.nvidia.com/name"
	LabelManagedBy = "nvidia.com/managed-by"

	// Label values
	LabelValueDynamoProfiler = "dynamo-profiler"
	LabelValueAICProfiler    = "aic-profiler"
	LabelValueDynamoOperator = "dynamo-operator"

	// Job naming
	JobNamePrefixOnline = "profile-online-"
	JobNamePrefixAIC    = "profile-aic-"

	// Container names
	ContainerNameProfiler     = "profiler"
	ContainerNameOutputCopier = "output-copier"

	// ServiceAccount
	ServiceAccountProfilingJob = "dgdr-profiling-job"

	// ConfigMap naming
	ConfigMapOutputPrefix = "dgdr-output-"

	// Sidecar image
	SidecarImage = "bitnami/kubectl:latest"

	// Volume names
	VolumeNameProfilingConfig = "profiling-config"
	VolumeNameProfilingOutput = "profiling-output"

	// Volume paths
	ProfilingOutputPath = "/output"
	ProfilingOutputFile = "k8s_deploy.yaml"
	ProfilingConfigPath = "/config"
	ProfilingConfigFile = "disagg.yaml"

	// Command line arguments
	ArgModel   = "--model"
	ArgBackend = "--backend"
	ArgTTFT    = "--ttft"
	ArgITL     = "--itl"
	ArgConfig  = "--config"

	// Messages
	MessageInitialized               = "DGDR initialized successfully"
	MessageProfilingJobCreated       = "Profiling job created"
	MessageAICProfilingJobCreated    = "AIC profiling job created"
	MessageProfilingInProgress       = "Profiling is in progress"
	MessageSpecGenerated             = "DynamoGraphDeployment spec generated successfully"
	MessageSpecAvailable             = "Generated spec is available in status.generatedSpec"
	MessageDeploymentCreated         = "DynamoGraphDeployment %s created successfully"
	MessageDeploymentReady           = "DynamoGraphDeployment %s is ready"
	MessageDeploymentDegraded        = "DynamoGraphDeployment %s degraded from Ready to %s"
	MessageDeploymentDeleted         = "DGD %s was deleted. DGDR will not recreate it. Delete this DGDR and create a new one to redeploy."
	MessageInvalidState              = "Invalid state"
	MessageSpecChangeRejected        = "Cannot modify spec in state '%s'. DynamoGraphDeploymentRequest is immutable once profiling starts. Create a new resource with a different name instead."
	MessageJobCreationFailed         = "JobCreationFailed"
	MessageResultsRetrievalFailed    = "ResultsRetrievalFailed"
	MessageGenerationFailed          = "GenerationFailed"
	MessageAIConfiguratorCheckFailed = "AIConfiguratorCheckFailed"
	MessageProfilingCheckFailed      = "ProfilingCheckFailed"
	MessageConfigMapNotFound         = "ConfigMap %s not found in namespace %s"
	MessageConfigMapKeyNotFound      = "key %s not found in ConfigMap %s"

	// Validation messages
	ValidationErrorModelNameRequired = "modelName is required"
	ValidationErrorITLPositive       = "sla.itl must be positive"
	ValidationErrorTTFTPositive      = "sla.ttft must be positive"
	ValidationErrorInvalidBackend    = "invalid backend: %s (must be vllm, sglang, or trtllm)"

	// Valid backend values
	BackendVLLM   = "vllm"
	BackendSGLang = "sglang"
	BackendTRTLLM = "trtllm"
)

// DynamoGraphDeploymentRequestReconciler reconciles a DynamoGraphDeploymentRequest object
type DynamoGraphDeploymentRequestReconciler struct {
	client.Client
	Recorder record.EventRecorder
	Config   commonController.Config

	// OnlineProfilingImage is the container image to use for online profiling jobs
	OnlineProfilingImage string
	// AICProfilingImage is the container image to use for AIC (AI Configurator) profiling jobs
	AICProfilingImage string
	// RBACMgr handles RBAC setup for profiling jobs
	RBACMgr RBACManager
}

// RBACManager interface for managing RBAC resources
type RBACManager interface {
	EnsureServiceAccountWithRBAC(ctx context.Context, targetNamespace, serviceAccountName, clusterRoleName string) error
}

// GetRecorder implements commonController.Reconciler interface
func (r *DynamoGraphDeploymentRequestReconciler) GetRecorder() record.EventRecorder {
	return r.Recorder
}

// FinalizeResource implements commonController.Finalizer interface
func (r *DynamoGraphDeploymentRequestReconciler) FinalizeResource(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) error {
	logger := log.FromContext(ctx)
	logger.Info("Finalizing DGDR", "name", dgdr.Name)

	// Cleanup profiling resources
	if err := r.cleanupProfilingResources(ctx, dgdr); err != nil {
		logger.Error(err, "Failed to cleanup profiling resources")
		return err
	}

	logger.Info("DGDR finalized successfully", "name", dgdr.Name)
	return nil
}

// +kubebuilder:rbac:groups=nvidia.com,resources=dynamographdeploymentrequests,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=nvidia.com,resources=dynamographdeploymentrequests/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=nvidia.com,resources=dynamographdeploymentrequests/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=events,verbs=create;patch

// Reconcile handles the reconciliation loop for DynamoGraphDeploymentRequest
func (r *DynamoGraphDeploymentRequestReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Reconciling DynamoGraphDeploymentRequest", "name", req.Name, "namespace", req.Namespace)

	// Fetch the DGDR instance
	dgdr := &nvidiacomv1alpha1.DynamoGraphDeploymentRequest{}
	if err := r.Get(ctx, req.NamespacedName, dgdr); err != nil {
		if apierrors.IsNotFound(err) {
			logger.Info("DGDR resource not found, ignoring since object must be deleted")
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get DGDR")
		return ctrl.Result{}, err
	}

	// Handle finalizer using common function
	finalized, err := commonController.HandleFinalizer(ctx, dgdr, r.Client, r)
	if err != nil {
		return ctrl.Result{}, err
	}
	if finalized {
		// Resource was deleted and finalized
		return ctrl.Result{}, nil
	}

	// Check for spec changes (immutability enforcement)
	if dgdr.Status.ObservedGeneration > 0 && dgdr.Status.ObservedGeneration != dgdr.Generation {
		// Spec has changed after initial processing
		if dgdr.Status.State == StateProfiling || dgdr.Status.State == StateReady {
			logger.Info("Spec change detected in immutable state",
				"state", dgdr.Status.State,
				"observedGeneration", dgdr.Status.ObservedGeneration,
				"currentGeneration", dgdr.Generation)

			r.Recorder.Event(dgdr, corev1.EventTypeWarning, EventReasonSpecChangeRejected,
				fmt.Sprintf(MessageSpecChangeRejected, dgdr.Status.State))

			// Keep the old observedGeneration to continue rejecting changes
			// No state transition - stay in current state with old spec
			return ctrl.Result{}, nil
		}
	}

	// State machine: handle different states
	switch dgdr.Status.State {
	case StateEmpty:
		return r.handleInitialState(ctx, dgdr)
	case StatePending:
		return r.handlePendingState(ctx, dgdr)
	case StateProfiling:
		return r.handleProfilingState(ctx, dgdr)
	case StateDeploying:
		return r.handleDeployingState(ctx, dgdr)
	case StateReady:
		return r.handleReadyState(ctx, dgdr)
	case StateDeploymentDeleted:
		return r.handleDeploymentDeletedState(ctx, dgdr)
	case StateFailed:
		return r.handleFailedState(ctx, dgdr)
	default:
		logger.Info("Unknown state", "state", dgdr.Status.State)
		return r.updateStateAndRequeue(ctx, dgdr, StateFailed, MessageInvalidState)
	}
}

// handleInitialState processes newly created DGDR resources
func (r *DynamoGraphDeploymentRequestReconciler) handleInitialState(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling initial state", "name", dgdr.Name)

	// Validate the spec
	if err := r.validateSpec(ctx, dgdr); err != nil {
		r.Recorder.Event(dgdr, corev1.EventTypeWarning, EventReasonValidationFailed, err.Error())
		return r.updateStateWithCondition(ctx, dgdr, StateFailed, ConditionTypeValidation, metav1.ConditionFalse, EventReasonValidationFailed, err.Error())
	}

	// Set observedGeneration to track the spec we're processing
	dgdr.Status.ObservedGeneration = dgdr.Generation

	// Initialize status
	r.Recorder.Event(dgdr, corev1.EventTypeNormal, EventReasonInitialized, MessageInitialized)
	return r.updateStateAndRequeue(ctx, dgdr, StatePending, MessageInitialized)
}

// handlePendingState starts the profiling process
func (r *DynamoGraphDeploymentRequestReconciler) handlePendingState(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling pending state", "name", dgdr.Name)

	// Create profiling job (online or AIC)
	if err := r.createProfilingJob(ctx, dgdr); err != nil {
		r.Recorder.Event(dgdr, corev1.EventTypeWarning, EventReasonProfilingJobFailed, err.Error())
		return r.updateStateWithCondition(ctx, dgdr, StateFailed, ConditionTypeProfiling, metav1.ConditionFalse, MessageJobCreationFailed, err.Error())
	}

	// Record event with appropriate message
	if dgdr.Spec.Online {
		r.Recorder.Event(dgdr, corev1.EventTypeNormal, EventReasonProfilingJobCreated, MessageProfilingJobCreated)
	} else {
		r.Recorder.Event(dgdr, corev1.EventTypeNormal, EventReasonProfilingJobCreated, MessageAICProfilingJobCreated)
	}

	// Update to Profiling state with Running status
	return r.updateStateWithCondition(ctx, dgdr, StateProfiling, ConditionTypeProfiling, metav1.ConditionFalse, "ProfilingRunning", MessageProfilingInProgress)
}

// handleProfilingState monitors profiling progress and generates spec when complete
func (r *DynamoGraphDeploymentRequestReconciler) handleProfilingState(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling profiling state", "name", dgdr.Name)

	// Check profiling job status (both online and AIC run as Jobs now)
	// Note: We watch the Job via Owns(), so we'll be triggered automatically on Job changes
	completed, err := r.checkProfilingJobStatus(ctx, dgdr)
	if err != nil {
		r.Recorder.Event(dgdr, corev1.EventTypeWarning, MessageProfilingCheckFailed, err.Error())
		// Job failed - transition to Failed state
		return r.updateStateWithCondition(ctx, dgdr, StateFailed, ConditionTypeProfiling, metav1.ConditionFalse, "ProfilingFailed", err.Error())
	}

	if !completed {
		logger.Info("Profiling job still running", "name", dgdr.Name)
		// Don't requeue - we'll be triggered when the Job completes/fails
		return ctrl.Result{}, nil
	}

	// Mark profiling as completed successfully
	meta.SetStatusCondition(&dgdr.Status.Conditions, metav1.Condition{
		Type:               ConditionTypeProfiling,
		Status:             metav1.ConditionTrue,
		ObservedGeneration: dgdr.Generation,
		Reason:             "ProfilingCompleted",
		Message:            "Profiling job completed successfully",
	})

	// Retrieve profiling results and generate spec
	if err := r.generateDGDSpec(ctx, dgdr); err != nil {
		r.Recorder.Event(dgdr, corev1.EventTypeWarning, MessageGenerationFailed, err.Error())
		return r.updateStateWithCondition(ctx, dgdr, StateFailed, ConditionTypeSpecGenerated, metav1.ConditionFalse, MessageGenerationFailed, err.Error())
	}

	// Record spec generation event
	r.Recorder.Event(dgdr, corev1.EventTypeNormal, EventReasonSpecGenerated, MessageSpecGenerated)

	// If autoApply is enabled, transition to Deploying state
	if dgdr.Spec.AutoApply {
		logger.Info("AutoApply enabled, transitioning to Deploying state")
		return r.updateStateWithCondition(ctx, dgdr, StateDeploying, ConditionTypeSpecGenerated, metav1.ConditionTrue, EventReasonSpecGenerated, MessageSpecGenerated)
	}

	// Otherwise, transition to Ready state
	return r.updateStateWithCondition(ctx, dgdr, StateReady, ConditionTypeSpecGenerated, metav1.ConditionTrue, EventReasonSpecGenerated, MessageSpecAvailable)
}

// handleReadyState handles DGDR in Ready state
func (r *DynamoGraphDeploymentRequestReconciler) handleReadyState(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("DGDR is ready", "name", dgdr.Name)

	// If autoApply is not enabled, nothing to monitor
	if !dgdr.Spec.AutoApply {
		return ctrl.Result{}, nil
	}

	// Check if DGD still exists and monitor its status
	dgd := &nvidiacomv1alpha1.DynamoGraphDeployment{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      dgdr.Status.Deployment.Name,
		Namespace: dgdr.Status.Deployment.Namespace,
	}, dgd)

	if apierrors.IsNotFound(err) {
		// DGD was deleted by user
		return r.handleDGDDeleted(ctx, dgdr)
	}

	if err != nil {
		return ctrl.Result{}, err
	}

	// Update deployment status
	dgdr.Status.Deployment.State = dgd.Status.State

	// Check if DGD degraded from Ready
	if dgd.Status.State != "Ready" {
		logger.Info("DGD degraded, transitioning back to Deploying",
			"dgdState", dgd.Status.State)

		dgdr.Status.State = StateDeploying

		r.Recorder.Event(dgdr, corev1.EventTypeWarning, EventReasonDeploymentDegraded,
			fmt.Sprintf(MessageDeploymentDegraded, dgd.Name, dgd.Status.State))

		meta.SetStatusCondition(&dgdr.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeDeploymentReady,
			Status:  metav1.ConditionFalse,
			Reason:  EventReasonDeploymentDegraded,
			Message: fmt.Sprintf("Deployment degraded to %s", dgd.Status.State),
		})
	}

	return ctrl.Result{}, r.Status().Update(ctx, dgdr)
}

// handleDeployingState handles DGD creation and monitors deployment
func (r *DynamoGraphDeploymentRequestReconciler) handleDeployingState(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("Handling deploying state", "name", dgdr.Name)

	if !dgdr.Spec.AutoApply {
		// Shouldn't be in this state without autoApply
		logger.Info("AutoApply not enabled, transitioning to Ready")
		dgdr.Status.State = StateReady
		return ctrl.Result{}, r.Status().Update(ctx, dgdr)
	}

	// Check if we need to create DGD
	if dgdr.Status.Deployment == nil || !dgdr.Status.Deployment.Created {
		return r.createDGD(ctx, dgdr)
	}

	// DGD was already created, check its status
	dgd := &nvidiacomv1alpha1.DynamoGraphDeployment{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      dgdr.Status.Deployment.Name,
		Namespace: dgdr.Status.Deployment.Namespace,
	}, dgd)

	if apierrors.IsNotFound(err) {
		// DGD was deleted by user
		return r.handleDGDDeleted(ctx, dgdr)
	}

	if err != nil {
		return ctrl.Result{}, err
	}

	// Update deployment status
	dgdr.Status.Deployment.State = dgd.Status.State

	// Check if DGD is Ready
	if dgd.Status.State == "Ready" {
		logger.Info("DGD is Ready, transitioning to Ready state")
		dgdr.Status.State = StateReady

		r.Recorder.Event(dgdr, corev1.EventTypeNormal, EventReasonDeploymentReady,
			fmt.Sprintf(MessageDeploymentReady, dgd.Name))

		meta.SetStatusCondition(&dgdr.Status.Conditions, metav1.Condition{
			Type:    ConditionTypeDeploymentReady,
			Status:  metav1.ConditionTrue,
			Reason:  EventReasonDeploymentReady,
			Message: fmt.Sprintf(MessageDeploymentReady, dgd.Name),
		})
	}

	return ctrl.Result{}, r.Status().Update(ctx, dgdr)
}

// handleDeploymentDeletedState is a terminal state for when auto-created DGD is deleted
func (r *DynamoGraphDeploymentRequestReconciler) handleDeploymentDeletedState(_ context.Context, _ *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	// Terminal state - nothing to do
	// User must delete this DGDR and create a new one to redeploy
	return ctrl.Result{}, nil
}

// handleDGDDeleted handles the case when auto-created DGD is deleted by user
func (r *DynamoGraphDeploymentRequestReconciler) handleDGDDeleted(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("DGD was deleted by user, transitioning to DeploymentDeleted state")

	dgdr.Status.State = StateDeploymentDeleted
	dgdr.Status.Deployment.State = "Deleted"

	r.Recorder.Event(dgdr, corev1.EventTypeWarning, EventReasonDeploymentDeleted,
		fmt.Sprintf(MessageDeploymentDeleted, dgdr.Status.Deployment.Name))

	meta.SetStatusCondition(&dgdr.Status.Conditions, metav1.Condition{
		Type:    ConditionTypeDeploymentReady,
		Status:  metav1.ConditionFalse,
		Reason:  EventReasonDeploymentDeleted,
		Message: "Deployment was deleted by user. Create a new DGDR to redeploy.",
	})

	return ctrl.Result{}, r.Status().Update(ctx, dgdr)
}

// createDGD creates a DynamoGraphDeployment with the generated spec
func (r *DynamoGraphDeploymentRequestReconciler) createDGD(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Extract DGD from RawExtension
	if dgdr.Status.GeneratedDeployment == nil {
		return ctrl.Result{}, fmt.Errorf("generatedDeployment is not set")
	}

	generatedDGD := &nvidiacomv1alpha1.DynamoGraphDeployment{}

	// RawExtension can have either Object (already decoded) or Raw (JSON bytes)
	if dgdr.Status.GeneratedDeployment.Object != nil {
		var ok bool
		generatedDGD, ok = dgdr.Status.GeneratedDeployment.Object.(*nvidiacomv1alpha1.DynamoGraphDeployment)
		if !ok {
			return ctrl.Result{}, fmt.Errorf("generatedDeployment.Object is not a DynamoGraphDeployment")
		}
	} else if dgdr.Status.GeneratedDeployment.Raw != nil {
		if err := yaml.Unmarshal(dgdr.Status.GeneratedDeployment.Raw, generatedDGD); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to unmarshal generated deployment: %w", err)
		}
	} else {
		return ctrl.Result{}, fmt.Errorf("generatedDeployment has neither Object nor Raw set")
	}

	// Determine DGD name and namespace (start with generated DGD's metadata)
	dgdName := generatedDGD.Name
	dgdNamespace := dgdr.Namespace

	if dgdr.Spec.DeploymentOverrides != nil {
		if dgdr.Spec.DeploymentOverrides.Name != "" {
			dgdName = dgdr.Spec.DeploymentOverrides.Name
		}
		if dgdr.Spec.DeploymentOverrides.Namespace != "" {
			dgdNamespace = dgdr.Spec.DeploymentOverrides.Namespace
		}
	}

	// Build labels (start with generated DGD's labels)
	labels := make(map[string]string)
	if generatedDGD.Labels != nil {
		for k, v := range generatedDGD.Labels {
			labels[k] = v
		}
	}
	// Add/override with managed labels
	labels[LabelDGDRName] = dgdr.Name
	labels[LabelManagedBy] = LabelValueDynamoOperator

	// Merge custom labels from overrides
	if dgdr.Spec.DeploymentOverrides != nil && dgdr.Spec.DeploymentOverrides.Labels != nil {
		for k, v := range dgdr.Spec.DeploymentOverrides.Labels {
			labels[k] = v
		}
	}

	// Build annotations (start with generated DGD's annotations)
	annotations := make(map[string]string)
	if generatedDGD.Annotations != nil {
		for k, v := range generatedDGD.Annotations {
			annotations[k] = v
		}
	}
	// Merge custom annotations from overrides
	if dgdr.Spec.DeploymentOverrides != nil && dgdr.Spec.DeploymentOverrides.Annotations != nil {
		for k, v := range dgdr.Spec.DeploymentOverrides.Annotations {
			annotations[k] = v
		}
	}

	// Create DGD from generated deployment
	dgd := &nvidiacomv1alpha1.DynamoGraphDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:        dgdName,
			Namespace:   dgdNamespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: generatedDGD.Spec,
	}

	// Set owner reference if in same namespace (enables cascade delete)
	if dgdNamespace == dgdr.Namespace {
		if err := controllerutil.SetControllerReference(dgdr, dgd, r.Scheme()); err != nil {
			return ctrl.Result{}, err
		}
	}

	logger.Info("Creating DynamoGraphDeployment", "name", dgdName, "namespace", dgdNamespace)

	if err := r.Create(ctx, dgd); err != nil {
		if apierrors.IsAlreadyExists(err) {
			// DGD already exists, just update status
			logger.Info("DGD already exists, updating status")
			dgdr.Status.Deployment = &nvidiacomv1alpha1.DeploymentStatus{
				Name:      dgdName,
				Namespace: dgdNamespace,
				State:     "Pending",
				Created:   true,
			}
			return ctrl.Result{}, r.Status().Update(ctx, dgdr)
		}
		r.Recorder.Event(dgdr, corev1.EventTypeWarning, MessageJobCreationFailed, err.Error())
		return ctrl.Result{}, err
	}

	// Update status
	dgdr.Status.Deployment = &nvidiacomv1alpha1.DeploymentStatus{
		Name:      dgdName,
		Namespace: dgdNamespace,
		State:     "Pending",
		Created:   true,
	}

	r.Recorder.Event(dgdr, corev1.EventTypeNormal, EventReasonDeploymentCreated,
		fmt.Sprintf(MessageDeploymentCreated, dgdName))

	meta.SetStatusCondition(&dgdr.Status.Conditions, metav1.Condition{
		Type:    ConditionTypeDeploymentReady,
		Status:  metav1.ConditionFalse,
		Reason:  EventReasonDeploymentCreated,
		Message: fmt.Sprintf("DGD %s created, waiting for Ready", dgdName),
	})

	logger.Info("DynamoGraphDeployment created successfully", "name", dgdName)

	return ctrl.Result{}, r.Status().Update(ctx, dgdr)
}

// handleFailedState handles DGDR in Failed state
func (r *DynamoGraphDeploymentRequestReconciler) handleFailedState(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("DGDR is in failed state", "name", dgdr.Name)

	// Cleanup profiling resources if any
	if err := r.cleanupProfilingResources(ctx, dgdr); err != nil {
		logger.Error(err, "Failed to cleanup profiling resources")
	}

	// Could implement retry logic here if desired
	return ctrl.Result{}, nil
}

// getProfilingJobName returns the job name for a DGDR based on profiling mode
func getProfilingJobName(dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) string {
	var jobNamePrefix string
	if dgdr.Spec.Online {
		jobNamePrefix = JobNamePrefixOnline
	} else {
		jobNamePrefix = JobNamePrefixAIC
	}
	return fmt.Sprintf("%s%s", jobNamePrefix, dgdr.Name)
}

// getOutputConfigMapName returns the ConfigMap name for profiling output
func getOutputConfigMapName(dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) string {
	return fmt.Sprintf("%s%s", ConfigMapOutputPrefix, dgdr.Name)
}

// validateSpec validates the DGDR spec
func (r *DynamoGraphDeploymentRequestReconciler) validateSpec(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) error {
	if dgdr.Spec.ModelName == "" {
		return errors.New(ValidationErrorModelNameRequired)
	}

	if dgdr.Spec.SLA.ITL <= 0 {
		return errors.New(ValidationErrorITLPositive)
	}

	if dgdr.Spec.SLA.TTFT <= 0 {
		return errors.New(ValidationErrorTTFTPositive)
	}

	// Validate backend
	validBackends := map[string]bool{
		BackendVLLM:   true,
		BackendSGLang: true,
		BackendTRTLLM: true,
	}
	if dgdr.Spec.Backend != "" && !validBackends[dgdr.Spec.Backend] {
		return fmt.Errorf(ValidationErrorInvalidBackend, dgdr.Spec.Backend)
	}

	// Validate ConfigMap if provided (only for online profiling)
	if dgdr.Spec.Online && dgdr.Spec.ProfilingConfig != nil && dgdr.Spec.ProfilingConfig.ConfigMapRef != nil {
		cm := &corev1.ConfigMap{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      dgdr.Spec.ProfilingConfig.ConfigMapRef.Name,
			Namespace: dgdr.Namespace,
		}, cm)

		if err != nil {
			if apierrors.IsNotFound(err) {
				return fmt.Errorf(MessageConfigMapNotFound,
					dgdr.Spec.ProfilingConfig.ConfigMapRef.Name, dgdr.Namespace)
			}
			return err
		}

		// Validate key exists
		key := dgdr.Spec.ProfilingConfig.ConfigMapRef.Key
		if key == "" {
			key = "disagg.yaml"
		}

		if _, exists := cm.Data[key]; !exists {
			return fmt.Errorf(MessageConfigMapKeyNotFound, key, cm.Name)
		}
	}

	return nil
}

// createProfilingJob creates a Kubernetes Job for profiling using SyncResource
func (r *DynamoGraphDeploymentRequestReconciler) createProfilingJob(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) error {
	logger := log.FromContext(ctx)

	// Ensure profiling job RBAC exists in cluster-wide mode
	if r.Config.RestrictedNamespace == "" {
		if err := r.RBACMgr.EnsureServiceAccountWithRBAC(
			ctx,
			dgdr.Namespace,
			ServiceAccountProfilingJob,
			r.Config.RBAC.DGDRProfilingClusterRoleName,
		); err != nil {
			logger.Error(err, "Failed to ensure profiling job RBAC")
			return fmt.Errorf("failed to ensure profiling job RBAC: %w", err)
		}
	}

	// Determine image and label based on profiling mode
	var imageName, labelValue string
	if dgdr.Spec.Online {
		imageName = r.OnlineProfilingImage
		labelValue = LabelValueDynamoProfiler
	} else {
		imageName = r.AICProfilingImage
		labelValue = LabelValueAICProfiler
	}

	if imageName == "" {
		mode := "online"
		if !dgdr.Spec.Online {
			mode = "AIC"
		}
		return fmt.Errorf("%s profiling image not configured", mode)
	}

	// Use SyncResource to create/update the job
	modified, job, err := commonController.SyncResource(ctx, r, dgdr, func(ctx context.Context) (*batchv1.Job, bool, error) {
		jobName := getProfilingJobName(dgdr)

		// TODO: Build args for actual profiler command
		// args := []string{
		// 	ArgModel, dgdr.Spec.ModelName,
		// 	ArgBackend, dgdr.Spec.Backend,
		// 	ArgTTFT, fmt.Sprintf("%d", dgdr.Spec.SLA.TTFT),
		// 	ArgITL, fmt.Sprintf("%d", dgdr.Spec.SLA.ITL),
		// }
		// if dgdr.Spec.Online && dgdr.Spec.ProfilingConfig != nil && dgdr.Spec.ProfilingConfig.ConfigMapRef != nil {
		// 	args = append(args, ArgConfig, fmt.Sprintf("%s/%s", ProfilingConfigPath, ProfilingConfigFile))
		// }

		// Build container with volume mounts
		volumeMounts := []corev1.VolumeMount{
			{
				Name:      VolumeNameProfilingOutput,
				MountPath: ProfilingOutputPath,
			},
		}

		// Add ConfigMap volume mount if provided (online only)
		if dgdr.Spec.Online && dgdr.Spec.ProfilingConfig != nil && dgdr.Spec.ProfilingConfig.ConfigMapRef != nil {
			volumeMounts = append(volumeMounts, corev1.VolumeMount{
				Name:      VolumeNameProfilingConfig,
				MountPath: ProfilingConfigPath,
				ReadOnly:  true,
			})
		}

		profilerContainer := corev1.Container{
			Name:         ContainerNameProfiler,
			Image:        imageName,
			VolumeMounts: volumeMounts,
			Command:      []string{"/bin/sh", "-c"},
			// For now, write a dummy DGD to the output file as a placeholder
			// In production, this should be replaced by actual profiler logic
			Args: []string{fmt.Sprintf(`
cat > %s/%s <<'EOF'
apiVersion: nvidia.com/v1alpha1
kind: DynamoGraphDeployment
metadata:
  name: sglang-agg
spec:
  services:
    Frontend:
      dynamoNamespace: sglang-agg
      componentType: frontend
      replicas: 1
      extraPodSpec:
        mainContainer:
          image: my-registry/sglang-runtime:my-tag
    decode:
      envFromSecret: hf-token-secret
      dynamoNamespace: sglang-agg
      componentType: worker
      replicas: 1
      resources:
        limits:
          gpu: "1"
      extraPodSpec:
        mainContainer:
          image: my-registry/sglang-runtime:my-tag
          workingDir: /workspace/components/backends/sglang
          command:
          - python3
          - -m
          - dynamo.sglang
          args:
            - --model-path
            - Qwen/Qwen3-0.6B
            - --served-model-name
            - Qwen/Qwen3-0.6B
            - --page-size
            - "16"
            - --tp
            - "1"
            - --trust-remote-code
            - --skip-tokenizer-init
EOF
`, ProfilingOutputPath, ProfilingOutputFile)},
		}

		// Build sidecar container that copies output to ConfigMap
		outputConfigMapName := getOutputConfigMapName(dgdr)
		sidecarContainer := corev1.Container{
			Name:    ContainerNameOutputCopier,
			Image:   SidecarImage,
			Command: []string{"/bin/sh", "-c"},
			Args: []string{fmt.Sprintf(`
				set -e  # Exit on any error
				set -o pipefail  # Exit on pipe failures
				
				echo "Waiting for profiling output..."
				
				# Wait for k8s_deploy.yaml to be created
				while [ ! -f %s/%s ]; do 
					sleep 2
				done
				
				echo "Output file found, processing and creating ConfigMap..."
				
				# Get DGDR UID for ownerReference
				DGDR_UID=$(kubectl get dgdr %s -n %s -o jsonpath='{.metadata.uid}')
				DGDR_API_VERSION=$(kubectl get dgdr %s -n %s -o jsonpath='{.apiVersion}')
				
				# Extract spec from k8s_deploy.yaml and create full DGD with DGDR name
				SPEC=$(kubectl create -f %s/%s --dry-run=client -o json | jq '.spec')
				
				# Create full DGD with DGDR name and extracted spec
				cat > /tmp/dgd.yaml <<EOF
apiVersion: nvidia.com/v1alpha1
kind: DynamoGraphDeployment
metadata:
  name: %s
spec: 
EOF
				echo "$SPEC" | jq -r 'to_entries | .[] | "  \(.key): \(.value | tojson)"' >> /tmp/dgd.yaml
				
				# Create ConfigMap with the full DGD
				kubectl create configmap %s \
					--namespace=%s \
					--from-file=%s=/tmp/dgd.yaml \
					--dry-run=client -o json | \
				jq '.metadata.ownerReferences = [{
					"apiVersion": "'$DGDR_API_VERSION'",
					"kind": "DynamoGraphDeploymentRequest",
					"name": "%s",
					"uid": "'$DGDR_UID'",
					"controller": true,
					"blockOwnerDeletion": true
				}]' | \
				kubectl apply -f -
				
				echo "Successfully saved DGD to ConfigMap %s with ownerReference"
			`,
				ProfilingOutputPath, ProfilingOutputFile,
				dgdr.Name, dgdr.Namespace,
				dgdr.Name, dgdr.Namespace,
				ProfilingOutputPath, ProfilingOutputFile,
				dgdr.Name,
				outputConfigMapName, dgdr.Namespace,
				ProfilingOutputFile,
				dgdr.Name,
				outputConfigMapName,
			)},
			VolumeMounts: []corev1.VolumeMount{{
				Name:      VolumeNameProfilingOutput,
				MountPath: ProfilingOutputPath,
				ReadOnly:  true,
			}},
		}

		// Build volumes - always include output emptyDir
		volumes := []corev1.Volume{{
			Name: VolumeNameProfilingOutput,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}}

		// Add ConfigMap volume if provided (online only)
		if dgdr.Spec.Online && dgdr.Spec.ProfilingConfig != nil && dgdr.Spec.ProfilingConfig.ConfigMapRef != nil {
			key := dgdr.Spec.ProfilingConfig.ConfigMapRef.Key
			if key == "" {
				key = ProfilingConfigFile
			}

			volumes = append(volumes, corev1.Volume{
				Name: VolumeNameProfilingConfig,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: dgdr.Spec.ProfilingConfig.ConfigMapRef.Name,
						},
						Items: []corev1.KeyToPath{{
							Key:  key,
							Path: ProfilingConfigFile,
						}},
					},
				},
			})
		}

		// Limit retries to prevent infinite loop
		backoffLimit := int32(3)

		job := &batchv1.Job{
			ObjectMeta: metav1.ObjectMeta{
				Name:      jobName,
				Namespace: dgdr.Namespace,
				Labels: map[string]string{
					LabelApp:       labelValue,
					LabelDGDR:      dgdr.Name,
					LabelManagedBy: LabelValueDynamoOperator,
				},
			},
			Spec: batchv1.JobSpec{
				BackoffLimit: &backoffLimit,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						ServiceAccountName: ServiceAccountProfilingJob,
						RestartPolicy:      corev1.RestartPolicyNever,
						Containers:         []corev1.Container{profilerContainer, sidecarContainer},
						Volumes:            volumes,
					},
				},
			},
		}

		return job, false, nil
	})

	if err != nil {
		return err
	}

	if modified {
		if dgdr.Spec.Online {
			logger.Info("Online profiling job created/updated", "job", job.Name)
		} else {
			logger.Info("AIC profiling job created/updated", "job", job.Name)
		}
	}

	return nil
}

// checkProfilingJobStatus checks if the profiling job has completed
func (r *DynamoGraphDeploymentRequestReconciler) checkProfilingJobStatus(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) (bool, error) {
	logger := log.FromContext(ctx)
	jobName := getProfilingJobName(dgdr)

	job := &batchv1.Job{}
	if err := r.Get(ctx, types.NamespacedName{Name: jobName, Namespace: dgdr.Namespace}, job); err != nil {
		return false, err
	}

	// Check job conditions
	for _, condition := range job.Status.Conditions {
		if condition.Type == batchv1.JobComplete && condition.Status == corev1.ConditionTrue {
			logger.Info("Profiling job completed", "job", jobName)
			return true, nil
		}
		if condition.Type == batchv1.JobFailed && condition.Status == corev1.ConditionTrue {
			return false, fmt.Errorf("profiling job failed: %s", condition.Message)
		}
	}

	return false, nil
}

// generateDGDSpec generates DGD spec from profiling results (online or AIC)
func (r *DynamoGraphDeploymentRequestReconciler) generateDGDSpec(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) error {
	logger := log.FromContext(ctx)
	logger.Info("Generating DGD spec from profiling results", "name", dgdr.Name, "online", dgdr.Spec.Online)

	// Read the generated spec from ConfigMap (created by sidecar)
	outputConfigMapName := getOutputConfigMapName(dgdr)
	cm := &corev1.ConfigMap{}
	err := r.Get(ctx, types.NamespacedName{
		Name:      outputConfigMapName,
		Namespace: dgdr.Namespace,
	}, cm)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("output ConfigMap %s not found - profiling may not have completed yet", outputConfigMapName)
		}
		return fmt.Errorf("failed to get output ConfigMap: %w", err)
	}

	// Get YAML content from ConfigMap
	yamlContent, exists := cm.Data[ProfilingOutputFile]
	if !exists {
		return fmt.Errorf("key %s not found in ConfigMap %s", ProfilingOutputFile, outputConfigMapName)
	}

	logger.Info("Found profiling output in ConfigMap", "configMap", outputConfigMapName, "size", len(yamlContent))

	// Parse YAML into full DynamoGraphDeployment object first to validate and get name
	dgd := &nvidiacomv1alpha1.DynamoGraphDeployment{}
	if err := yaml.Unmarshal([]byte(yamlContent), dgd); err != nil {
		return fmt.Errorf("failed to parse k8s_deploy.yaml: %w", err)
	}

	logger.Info("Parsed DGD from ConfigMap", "dgdName", dgd.Name)

	// Store as RawExtension (need to marshal to JSON as RawExtension expects JSON)
	// This preserves all fields including metadata
	dgdr.Status.GeneratedDeployment = &runtime.RawExtension{
		Object: dgd,
	}

	// Set profiling results reference
	dgdr.Status.ProfilingResults = fmt.Sprintf("configmap/%s", outputConfigMapName)

	logger.Info("Successfully generated DGD from profiling output", "dgdName", dgd.Name)

	return r.Status().Update(ctx, dgdr)
}

// cleanupProfilingResources cleans up profiling resources
func (r *DynamoGraphDeploymentRequestReconciler) cleanupProfilingResources(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest) error {
	logger := log.FromContext(ctx)
	logger.Info("Cleaning up profiling resources", "name", dgdr.Name)

	// Note: All profiling resources are cleaned up automatically via ownerReference (cascade delete):
	// - Profiling Job: ownerReference set by SyncResource
	// - Output ConfigMap: ownerReference set by sidecar container
	// - Auto-created DGD: ownerReference set by controllerutil.SetControllerReference
	//
	// No manual cleanup needed!
	return nil
}

// updateStateAndRequeue updates the DGDR state and requeues
func (r *DynamoGraphDeploymentRequestReconciler) updateStateAndRequeue(ctx context.Context, dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest, state, message string) (ctrl.Result, error) {
	dgdr.Status.State = state
	if err := r.Status().Update(ctx, dgdr); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{Requeue: true}, nil
}

// updateStateWithCondition updates state and adds/updates a condition
func (r *DynamoGraphDeploymentRequestReconciler) updateStateWithCondition(
	ctx context.Context,
	dgdr *nvidiacomv1alpha1.DynamoGraphDeploymentRequest,
	state string,
	conditionType string,
	status metav1.ConditionStatus,
	reason string,
	message string,
) (ctrl.Result, error) {
	dgdr.Status.State = state

	condition := metav1.Condition{
		Type:               conditionType,
		Status:             status,
		ObservedGeneration: dgdr.Generation,
		LastTransitionTime: metav1.Now(),
		Reason:             reason,
		Message:            message,
	}

	dgdr.AddStatusCondition(condition)

	if err := r.Status().Update(ctx, dgdr); err != nil {
		return ctrl.Result{}, err
	}

	return ctrl.Result{Requeue: true}, nil
}

// SetupWithManager sets up the controller with the Manager
func (r *DynamoGraphDeploymentRequestReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&nvidiacomv1alpha1.DynamoGraphDeploymentRequest{}).
		Owns(&batchv1.Job{}, builder.WithPredicates(predicate.Funcs{
			// ignore creation cause we don't want to be called again after we create the job
			CreateFunc:  func(ce event.CreateEvent) bool { return false },
			DeleteFunc:  func(de event.DeleteEvent) bool { return true },
			UpdateFunc:  func(de event.UpdateEvent) bool { return true },
			GenericFunc: func(ge event.GenericEvent) bool { return true },
		})). // Watch Jobs created by this controller
		Owns(&nvidiacomv1alpha1.DynamoGraphDeployment{}, builder.WithPredicates(predicate.Funcs{
			// ignore creation cause we don't want to be called again after we create the DGD
			CreateFunc:  func(ce event.CreateEvent) bool { return false },
			DeleteFunc:  func(de event.DeleteEvent) bool { return true },
			UpdateFunc:  func(de event.UpdateEvent) bool { return true },
			GenericFunc: func(ge event.GenericEvent) bool { return true },
		})). // Watch DGDs created by this controller (via ownerReference)
		Complete(r)
}

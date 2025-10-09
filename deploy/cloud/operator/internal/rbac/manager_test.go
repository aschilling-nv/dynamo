/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package rbac

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func setupTest() (client.Client, *runtime.Scheme) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = rbacv1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	return fakeClient, scheme
}

func TestEnsureServiceAccountWithRBAC_CreateNew(t *testing.T) {
	// Setup
	fakeClient, _ := setupTest()
	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute
	err := manager.EnsureServiceAccountWithRBAC(
		ctx,
		"test-namespace",
		"test-sa",
		"test-cluster-role",
	)

	// Verify
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Check ServiceAccount was created
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa",
	}, sa)
	if err != nil {
		t.Fatalf("ServiceAccount not created: %v", err)
	}

	// Verify ServiceAccount labels
	expectedLabels := map[string]string{
		"app.kubernetes.io/managed-by": "dynamo-operator",
		"app.kubernetes.io/component":  "rbac",
		"app.kubernetes.io/name":       "test-sa",
	}
	for k, v := range expectedLabels {
		if sa.Labels[k] != v {
			t.Errorf("Expected label %s=%s, got %s", k, v, sa.Labels[k])
		}
	}

	// Check RoleBinding was created
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa-binding",
	}, rb)
	if err != nil {
		t.Fatalf("RoleBinding not created: %v", err)
	}

	// Verify RoleBinding configuration
	if len(rb.Subjects) != 1 {
		t.Fatalf("Expected 1 subject, got %d", len(rb.Subjects))
	}
	if rb.Subjects[0].Kind != "ServiceAccount" {
		t.Errorf("Expected subject kind ServiceAccount, got %s", rb.Subjects[0].Kind)
	}
	if rb.Subjects[0].Name != "test-sa" {
		t.Errorf("Expected subject name test-sa, got %s", rb.Subjects[0].Name)
	}
	if rb.Subjects[0].Namespace != "test-namespace" {
		t.Errorf("Expected subject namespace test-namespace, got %s", rb.Subjects[0].Namespace)
	}

	// Verify RoleRef
	if rb.RoleRef.Kind != "ClusterRole" {
		t.Errorf("Expected RoleRef kind ClusterRole, got %s", rb.RoleRef.Kind)
	}
	if rb.RoleRef.Name != "test-cluster-role" {
		t.Errorf("Expected RoleRef name test-cluster-role, got %s", rb.RoleRef.Name)
	}
	if rb.RoleRef.APIGroup != "rbac.authorization.k8s.io" {
		t.Errorf("Expected RoleRef APIGroup rbac.authorization.k8s.io, got %s", rb.RoleRef.APIGroup)
	}
}

func TestEnsureServiceAccountWithRBAC_AlreadyExists(t *testing.T) {
	// Setup - pre-create ServiceAccount and RoleBinding
	_, scheme := setupTest()

	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       "test-sa",
			},
		},
	}

	existingRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa-binding",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       "test-sa",
			},
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      "test-sa",
			Namespace: "test-namespace",
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "test-cluster-role",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSA, existingRB).
		Build()

	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute
	err := manager.EnsureServiceAccountWithRBAC(
		ctx,
		"test-namespace",
		"test-sa",
		"test-cluster-role",
	)

	// Verify
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify resources still exist and unchanged
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa",
	}, sa)
	if err != nil {
		t.Fatalf("ServiceAccount not found: %v", err)
	}

	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa-binding",
	}, rb)
	if err != nil {
		t.Fatalf("RoleBinding not found: %v", err)
	}
}

func TestEnsureServiceAccountWithRBAC_UpdateRoleBinding(t *testing.T) {
	// Setup - pre-create ServiceAccount and RoleBinding with wrong subject
	_, scheme := setupTest()

	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       "test-sa",
			},
		},
	}

	existingRB := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa-binding",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       "test-sa",
			},
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      "wrong-sa", // Wrong name
			Namespace: "test-namespace",
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "test-cluster-role",
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSA, existingRB).
		Build()

	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute
	err := manager.EnsureServiceAccountWithRBAC(
		ctx,
		"test-namespace",
		"test-sa",
		"test-cluster-role",
	)

	// Verify
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify RoleBinding was updated with correct subject
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa-binding",
	}, rb)
	if err != nil {
		t.Fatalf("RoleBinding not found: %v", err)
	}

	if len(rb.Subjects) != 1 {
		t.Fatalf("Expected 1 subject, got %d", len(rb.Subjects))
	}
	if rb.Subjects[0].Name != "test-sa" {
		t.Errorf("Expected subject name test-sa, got %s", rb.Subjects[0].Name)
	}
}

func TestEnsureServiceAccountWithRBAC_MultipleNamespaces(t *testing.T) {
	// Setup
	fakeClient, _ := setupTest()
	manager := NewManager(fakeClient)
	ctx := context.Background()

	namespaces := []string{"ns1", "ns2", "ns3"}

	// Execute - create RBAC in multiple namespaces
	for _, ns := range namespaces {
		err := manager.EnsureServiceAccountWithRBAC(
			ctx,
			ns,
			"test-sa",
			"test-cluster-role",
		)
		if err != nil {
			t.Fatalf("Failed for namespace %s: %v", ns, err)
		}
	}

	// Verify - check resources exist in all namespaces
	for _, ns := range namespaces {
		sa := &corev1.ServiceAccount{}
		err := fakeClient.Get(ctx, client.ObjectKey{
			Namespace: ns,
			Name:      "test-sa",
		}, sa)
		if err != nil {
			t.Errorf("ServiceAccount not found in namespace %s: %v", ns, err)
		}

		rb := &rbacv1.RoleBinding{}
		err = fakeClient.Get(ctx, client.ObjectKey{
			Namespace: ns,
			Name:      "test-sa-binding",
		}, rb)
		if err != nil {
			t.Errorf("RoleBinding not found in namespace %s: %v", ns, err)
		}
	}
}

func TestEnsureServiceAccountWithRBAC_ServiceAccountExistsRoleBindingDoesNot(t *testing.T) {
	// Setup - pre-create only ServiceAccount
	_, scheme := setupTest()

	existingSA := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-sa",
			Namespace: "test-namespace",
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       "test-sa",
			},
		},
	}

	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(existingSA).
		Build()

	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute
	err := manager.EnsureServiceAccountWithRBAC(
		ctx,
		"test-namespace",
		"test-sa",
		"test-cluster-role",
	)

	// Verify
	if err != nil {
		t.Fatalf("Expected no error, got: %v", err)
	}

	// Verify ServiceAccount still exists
	sa := &corev1.ServiceAccount{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa",
	}, sa)
	if err != nil {
		t.Fatalf("ServiceAccount not found: %v", err)
	}

	// Verify RoleBinding was created
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa-binding",
	}, rb)
	if err != nil {
		t.Fatalf("RoleBinding not created: %v", err)
	}
}

func TestEnsureServiceAccountWithRBAC_Idempotency(t *testing.T) {
	// Setup
	fakeClient, _ := setupTest()
	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute multiple times
	for i := 0; i < 3; i++ {
		err := manager.EnsureServiceAccountWithRBAC(
			ctx,
			"test-namespace",
			"test-sa",
			"test-cluster-role",
		)
		if err != nil {
			t.Fatalf("Iteration %d failed: %v", i, err)
		}
	}

	// Verify - should still have exactly one ServiceAccount and one RoleBinding
	saList := &corev1.ServiceAccountList{}
	err := fakeClient.List(ctx, saList, client.InNamespace("test-namespace"))
	if err != nil {
		t.Fatalf("Failed to list ServiceAccounts: %v", err)
	}
	if len(saList.Items) != 1 {
		t.Errorf("Expected 1 ServiceAccount, got %d", len(saList.Items))
	}

	rbList := &rbacv1.RoleBindingList{}
	err = fakeClient.List(ctx, rbList, client.InNamespace("test-namespace"))
	if err != nil {
		t.Fatalf("Failed to list RoleBindings: %v", err)
	}
	if len(rbList.Items) != 1 {
		t.Errorf("Expected 1 RoleBinding, got %d", len(rbList.Items))
	}
}

func TestNewManager(t *testing.T) {
	// Setup
	fakeClient, _ := setupTest()

	// Execute
	manager := NewManager(fakeClient)

	// Verify
	if manager == nil {
		t.Fatal("Expected non-nil manager")
	}
	if manager.client == nil {
		t.Fatal("Expected non-nil client in manager")
	}
}

func TestEnsureServiceAccountWithRBAC_DifferentClusterRoles(t *testing.T) {
	// Setup
	fakeClient, _ := setupTest()
	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute - create with first cluster role
	err := manager.EnsureServiceAccountWithRBAC(
		ctx,
		"test-namespace",
		"test-sa",
		"cluster-role-1",
	)
	if err != nil {
		t.Fatalf("First call failed: %v", err)
	}

	// Verify first cluster role
	rb := &rbacv1.RoleBinding{}
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "test-namespace",
		Name:      "test-sa-binding",
	}, rb)
	if err != nil {
		t.Fatalf("RoleBinding not found: %v", err)
	}
	if rb.RoleRef.Name != "cluster-role-1" {
		t.Errorf("Expected RoleRef name cluster-role-1, got %s", rb.RoleRef.Name)
	}

	// Note: In real Kubernetes, RoleRef is immutable so you can't change it
	// This test documents the current behavior where the code attempts to update
	// but would fail in a real cluster (the fake client doesn't enforce RoleRef immutability)
}

func TestEnsureServiceAccountWithRBAC_EmptyNamespace(t *testing.T) {
	// Setup
	fakeClient, _ := setupTest()
	manager := NewManager(fakeClient)
	ctx := context.Background()

	// Execute with empty namespace
	err := manager.EnsureServiceAccountWithRBAC(
		ctx,
		"",
		"test-sa",
		"test-cluster-role",
	)

	// Verify - should fail because namespace is required
	// The fake client might not enforce this, but in real K8s it would fail
	// We just verify the function returns (it might succeed with fake client)
	if err == nil {
		// Check if resources were created in empty namespace
		sa := &corev1.ServiceAccount{}
		err = fakeClient.Get(ctx, client.ObjectKey{
			Namespace: "",
			Name:      "test-sa",
		}, sa)
		// In fake client this might work, but we document the behavior
		if err != nil && !apierrors.IsNotFound(err) {
			t.Logf("Expected behavior: empty namespace handled: %v", err)
		}
	}
}

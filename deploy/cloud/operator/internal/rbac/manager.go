/*
 * SPDX-FileCopyrightText: Copyright (c) 2025 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
 * SPDX-License-Identifier: Apache-2.0
 */

package rbac

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// Manager handles dynamic RBAC creation for cluster-wide operator installations.
type Manager struct {
	client client.Client
}

// NewManager creates a new RBAC manager.
func NewManager(client client.Client) *Manager {
	return &Manager{client: client}
}

// EnsureServiceAccountWithRBAC creates or updates a ServiceAccount and RoleBinding
// in the target namespace. This should ONLY be called in cluster-wide mode.
//
// In cluster-wide mode, the operator dynamically creates:
//   - ServiceAccount in the target namespace
//   - RoleBinding in the target namespace that binds the SA to a ClusterRole
//
// The ClusterRole must already exist (created by Helm).
//
// Parameters:
//   - ctx: context
//   - targetNamespace: namespace to create RBAC resources in
//   - serviceAccountName: name of the ServiceAccount to create
//   - clusterRoleName: name of the ClusterRole to bind to (must exist)
func (m *Manager) EnsureServiceAccountWithRBAC(
	ctx context.Context,
	targetNamespace string,
	serviceAccountName string,
	clusterRoleName string,
) error {
	logger := log.FromContext(ctx)

	// Create/update ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName,
			Namespace: targetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       serviceAccountName,
			},
		},
	}

	if err := m.client.Get(ctx, client.ObjectKeyFromObject(sa), sa); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get service account: %w", err)
		}
		// ServiceAccount doesn't exist, create it
		if err := m.client.Create(ctx, sa); err != nil {
			return fmt.Errorf("failed to create service account: %w", err)
		}
		logger.V(1).Info("ServiceAccount created",
			"serviceAccount", serviceAccountName,
			"namespace", targetNamespace)
	} else {
		logger.V(1).Info("ServiceAccount already exists",
			"serviceAccount", serviceAccountName,
			"namespace", targetNamespace)
	}

	// Create/update RoleBinding
	roleBindingName := fmt.Sprintf("%s-binding", serviceAccountName)
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleBindingName,
			Namespace: targetNamespace,
			Labels: map[string]string{
				"app.kubernetes.io/managed-by": "dynamo-operator",
				"app.kubernetes.io/component":  "rbac",
				"app.kubernetes.io/name":       serviceAccountName,
			},
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      serviceAccountName,
			Namespace: targetNamespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     clusterRoleName,
		},
	}

	existingRB := &rbacv1.RoleBinding{}
	if err := m.client.Get(ctx, client.ObjectKeyFromObject(rb), existingRB); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("failed to get role binding: %w", err)
		}
		// RoleBinding doesn't exist, create it
		if err := m.client.Create(ctx, rb); err != nil {
			return fmt.Errorf("failed to create role binding: %w", err)
		}
		logger.V(1).Info("RoleBinding created",
			"roleBinding", roleBindingName,
			"clusterRole", clusterRoleName,
			"namespace", targetNamespace)
	} else {
		// RoleBinding exists, update if needed
		if existingRB.RoleRef.Name != clusterRoleName ||
			len(existingRB.Subjects) != 1 ||
			existingRB.Subjects[0].Name != serviceAccountName {
			existingRB.Subjects = rb.Subjects
			// Note: RoleRef is immutable, so if it changes, we'd need to delete and recreate
			if err := m.client.Update(ctx, existingRB); err != nil {
				return fmt.Errorf("failed to update role binding: %w", err)
			}
			logger.V(1).Info("RoleBinding updated",
				"roleBinding", roleBindingName,
				"clusterRole", clusterRoleName,
				"namespace", targetNamespace)
		} else {
			logger.V(1).Info("RoleBinding already up-to-date",
				"roleBinding", roleBindingName,
				"clusterRole", clusterRoleName,
				"namespace", targetNamespace)
		}
	}

	return nil
}

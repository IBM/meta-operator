//
// Copyright 2020 IBM Corporation
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//

package operandbindinfo

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	operatorv1alpha1 "github.com/IBM/operand-deployment-lifecycle-manager/pkg/apis/operator/v1alpha1"
)

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new OperandBindInfo Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileOperandBindInfo{
		client:   mgr.GetClient(),
		recorder: mgr.GetEventRecorderFor("OperandRequest"),
		scheme:   mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	c, err := controller.New("operandbindinfo-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource OperandBindInfo
	err = c.Watch(&source.Kind{Type: &operatorv1alpha1.OperandBindInfo{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource OperandRequest
	err = c.Watch(&source.Kind{Type: &operatorv1alpha1.OperandRequest{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	// TODO(user): Modify this to be the types you create that are owned by the primary resource
	// Watch for changes to secondary resource Pods and requeue the owner OperandBindInfo
	err = c.Watch(&source.Kind{Type: &corev1.Pod{}}, &handler.EnqueueRequestForOwner{
		IsController: true,
		OwnerType:    &operatorv1alpha1.OperandBindInfo{},
	})
	if err != nil {
		return err
	}

	return nil
}

// blank assignment to verify that ReconcileOperandBindInfo implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileOperandBindInfo{}

// ReconcileOperandBindInfo reconciles a OperandBindInfo object
type ReconcileOperandBindInfo struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client   client.Client
	recorder record.EventRecorder
	scheme   *runtime.Scheme
}

// Reconcile reads that state of the cluster for a OperandBindInfo object and makes changes based on the state read
// and what is in the OperandBindInfo.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  This example creates
// a Pod as an example
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileOperandBindInfo) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	klog.V(1).Info("Reconciling OperandBindInfo: ", request)

	// Fetch the OperandBindInfo instance
	bindInfoInstance := &operatorv1alpha1.OperandBindInfo{}
	if err := r.client.Get(context.TODO(), request.NamespacedName, bindInfoInstance); err != nil {
		// Error reading the object - requeue the request.
		return reconcile.Result{}, client.IgnoreNotFound(err)
	}

	// Fetch the OperandRegistry instance
	registryInstance := &operatorv1alpha1.OperandRegistry{}
	if err := r.client.Get(context.TODO(), types.NamespacedName{Name: bindInfoInstance.Spec.Registry, Namespace: request.Namespace}, registryInstance); err != nil {
		if errors.IsNotFound(err) {
			r.recorder.Eventf(bindInfoInstance, corev1.EventTypeWarning, "NotFound", "NotFound OperandRegistry %s from the namespace %s", bindInfoInstance.Spec.Registry, request.Namespace)
		}
		return reconcile.Result{}, err
	}

	// Get the OperandRequest namespace
	for _, requestInstance := range registryInstance.Status.OperatorsStatus[bindInfoInstance.Spec.Operand].ReconcileRequests {
		if request.Namespace == requestInstance.Namespace {
			//skip the namespace of OperandBindInfo
			continue
		}
		requestInstance := &operatorv1alpha1.OperandRequest{}
		if err := r.client.Get(context.TODO(), types.NamespacedName{Name: requestInstance.Name, Namespace: requestInstance.Namespace}, requestInstance); err != nil {
			// Error reading the object - requeue the request.
			return reconcile.Result{}, client.IgnoreNotFound(err)
		}
		klog.V(2).Infof("Copy secret and/or configmap to namespace %s", requestInstance.Namespace)
		for _, secretcm := range bindInfoInstance.Spec.Bindings {
			if secretcm.Scope == operatorv1alpha1.ScopePublic {
				if secretcm.Secret != "" {
					secret := &corev1.Secret{}
					if err := r.client.Get(context.TODO(), types.NamespacedName{Name: secretcm.Secret, Namespace: request.Namespace}, secret); err != nil {
						if errors.IsNotFound(err) {
							r.recorder.Eventf(bindInfoInstance, corev1.EventTypeWarning, "NotFound", "NotFound Secret %s from the namespace %s", secretcm.Secret, request.Namespace)
						} else {
							klog.Errorf("NotFound Secret %s from the namespace %s", secretcm.Secret, request.Namespace)
							return reconcile.Result{}, err
						}
					}
					secretCopy := &corev1.Secret{
						ObjectMeta: metav1.ObjectMeta{
							Name:      secretcm.Secret,
							Namespace: requestInstance.Namespace,
						},
						Type: secret.Type,
						Data: secret.Data,
					}
					if err := controllerutil.SetControllerReference(requestInstance, secretCopy, r.scheme); err != nil {
						return reconcile.Result{}, err
					}
					if err := r.client.Create(context.TODO(), secretCopy); err != nil {
						if errors.IsAlreadyExists(err) {
							if err := r.client.Update(context.TODO(), secretCopy); err != nil {
								return reconcile.Result{}, err
							}
						}
						return reconcile.Result{}, err
					}
				}
				if secretcm.Configmap != "" {
					cm := &corev1.ConfigMap{}
					if err := r.client.Get(context.TODO(), types.NamespacedName{Name: secretcm.Secret, Namespace: request.Namespace}, cm); err != nil {
						if errors.IsNotFound(err) {
							r.recorder.Eventf(bindInfoInstance, corev1.EventTypeWarning, "NotFound", "NotFound Configmap %s from the namespace %s", secretcm.Configmap, request.Namespace)
						} else {
							klog.Errorf("NotFound Configmap %s from the namespace %s", secretcm.Configmap, request.Namespace)
							return reconcile.Result{}, err
						}
					}
					cmCopy := &corev1.ConfigMap{
						ObjectMeta: metav1.ObjectMeta{
							Name:      secretcm.Configmap,
							Namespace: requestInstance.Namespace,
						},
						Data: cm.Data,
					}
					if err := controllerutil.SetControllerReference(requestInstance, cmCopy, r.scheme); err != nil {
						return reconcile.Result{}, err
					}
					if err := r.client.Create(context.TODO(), cmCopy); err != nil {
						if errors.IsAlreadyExists(err) {
							if err := r.client.Update(context.TODO(), cmCopy); err != nil {
								return reconcile.Result{}, err
							}
						}
						return reconcile.Result{}, err
					}
				}
			}
		}
	}
	return reconcile.Result{}, nil
}

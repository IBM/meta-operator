//
// Copyright 2021 IBM Corporation
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

package operandconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	utilerrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	operatorv1alpha1 "github.com/IBM/operand-deployment-lifecycle-manager/api/v1alpha1"
	"github.com/IBM/operand-deployment-lifecycle-manager/controllers/constant"
	deploy "github.com/IBM/operand-deployment-lifecycle-manager/controllers/operator"
	"github.com/IBM/operand-deployment-lifecycle-manager/controllers/util"
)

// Reconciler reconciles a OperandConfig object
type Reconciler struct {
	*deploy.ODLMOperator
}

// Reconcile reads that state of the cluster for a OperandConfig object and makes changes based on the state read
// and what is in the OperandConfig.Spec
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (_ ctrl.Result, reconcileErr error) {
	// Fetch the OperandConfig instance
	instance := &operatorv1alpha1.OperandConfig{}
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	klog.V(2).Infof("Reconciling OperandConfig: %s", req.NamespacedName)

	originalInstance := instance.DeepCopy()

	// Always attempt to patch the status after each reconciliation.
	defer func() {
		if reflect.DeepEqual(originalInstance.Status, instance.Status) {
			return
		}
		if err := r.Client.Status().Patch(ctx, instance, client.MergeFrom(originalInstance)); err != nil {
			reconcileErr = utilerrors.NewAggregate([]error{reconcileErr, fmt.Errorf("error while patching OperandConfig.Status: %v", err)})
		}
	}()

	// Update status of OperandConfig by checking CRs
	if err := r.updateStatus(ctx, instance); err != nil {
		klog.Errorf("failed to update the status for OperandConfig %s : %v", req.NamespacedName.String(), err)
		return ctrl.Result{}, err
	}

	// Check if all the services are deployed
	if instance.Status.Phase != operatorv1alpha1.ServiceInit &&
		instance.Status.Phase != operatorv1alpha1.ServiceRunning {
		klog.V(2).Info("Waiting for all the services being deployed ...")
		return ctrl.Result{RequeueAfter: constant.DefaultRequeueDuration}, nil
	}

	klog.V(2).Infof("Finished reconciling OperandConfig: %s", req.NamespacedName)
	return ctrl.Result{}, nil
}

func (r *Reconciler) updateStatus(ctx context.Context, instance *operatorv1alpha1.OperandConfig) error {
	// Create an empty ServiceStatus map
	klog.V(3).Info("Initializing OperandConfig status")

	// Set the init status for OperandConfig instance
	if instance.Status.Phase == "" {
		instance.Status.Phase = operatorv1alpha1.ServiceInit
	}

	instance.Status.ServiceStatus = make(map[string]operatorv1alpha1.CrStatus)

	registryInstance, err := r.GetOperandRegistry(ctx, types.NamespacedName{Name: instance.Name, Namespace: instance.Namespace})
	if err != nil {
		return err
	}

	for _, op := range registryInstance.Spec.Operators {

		service := instance.GetService(op.Name)
		if service == nil {
			continue
		}

		// Check if the operator is request in the OperandRegistry
		if !checkRegistryStatus(op.Name, registryInstance) {
			continue
		}

		// Looking for the CSV
		namespace := r.GetOperatorNamespace(op.InstallMode, op.Namespace)
		sub, err := r.GetSubscription(ctx, op.Name, namespace, op.PackageName)

		if apierrors.IsNotFound(err) {
			klog.V(3).Infof("There is no Subscription %s or %s in the namespace %s", op.Name, op.PackageName, namespace)
			continue
		}

		if err != nil {
			return errors.Wrapf(err, "failed to get Subscription %s or %s in the namespace %s", op.Name, op.PackageName, namespace)
		}

		if _, ok := sub.Labels[constant.OpreqLabel]; !ok {
			// Subscription existing and not managed by OperandRequest controller
			klog.V(1).Infof("Subscription %s in the namespace %s isn't created by ODLM", sub.Name, sub.Namespace)
		}

		csv, err := r.GetClusterServiceVersion(ctx, sub)

		if err != nil {
			return errors.Wrapf(err, "failed to get ClusterServiceVersion for the Subscription %s/%s", namespace, sub.Name)
		}

		if csv == nil {
			klog.Warningf("ClusterServiceVersion for the Subscription %s/%s doesn't exist, retry...", namespace, sub.Name)
			continue
		}

		_, ok := instance.Status.ServiceStatus[op.Name]

		if !ok {
			instance.Status.ServiceStatus[op.Name] = operatorv1alpha1.CrStatus{}
		}

		if instance.Status.ServiceStatus[op.Name].CrStatus == nil {
			tmp := instance.Status.ServiceStatus[op.Name]
			tmp.CrStatus = make(map[string]operatorv1alpha1.ServicePhase)
			instance.Status.ServiceStatus[op.Name] = tmp
		}

		almExamples := csv.ObjectMeta.Annotations["alm-examples"]
		if almExamples == "" {
			klog.Warningf("Notfound alm-examples in the ClusterServiceVersion %s/%s", csv.Namespace, csv.Name)
			continue
		}
		// Create a slice for crTemplates
		var crTemplates []interface{}

		// Convert CR template string to slice
		err = json.Unmarshal([]byte(almExamples), &crTemplates)
		if err != nil {
			return errors.Wrapf(err, "failed to convert alm-examples in the Subscription %s/%s to slice", sub.Namespace, sub.Name)
		}

		merr := &util.MultiErr{}

		// Merge OperandConfig and ClusterServiceVersion alm-examples
		for _, crTemplate := range crTemplates {

			// Create an unstruct object for CR and request its value to CR template
			var unstruct unstructured.Unstructured
			unstruct.Object = crTemplate.(map[string]interface{})

			kind := unstruct.Object["kind"].(string)

			existinConfig := false
			for crName := range service.Spec {
				// Compare the name of OperandConfig and CRD name
				if strings.EqualFold(kind, crName) {
					existinConfig = true
				}
			}

			if !existinConfig {
				continue
			}

			name := unstruct.GetName()
			if name == "" {
				continue
			}

			getError := r.Client.Get(ctx, types.NamespacedName{
				Name:      name,
				Namespace: op.Namespace,
			}, &unstruct)

			if getError != nil && !apierrors.IsNotFound(getError) {
				instance.Status.ServiceStatus[op.Name].CrStatus[kind] = operatorv1alpha1.ServiceFailed
			} else if apierrors.IsNotFound(getError) {
			} else {
				instance.Status.ServiceStatus[op.Name].CrStatus[kind] = operatorv1alpha1.ServiceRunning
			}
		}
		if len(merr.Errors) != 0 {
			return merr
		}
	}

	klog.V(2).Info("Updating OperandConfig status")
	instance.UpdateOperandPhase()

	return nil
}

func checkRegistryStatus(opName string, registryInstance *operatorv1alpha1.OperandRegistry) bool {
	status := registryInstance.Status.OperatorsStatus
	for opRegistryName := range status {
		if opName == opRegistryName {
			return true
		}
	}
	return false
}

func (r *Reconciler) getRequestToConfigMapper(ctx context.Context) handler.MapFunc {
	return func(object client.Object) []reconcile.Request {
		opreqInstance := &operatorv1alpha1.OperandRequest{}
		requests := []reconcile.Request{}
		// If the OperandRequest has been deleted, reconcile all the OperandConfig in the cluster
		if err := r.Client.Get(ctx, types.NamespacedName{Name: object.GetName(), Namespace: object.GetNamespace()}, opreqInstance); apierrors.IsNotFound(err) {
			configList := &operatorv1alpha1.OperandConfigList{}
			_ = r.Client.List(ctx, configList)
			for _, config := range configList.Items {
				namespaceName := types.NamespacedName{Name: config.Name, Namespace: config.Namespace}
				req := reconcile.Request{NamespacedName: namespaceName}
				requests = append(requests, req)
			}
			return requests
		}

		// If the OperandRequest exist, reconcile OperandConfigs specific in the OperandRequest instance.
		for _, request := range opreqInstance.Spec.Requests {
			registryKey := opreqInstance.GetRegistryKey(request)
			req := reconcile.Request{NamespacedName: registryKey}
			requests = append(requests, req)
		}
		return requests
	}
}

// SetupWithManager adds OperandConfig controller to the manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	ctx := context.Background()
	return ctrl.NewControllerManagedBy(mgr).
		For(&operatorv1alpha1.OperandConfig{}, builder.WithPredicates(predicate.GenerationChangedPredicate{})).
		Watches(&source.Kind{Type: &operatorv1alpha1.OperandRequest{}}, handler.EnqueueRequestsFromMapFunc(r.getRequestToConfigMapper(ctx)), builder.WithPredicates(predicate.Funcs{
			CreateFunc: func(e event.CreateEvent) bool {
				return true
			},
			DeleteFunc: func(e event.DeleteEvent) bool {
				// Evaluates to false if the object has been confirmed deleted.
				return !e.DeleteStateUnknown
			},
			UpdateFunc: func(e event.UpdateEvent) bool {
				oldObject := e.ObjectOld.(*operatorv1alpha1.OperandRequest)
				newObject := e.ObjectNew.(*operatorv1alpha1.OperandRequest)
				return !reflect.DeepEqual(oldObject.Status, newObject.Status)
			},
		})).Complete(r)
}

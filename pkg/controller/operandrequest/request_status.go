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

package operandrequest

import (
	"context"

	olmv1alpha1 "github.com/operator-framework/operator-lifecycle-manager/pkg/api/apis/operators/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	operatorv1alpha1 "github.com/IBM/operand-deployment-lifecycle-manager/pkg/apis/operator/v1alpha1"
)

// States of common services
const (
	Absent  = "absent"
	Present = "present"
)

func newMember(name string, operatorPhase olmv1alpha1.ClusterServiceVersionPhase, operandPhase operatorv1alpha1.ServicePhase) operatorv1alpha1.MemberStatus {
	return operatorv1alpha1.MemberStatus{
		Name: name,
		Phase: operatorv1alpha1.MemberPhase{
			OperatorPhase: operatorPhase,
			OperandPhase:  operandPhase,
		},
	}
}

func getMember(cr *operatorv1alpha1.OperandRequest, name string) (int, *operatorv1alpha1.MemberStatus) {
	for i, m := range cr.Status.Members {
		if name == m.Name {
			return i, &m
		}
	}
	return -1, nil
}

func setMember(cr *operatorv1alpha1.OperandRequest, newM operatorv1alpha1.MemberStatus) {
	pos, oldM := getMember(cr, newM.Name)
	if oldM != nil {
		if oldM.Phase == newM.Phase {
			return
		}
		cr.Status.Members[pos] = newM
	} else {
		cr.Status.Members = append(cr.Status.Members, newM)
	}
}

func (r *ReconcileOperandRequest) updateMemberStatus(cr *operatorv1alpha1.OperandRequest) error {
	subs, err := r.olmClient.OperatorsV1alpha1().Subscriptions("").List(metav1.ListOptions{
		LabelSelector: "operator.ibm.com/opreq-control",
	})
	if err != nil {
		return err
	}

	for _, s := range subs.Items {
		csvName := s.Status.InstalledCSV
		if csvName != "" {
			csv, err := r.olmClient.OperatorsV1alpha1().ClusterServiceVersions(s.Namespace).Get(csvName, metav1.GetOptions{})
			if err != nil {
				return client.IgnoreNotFound(err)
			}
			member := newMember(s.Name, csv.Status.Phase, "")
			setMember(cr, member)
		}
	}

	phase := operatorv1alpha1.ClusterPhaseRunning
	for _, m := range cr.Status.Members {
		switch m.Phase.OperatorPhase {
		case olmv1alpha1.CSVPhasePending:
			phase = operatorv1alpha1.ClusterPhasePending
		case olmv1alpha1.CSVPhaseFailed, olmv1alpha1.CSVPhaseUnknown:
			phase = operatorv1alpha1.ClusterPhaseFailed
		case olmv1alpha1.CSVPhaseReplacing:
			phase = operatorv1alpha1.ClusterPhaseUpdating
		case olmv1alpha1.CSVPhaseDeleting:
			phase = operatorv1alpha1.ClusterPhaseDeleting
		case olmv1alpha1.CSVPhaseInstalling, olmv1alpha1.CSVPhaseInstallReady:
			phase = operatorv1alpha1.ClusterPhaseCreating
		case olmv1alpha1.CSVPhaseNone:
			phase = operatorv1alpha1.ClusterPhaseNone
		default:

		}
		// TBD, add instance status
	}
	cr.Status.Phase = phase
	if err := r.client.Status().Update(context.TODO(), cr); err != nil {
		return err
	}
	return nil
}

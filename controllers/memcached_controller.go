/*
Copyright 2022 paragor.

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

package controllers

import (
	"context"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	cachev1alpha1 "github.com/paragor/k8s-operators-memcached-operator/api/v1alpha1"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// MemcachedReconciler reconciles a Memcached object
type MemcachedReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

const (
	LabelOwnerKey = "memcached.paragor.ru/owner"
)

//+kubebuilder:rbac:groups=cache.paragor.ru,resources=memcacheds,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=cache.paragor.ru,resources=memcacheds/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=cache.paragor.ru,resources=memcacheds/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete;deletecollection
//+kubebuilder:rbac:groups=core,resources=pods,verbs=get;list;watch

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Memcached object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.11.2/pkg/reconcile
func (r *MemcachedReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx, "processing-object", req.NamespacedName)

	memcached := &cachev1alpha1.Memcached{}
	err := r.Get(ctx, req.NamespacedName, memcached)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(4).Info("delete all deployments")
			if err := r.DeleteAllOf(
				ctx,
				&appsv1.Deployment{},
				client.InNamespace(req.Namespace),
				client.MatchingLabels{LabelOwnerKey: req.Name},
			); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{}, nil
		}

		return ctrl.Result{}, err
	}

	// Check if the deployment already exists, if not create a new deployment.
	found := &appsv1.Deployment{}
	err = r.Get(ctx, types.NamespacedName{Name: memcached.Name, Namespace: memcached.Namespace}, found)
	if err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(4).Info("create deployment")
			// Define and create a new deployment.
			dep, err := r.deploymentForMemcached(memcached)
			if err != nil {
				return ctrl.Result{}, err
			}
			if err = r.Create(ctx, dep); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{Requeue: true}, nil
		} else {
			return ctrl.Result{}, err
		}
	}

	// Ensure the deployment size is the same as the spec.
	size := memcached.Spec.Size
	if *found.Spec.Replicas != size {
		found.Spec.Replicas = &size
		logger.V(4).Info("update replicas count stat")
		if err = r.Update(ctx, found); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// Update the Memcached status with the pod names.
	// List the pods for this CR's deployment.
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(memcached.Namespace),
		client.MatchingLabels{LabelOwnerKey: req.Name},
	}
	if err = r.List(ctx, podList, listOpts...); err != nil {
		return ctrl.Result{}, err
	}

	// Update status.Nodes if needed.
	podNames := getPodNames(podList.Items)
	if !reflect.DeepEqual(podNames, memcached.Status.Nodes) {
		logger.V(4).Info("update pods nodenames")
		memcached.Status.Nodes = podNames
		if err := r.Status().Update(ctx, memcached); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

func getPodNames(list []corev1.Pod) []string {
	nodes := map[string]struct{}{}
	for _, pod := range list {
		nodeName := pod.Spec.NodeName
		if nodeName == "" {
			continue
		}
		nodes[nodeName] = struct{}{}
	}

	nodesList := make([]string, 0, len(nodes))
	for node := range nodes {
		nodesList = append(nodesList, node)
	}
	return nodesList
}

// deploymentForMemcached returns a Deployment object for data from m.
func (r *MemcachedReconciler) deploymentForMemcached(m *cachev1alpha1.Memcached) (*appsv1.Deployment, error) {
	lbls := map[string]string{LabelOwnerKey: m.Name}
	replicas := m.Spec.Size
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      m.Name,
			Namespace: m.Namespace,
			Labels:    lbls,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{
				MatchLabels: lbls,
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: lbls,
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Image:   "memcached:alpine",
						Name:    "memcached",
						Command: []string{"memcached", "-a=64", "-b"},
						Ports: []corev1.ContainerPort{{
							ContainerPort: 10000,
							Name:          "memcached",
						}},
					}},
				},
			},
		},
	}
	// Set Memcached instance as the owner and controller.memcac
	// NOTE: calling SetControllerReference, and setting owner references in
	// general, is important as it allows deleted objects to be garbage collected.
	err := controllerutil.SetControllerReference(m, dep, r.Scheme)
	return dep, err
}

// SetupWithManager sets up the controller with the Manager.
func (r *MemcachedReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&cachev1alpha1.Memcached{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

/*
Copyright 2025.

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
	"encoding/json"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	apiv1alpha1 "github.com/nuanqiu426/deploy-scaler-operator/api/v1alpha1"
)

// ScalerReconciler reconciles a Scaler object
type ScalerReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// key == deployment.Name
var originalDeploymentInfo = make(map[string]apiv1alpha1.DeploymentInfo)
var annotations = make(map[string]string)

// +kubebuilder:rbac:groups=api.scaler.com,resources=scalers,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=api.scaler.com,resources=scalers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=api.scaler.com,resources=scalers/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the Scaler object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.19.4/pkg/reconcile
var logger = log.Log.WithName("controller_scaler")

func (r *ScalerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logger.WithValues("Request.Namespace", req.Namespace, "Request.Name", req.Name)
	log.Info("Reconciling Scaler")
	// Fetch the Scaler instance
	scaler := &apiv1alpha1.Scaler{}
	err := r.Get(ctx, req.NamespacedName, scaler)
	if err != nil {
		// 如果没有发现，就忽略错误
		log.Error(err, "unable to fetch Scaler")
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if scaler.Status.State == "" {
		// 初始化状态
		scaler.Status.State = apiv1alpha1.PENGDING
		if err = r.Status().Update(ctx, scaler); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}
		// 初始化map + 添加scaler的annotation
		if err = addAnnotation(scaler, r, ctx); err != nil {
			return ctrl.Result{}, client.IgnoreNotFound(err)
		}

	}
	startTime := scaler.Spec.Start
	endTime := scaler.Spec.End
	replicas := scaler.Spec.Replicas

	currentHour := time.Now().Local().Hour()
	log.Info(fmt.Sprintf("Current Hour: %d", currentHour))

	// 从start 到 end 的时间段内，设置副本数
	if startTime <= currentHour && currentHour < endTime {
		if scaler.Status.State != apiv1alpha1.SCALED {
			// 执行扩充逻辑
			log.Info("Start to expand deployment")
			err = scalerExpand(scaler, r, ctx, replicas)
			if err != nil {
				log.Error(err, "unable to expand deployment")
				return ctrl.Result{}, err
			}
		}
	} else {
		if scaler.Status.State == apiv1alpha1.SCALED {
			log.Info("Start to shrinkage deployment")
			err := scalerShrinkage(scaler, r, ctx)
			if err != nil {
				log.Error(err, "unable to shrinkage deployment")
				return ctrl.Result{}, err
			}
		}
	}

	return ctrl.Result{RequeueAfter: time.Duration(60 * time.Second)}, nil
}

func scalerExpand(scaler *apiv1alpha1.Scaler, r *ScalerReconciler, ctx context.Context, replicas int32) error {
	// 扩容逻辑
	for _, deploy := range scaler.Spec.Deployments {
		tmpDeploy := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      deploy.Name,
			Namespace: deploy.Namespace,
		}, tmpDeploy)
		if err != nil {
			return err
		}

		// 判断scaler指定的deployment的副本数是否满足
		if tmpDeploy.Spec.Replicas != &replicas {
			tmpDeploy.Spec.Replicas = &replicas
			// 执行扩容
			if err := r.Update(ctx, tmpDeploy); err != nil {
				return err
			}
		}
	}

	scaler.Status.State = apiv1alpha1.SCALED
	err := r.Status().Update(ctx, scaler)
	if err != nil {
		return err
	}
	return nil
}

func scalerShrinkage(scaler *apiv1alpha1.Scaler, r *ScalerReconciler, ctx context.Context) error {
	// 缩容逻辑
	for name, deployInfo := range originalDeploymentInfo {
		tmpDeploy := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      name,
			Namespace: deployInfo.Namespace,
		}, tmpDeploy)
		if err != nil {
			return err
		}
		// 判断scaler指定的deployment的副本数是否满足
		if tmpDeploy.Spec.Replicas != &deployInfo.Replicas {
			tmpDeploy.Spec.Replicas = &deployInfo.Replicas
			// 执行缩容
			if err := r.Update(ctx, tmpDeploy); err != nil {
				return err
			}
		}
	}

	scaler.Status.State = apiv1alpha1.RESTORED
	err := r.Status().Update(ctx, scaler)
	if err != nil {
		return err
	}

	return nil
}

func addAnnotation(scaler *apiv1alpha1.Scaler, r *ScalerReconciler, ctx context.Context) error {
	// 记录deployment的原始副本数
	logger.Info("add original state to map")
	for _, deploy := range scaler.Spec.Deployments {
		tmpDeploy := &appsv1.Deployment{}
		err := r.Get(ctx, types.NamespacedName{
			Name:      deploy.Name,
			Namespace: deploy.Namespace,
		}, tmpDeploy)
		if err != nil {
			return err
		}

		if tmpDeploy.Spec.Replicas != &scaler.Spec.Replicas {
			originalDeploymentInfo[deploy.Name] = apiv1alpha1.DeploymentInfo{
				Namespace: tmpDeploy.Namespace,
				Replicas:  *tmpDeploy.Spec.Replicas,
			}
		}
	}

	// 添加annotation
	for deployName, deployInfo := range originalDeploymentInfo {
		infoJson, err := json.Marshal(deployInfo)
		if err != nil {
			return err
		}
		annotations[deployName] = string(infoJson)
	}

	// 更新scaler的annotation
	scaler.ObjectMeta.Annotations = annotations
	err := r.Update(ctx, scaler)
	if err != nil {
		return err
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *ScalerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&apiv1alpha1.Scaler{}).
		Named("scaler").
		Complete(r)
}

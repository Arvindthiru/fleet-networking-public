package membercluster

import (
	"context"
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/record"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	fleetnetv1alpha1 "go.goms.io/fleet-networking/api/v1alpha1"
	clusterv1beta1 "go.goms.io/fleet/apis/cluster/v1beta1"
)

var (
	fleetMemberNamespace = "fleet-member-%s"
)

const (
	ControllerName = "membercluster-watcher"
)

// Reconciler reconciles a MemberCluster object.
type Reconciler struct {
	client.Client
	Recorder record.EventRecorder
	// the wait time in minutes before we need to force delete a member cluster.
	ForceDeleteWaitTime time.Duration
}

// Reconcile watches the deletion of the member cluster and removes finalizers on fleet networking resources in the
// member cluster namespace.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	mcObjRef := klog.KRef(req.Namespace, req.Name)
	startTime := time.Now()
	klog.V(2).InfoS("Reconciliation starts", "memberCluster", mcObjRef)
	defer func() {
		latency := time.Since(startTime).Milliseconds()
		klog.V(2).InfoS("Reconciliation ends", "memberCluster", mcObjRef, "latency", latency)
	}()
	var mc clusterv1beta1.MemberCluster
	if err := r.Client.Get(ctx, req.NamespacedName, &mc); err != nil {
		if errors.IsNotFound(err) {
			klog.V(4).InfoS("Ignoring NotFound memberCluster", "memberCluster", mcObjRef)
			return ctrl.Result{}, nil
		}
		klog.ErrorS(err, "Failed to get memberCluster", "memberCluster", mcObjRef)
		return ctrl.Result{}, err
	}
	// Handle deleting member cluster, removes finalizers on all the resources in the cluster namespace
	// after member cluster force delete wait time.
	if !mc.DeletionTimestamp.IsZero() && time.Since(mc.DeletionTimestamp.Time) >= r.ForceDeleteWaitTime {
		klog.V(2).InfoS("The member cluster is leaving", "memberCluster", mcObjRef)
		return r.removeFinalizer(ctx, mc.DeepCopy())
	}

	return ctrl.Result{RequeueAfter: r.ForceDeleteWaitTime}, nil
}

// removeFinalizer removes finalizers on the resources in the member cluster namespace.
// for EndpointSliceExport, InternalServiceImport & InternalServiceExport resources, the finalizers
// are removed by other hub networking controllers on delete.
func (r *Reconciler) removeFinalizer(ctx context.Context, mc *clusterv1beta1.MemberCluster) (ctrl.Result, error) {
	// Remove finalizer for EndpointSliceImport resources in the cluster namespace.
	mcObjRef := klog.KRef(mc.Namespace, mc.Name)
	mcNamespace := fmt.Sprintf(fleetMemberNamespace, mc.Name)
	var endpointSliceImportList fleetnetv1alpha1.EndpointSliceImportList
	err := r.Client.List(ctx, &endpointSliceImportList, client.InNamespace(mcNamespace))
	if err != nil {
		klog.ErrorS(err, "Failed to list endpointSliceImports", "memberCluster", mcObjRef)
		return ctrl.Result{}, err
	}
	if len(endpointSliceImportList.Items) > 0 {
		for i := range endpointSliceImportList.Items {
			esi := &endpointSliceImportList.Items[i]
			esiObjRef := klog.KRef(esi.Namespace, esi.Name)
			esi.SetFinalizers(nil)
			if err := r.Client.Update(ctx, esi); err != nil {
				klog.ErrorS(err, "Failed to remove finalizers for endpointSliceImport", "memberCluster", mcObjRef, "endpointSliceImport", esiObjRef)
				return ctrl.Result{}, err
			}
			klog.V(2).InfoS("Removed finalizers for endpointSliceImport", "memberCluster", mcObjRef, "endpointSliceImport", esiObjRef)
		}
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	customPredicate := predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			// Ignore creation events.
			return false
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			// trigger reconcile on delete event just in case update event is missed.
			return true
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			// If new object is being deleted, trigger reconcile.
			if e.ObjectOld.GetDeletionTimestamp() == nil && e.ObjectNew.GetDeletionTimestamp() != nil {
				return true
			}
			return false
		},
	}
	// Watch for changes to primary resource MemberCluster
	return ctrl.NewControllerManagedBy(mgr).
		For(&clusterv1beta1.MemberCluster{}).
		WithEventFilter(customPredicate).
		Complete(r)
}

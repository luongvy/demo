package collectdplugin

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"reflect"
	"strings"

	"github.com/go-logr/logr"

	onapv1alpha1 "demo/vnfs/DAaaS/microservices/collectd-operator/pkg/apis/onap/v1alpha1"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	extensionsv1beta1 "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	logf "sigs.k8s.io/controller-runtime/pkg/runtime/log"
	"sigs.k8s.io/controller-runtime/pkg/source"
)

var log = logf.Log.WithName("controller_collectdplugin")

// ResourceMap to hold objects to update/reload
type ResourceMap struct {
	configMap       *corev1.ConfigMap
	daemonSet       *extensionsv1beta1.DaemonSet
	collectdPlugins *[]onapv1alpha1.CollectdPlugin
}

/**
* USER ACTION REQUIRED: This is a scaffold file intended for the user to modify with their own Controller
* business logic.  Delete these comments after modifying this file.*
 */

// Add creates a new CollectdPlugin Controller and adds it to the Manager. The Manager will set fields on the Controller
// and Start it when the Manager is Started.
func Add(mgr manager.Manager) error {
	return add(mgr, newReconciler(mgr))
}

// newReconciler returns a new reconcile.Reconciler
func newReconciler(mgr manager.Manager) reconcile.Reconciler {
	return &ReconcileCollectdPlugin{client: mgr.GetClient(), scheme: mgr.GetScheme()}
}

// add adds a new Controller to mgr with r as the reconcile.Reconciler
func add(mgr manager.Manager, r reconcile.Reconciler) error {
	// Create a new controller
	log.V(1).Info("Creating a new controller for CollectdPlugin")
	c, err := controller.New("collectdplugin-controller", mgr, controller.Options{Reconciler: r})
	if err != nil {
		return err
	}

	// Watch for changes to primary resource CollectdPlugin
	log.V(1).Info("Add watcher for primary resource CollectdPlugin")
	err = c.Watch(&source.Kind{Type: &onapv1alpha1.CollectdPlugin{}}, &handler.EnqueueRequestForObject{})
	if err != nil {
		return err
	}

	log.V(1).Info("Add watcher for secondary resource Collectd Daemonset")
	err = c.Watch(
		&source.Kind{Type: &appsv1.DaemonSet{}},
		&handler.EnqueueRequestsFromMapFunc{
			ToRequests: handler.ToRequestsFunc(func (a handler.MapObject) []reconcile.Request {
				labelSelector, err := getWatchLabels()
				labels := strings.Split(labelSelector, "=")
				if err != nil {
					log.Error(err, "Failed to get watch labels, continuing with default label")
				}
				rcp := r.(*ReconcileCollectdPlugin)
				// Select the Daemonset with labelSelector (Defautl  is app=collectd)
				if a.Meta.GetLabels()[labels[0]] == labels[1]  {
					var requests []reconcile.Request
					cpList, err := rcp.getCollectdPluginList(a.Meta.GetNamespace())
					if err != nil {
						return nil
					}
					for _, cp := range cpList.Items {
						requests = append(requests, reconcile.Request{
							NamespacedName: client.ObjectKey{Namespace: cp.Namespace, Name: cp.Name}})
					}
					return requests
				}
				return nil
			}),
		})
	if err != nil {
		return err
	}

	return nil
}


// blank assignment to verify that ReconcileCollectdPlugin implements reconcile.Reconciler
var _ reconcile.Reconciler = &ReconcileCollectdPlugin{}

// ReconcileCollectdPlugin reconciles a CollectdPlugin object
type ReconcileCollectdPlugin struct {
	// This client, initialized using mgr.Client() above, is a split client
	// that reads objects from the cache and writes to the apiserver
	client client.Client
	scheme *runtime.Scheme
}

// Define the collectdPlugin finalizer for handling deletion
const (
	defaultWatchLabel       = "app=collectd"
	collectdPluginFinalizer = "finalizer.collectdplugin.onap.org"

	// WatchLabelsEnvVar is the constant for env variable WATCH_LABELS
	// which is the labels where the watch activity happens.
	// this value is empty if the operator is running with clusterScope.
	WatchLabelsEnvVar = "WATCH_LABELS"
)

// Reconcile reads that state of the cluster for a CollectdPlugin object and makes changes based on the state read
// and what is in the CollectdPlugin.Spec
// TODO(user): Modify this Reconcile function to implement your Controller logic.  This example creates
// a Pod as an example
// Note:
// The Controller will requeue the Request to be processed again if the returned error is non-nil or
// Result.Requeue is true, otherwise upon completion it will remove the work from the queue.
func (r *ReconcileCollectdPlugin) Reconcile(request reconcile.Request) (reconcile.Result, error) {
	reqLogger := log.WithValues("Request.Namespace", request.Namespace, "Request.Name", request.Name)
	reqLogger.Info("Reconciling CollectdPlugin")

	// Fetch the CollectdPlugin instance
	instance := &onapv1alpha1.CollectdPlugin{}
	err := r.client.Get(context.TODO(), request.NamespacedName, instance)
	if err != nil {
		if errors.IsNotFound(err) {
			// Request object not found, could have been deleted after reconcile request.
			// Owned objects are automatically garbage collected. For additional cleanup logic use finalizers.
			// Return and don't requeue
			reqLogger.V(1).Info("CollectdPlugin object Not found")
			return reconcile.Result{}, nil
		}
		// Error reading the object - requeue the request.
		reqLogger.V(1).Info("Error reading the CollectdPlugin object, Requeuing")
		return reconcile.Result{}, err
	}

	// Handle Delete CR for additional cleanup
	isDelete, err := r.handleDelete(reqLogger, instance)
	if isDelete {
		return reconcile.Result{}, err
	}

	// Add finalizer for this CR
	if !contains(instance.GetFinalizers(), collectdPluginFinalizer) {
		if err := r.addFinalizer(reqLogger, instance); err != nil {
			return reconcile.Result{}, err
		}
		return reconcile.Result{}, nil
	}
	// Handle the reconciliation for CollectdPlugin.
	// At this stage the Status of the CollectdPlugin should NOT be ""
	err = r.handleCollectdPlugin(reqLogger, instance, false)
	return reconcile.Result{}, err
}

// handleCollectdPlugin regenerates the collectd conf on CR Create, Update, Delete events
func (r *ReconcileCollectdPlugin) handleCollectdPlugin(reqLogger logr.Logger, cr *onapv1alpha1.CollectdPlugin, isDelete bool) error {

	rmap, err := r.findResourceMapForCR(reqLogger, cr)
	if err != nil {
		reqLogger.Error(err, "Skip reconcile: Resources not found")
		return err
	}

	cm := rmap.configMap
	collectPlugins := rmap.collectdPlugins
	reqLogger.V(1).Info("Found ResourceMap")
	reqLogger.V(1).Info(":::: ConfigMap Info ::::", "ConfigMap.Namespace", cm.Namespace, "ConfigMap.Name", cm.Name)

	collectdConf, err := rebuildCollectdConf(cr, collectPlugins, isDelete)

	cm.SetAnnotations(map[string]string{
		"daaas-random": ComputeSHA256([]byte(collectdConf)),
	})

	cm.Data["node-collectd.conf"] = collectdConf

	// Update the ConfigMap with new Spec and reload DaemonSets
	reqLogger.Info("Updating the ConfigMap", "ConfigMap.Namespace", cm.Namespace, "ConfigMap.Name", cm.Name)
	log.V(1).Info("ConfigMap Data", "Map: ", cm.Data)
	err = r.client.Update(context.TODO(), cm)
	if err != nil {
		reqLogger.Error(err, "Update the ConfigMap failed", "ConfigMap.Namespace", cm.Namespace, "ConfigMap.Name", cm.Name)
		return err
	}

	retryErr := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		// Retrieve the latest version of Daemonset before attempting update
		// RetryOnConflict uses exponential backoff to avoid exhausting the apiserver
		// Select DaemonSets with label
		dsList := &extensionsv1beta1.DaemonSetList{}
		opts := &client.ListOptions{}
		labelSelector, err := getWatchLabels()
		if err != nil {
			reqLogger.Error(err, "Failed to get watch labels, continuing with default label")
		}
		opts.SetLabelSelector(labelSelector)
		opts.InNamespace(cr.Namespace)
		err = r.client.List(context.TODO(), opts, dsList)
		if err != nil {
			panic(fmt.Errorf("Failed to get latest version of DaemonSet: %v", err))
		}

		if dsList.Items == nil || len(dsList.Items) == 0 {
			return errors.NewNotFound(corev1.Resource("daemonset"), "DaemonSet")
		}
		ds := &dsList.Items[0]
		//Restart Collectd Pods
		reqLogger.Info("Reloading the Daemonset", "DaemonSet.Namespace", ds.Namespace, "DaemonSet.Name", ds.Name)
		//Restart only if hash of conf has changed.
		ds.Spec.Template.SetAnnotations(map[string]string{
			"daaas-random": ComputeSHA256([]byte(collectdConf)),
		})
		updateErr := r.client.Update(context.TODO(), ds)
		return updateErr
	})
	if retryErr != nil {
		panic(fmt.Errorf("Update failed: %v", retryErr))
	}

	err = r.updateStatus(cr)
	if err != nil {
		reqLogger.Error(err, "Unable to update status")
		return err
	}
	// Reconcile success
	reqLogger.Info("Reconcile success!!")
	return nil
}

// ComputeSHA256  returns hash of data as string
func ComputeSHA256(data []byte) string {
	hash := sha256.Sum256(data)
	return fmt.Sprintf("%x", hash)
}

// findResourceMapForCR returns the configMap, collectd Daemonset and list of Collectd Plugins
func (r *ReconcileCollectdPlugin) findResourceMapForCR(reqLogger logr.Logger, cr *onapv1alpha1.CollectdPlugin) (ResourceMap, error) {
	cmList := &corev1.ConfigMapList{}
	opts := &client.ListOptions{}
	rmap := ResourceMap{}

	// Select ConfigMaps with label
	labelSelector, err := getWatchLabels()
	if err != nil {
		reqLogger.Error(err, "Failed to get watch labels, continuing with default label")
	}
	opts.SetLabelSelector(labelSelector)
	opts.InNamespace(cr.Namespace)

	err = r.client.List(context.TODO(), opts, cmList)
	if err != nil {
		return rmap, err
	}

	if cmList.Items == nil || len(cmList.Items) == 0 {
		return rmap, errors.NewNotFound(corev1.Resource("configmap"), "ConfigMap")
	}

	// Select DaemonSets with label
	dsList := &extensionsv1beta1.DaemonSetList{}
	err = r.client.List(context.TODO(), opts, dsList)
	if err != nil {
		return rmap, err
	}

	if dsList.Items == nil || len(dsList.Items) == 0 {
		return rmap, errors.NewNotFound(corev1.Resource("daemonset"), "DaemonSet")
	}

	// Get all collectd plugins in the current namespace to rebuild conf.
	cpList, err := r.getCollectdPluginList(cr.Namespace)
	if err != nil {
		return rmap, err
	}

	rmap.configMap = &cmList.Items[0]
	rmap.daemonSet = &dsList.Items[0]
	rmap.collectdPlugins = &cpList.Items //will be nil if no plugins exist
	return rmap, err
}

// Get all collectd plugins and reconstruct, compute Hash and check for changes
func rebuildCollectdConf(cr *onapv1alpha1.CollectdPlugin, cpList *[]onapv1alpha1.CollectdPlugin, isDelete bool) (string, error) {
	var collectdConf string
	if *cpList == nil || len(*cpList) == 0 {
		return "", errors.NewNotFound(corev1.Resource("collectdplugin"), "CollectdPlugin")
	}
	loadPlugin := make(map[string]string)
	for _, cp := range *cpList {
		if cp.Spec.PluginName == "global" {
			collectdConf += cp.Spec.PluginConf + "\n"
		} else {
			loadPlugin[cp.Spec.PluginName] = cp.Spec.PluginConf
		}
	}

	if isDelete {
		delete(loadPlugin, cr.Spec.PluginName)
	}

	log.V(1).Info("::::::: Plugins Map ::::::: ", "PluginMap ", loadPlugin)

	for cpName, cpConf := range loadPlugin {
		collectdConf += "LoadPlugin" + " " + cpName + "\n"
		collectdConf += cpConf + "\n"
	}

	collectdConf += "#Last line (collectd requires ‘\\n’ at the last line)\n"

	return collectdConf, nil
}

// Handle Delete CR event for additional cleanup
func (r *ReconcileCollectdPlugin) handleDelete(reqLogger logr.Logger, cr *onapv1alpha1.CollectdPlugin) (bool, error) {
	// Check if the CollectdPlugin instance is marked to be deleted, which is
	// indicated by the deletion timestamp being set.
	isMarkedToBeDeleted := cr.GetDeletionTimestamp() != nil
	if isMarkedToBeDeleted {
		// Update status to Deleting state
		cr.Status.Status = onapv1alpha1.Deleting
		cr.Status.CollectdAgents = nil
		_ = r.client.Status().Update(context.TODO(), cr)

		if contains(cr.GetFinalizers(), collectdPluginFinalizer) {
			// Run finalization logic for collectdPluginFinalizer. If the
			// finalization logic fails, don't remove the finalizer so
			// that we can retry during the next reconciliation.
			if err := r.finalizeCollectdPlugin(reqLogger, cr); err != nil {
				return isMarkedToBeDeleted, err
			}

			// Remove collectdPluginFinalizer. Once all finalizers have been
			// removed, the object will be deleted.
			cr.SetFinalizers(remove(cr.GetFinalizers(), collectdPluginFinalizer))
			err := r.client.Update(context.TODO(), cr)
			if err != nil {
				return isMarkedToBeDeleted, err
			}
		}
	}
	return isMarkedToBeDeleted, nil
}

func (r *ReconcileCollectdPlugin) updateStatus(cr *onapv1alpha1.CollectdPlugin) error {
	switch cr.Status.Status {
	case onapv1alpha1.Initial:
		cr.Status.Status = onapv1alpha1.Created
	case onapv1alpha1.Created, onapv1alpha1.Enabled:
		pods, err := r.getPodList(cr.Namespace)
		if err != nil {
			return err
		}
		if !reflect.DeepEqual(pods, cr.Status.CollectdAgents) {
			cr.Status.CollectdAgents = pods
			cr.Status.Status = onapv1alpha1.Enabled
		}
	case onapv1alpha1.Deleting, onapv1alpha1.Deprecated:
		return nil
	}
	err := r.client.Status().Update(context.TODO(), cr)
	return err
}

func (r *ReconcileCollectdPlugin) finalizeCollectdPlugin(reqLogger logr.Logger, cr *onapv1alpha1.CollectdPlugin) error {
	// Cleanup by regenerating new collectd conf and rolling update of DaemonSet
	if err := r.handleCollectdPlugin(reqLogger, cr, true); err != nil {
		reqLogger.Error(err, "Finalize CollectdPlugin failed!!")
		return err
	}
	reqLogger.Info("Successfully finalized CollectdPlugin!!")
	return nil
}

func (r *ReconcileCollectdPlugin) addFinalizer(reqLogger logr.Logger, cr *onapv1alpha1.CollectdPlugin) error {
	reqLogger.Info("Adding Finalizer for the CollectdPlugin")
	cr.SetFinalizers(append(cr.GetFinalizers(), collectdPluginFinalizer))
	// Update status from Initial to Created
	// Since addFinalizer will be executed only once,
	// the status will be changed from Initial state to Created
	updateErr := r.updateStatus(cr)
	if updateErr != nil {
		reqLogger.Error(updateErr, "Failed to update status from Initial state")
	}
	// Update CR
	err := r.client.Update(context.TODO(), cr)
	if err != nil {
		reqLogger.Error(err, "Failed to update CollectdPlugin with finalizer")
		return err
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func remove(list []string, s string) []string {
	for i, v := range list {
		if v == s {
			list = append(list[:i], list[i+1:]...)
		}
	}
	return list
}

// getWatchLabels returns the labels the operator should be watching for changes
func getWatchLabels() (string, error) {
	labelSelector, found := os.LookupEnv(WatchLabelsEnvVar)
	if !found {
		return defaultWatchLabel, fmt.Errorf("%s must be set", WatchLabelsEnvVar)
	}
	return labelSelector, nil
}

func (r *ReconcileCollectdPlugin) getPodList(ns string) ([]string, error) {
	var pods []string
	podList := &corev1.PodList{}
	opts := &client.ListOptions{}
	// Select ConfigMaps with label
	labelSelector, _ := getWatchLabels()
	opts.SetLabelSelector(labelSelector)
	opts.InNamespace(ns)
	err := r.client.List(context.TODO(), opts, podList)
	if err != nil {
		return nil, err
	}

	if podList.Items == nil || len(podList.Items) == 0 {
		return nil, err
	}

	for _, pod := range podList.Items {
		pods = append(pods, pod.Name)
	}
	return pods, nil
}

func (r *ReconcileCollectdPlugin) getCollectdPluginList(ns string) (*onapv1alpha1.CollectdPluginList, error) {
	// Get all collectd plugins in the current namespace to rebuild conf.
	collectdPlugins := &onapv1alpha1.CollectdPluginList{}
	cpOpts := &client.ListOptions{}
	cpOpts.InNamespace(ns)
	err := r.client.List(context.TODO(), cpOpts, collectdPlugins)
	if err != nil {
		return nil, err
	}
	return collectdPlugins, nil
}
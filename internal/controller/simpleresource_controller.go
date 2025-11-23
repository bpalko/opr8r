package controller

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/kbpalko/opr8r/api/v1alpha1"
	"github.com/kbpalko/opr8r/pkg/terraform"
)

const (
	SimpleFinalizerName = "infra.opr8r/simple-finalizer"

	PhasePending    = "Pending"
	PhaseApplying   = "Applying"
	PhaseReady      = "Ready"
	PhaseError      = "Error"
	PhaseDestroying = "Destroying"
)

// SimpleResourceReconciler reconciles a SimpleResource object
type SimpleResourceReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// Configuration
	TemplatePath string
}

// +kubebuilder:rbac:groups=infra.opr8r,resources=simpleresources,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=infra.opr8r,resources=simpleresources/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=infra.opr8r,resources=simpleresources/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=create;get;list
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=rolebindings,verbs=create;get;list

// Reconcile is part of the main kubernetes reconciliation loop
func (r *SimpleResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Fetch the SimpleResource resource
	simple := &infrav1alpha1.SimpleResource{}
	if err := r.Get(ctx, req.NamespacedName, simple); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		logger.Error(err, "Failed to get SimpleResource")
		return ctrl.Result{}, err
	}

	// Handle deletion
	if !simple.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.reconcileDelete(ctx, simple)
	}

	// Add finalizer if not present
	if !controllerutil.ContainsFinalizer(simple, SimpleFinalizerName) {
		controllerutil.AddFinalizer(simple, SimpleFinalizerName)
		if err := r.Update(ctx, simple); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Ensure ServiceAccount exists in this namespace
	if err := r.ensureServiceAccount(ctx, simple.Namespace); err != nil {
		logger.Error(err, "Failed to ensure ServiceAccount exists")
		return ctrl.Result{}, err
	}

	// Reconcile the SimpleResource
	return r.reconcileNormal(ctx, simple)
}

// reconcileNormal handles the normal reconciliation flow
func (r *SimpleResourceReconciler) reconcileNormal(ctx context.Context, simple *infrav1alpha1.SimpleResource) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	// Compute spec hash for change detection
	currentHash := terraform.ComputeSpecHash(&simple.Spec)

	// Check if we need to apply changes
	needsApply := false
	if simple.Status.Phase == "" || simple.Status.Phase == PhasePending {
		needsApply = true
	} else if simple.Status.LastAppliedHash != currentHash {
		needsApply = true
		logger.Info("Spec has changed, will reapply", "oldHash", simple.Status.LastAppliedHash, "newHash", currentHash)
	}

	if !needsApply && simple.Status.Phase == PhaseReady {
		// Nothing to do
		return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
	}

	// Check if there's already a running job
	hasRunningJob, err := r.hasRunningJob(ctx, simple)
	if err != nil {
		logger.Error(err, "Failed to check for running jobs")
		return ctrl.Result{}, err
	}
	if hasRunningJob {
		logger.Info("Terraform job already running, waiting for completion")
		simple.Status.Phase = PhaseApplying
		simple.Status.Message = "Waiting for existing Terraform job to complete"
		if err := r.Status().Update(ctx, simple); err != nil {
			logger.Error(err, "Failed to update status")
		}
		// Requeue quickly to check again
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Update phase to Applying
	simple.Status.Phase = PhaseApplying
	simple.Status.Message = "Running Terraform apply"
	if err := r.Status().Update(ctx, simple); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	// Create Terraform executor
	executor := terraform.NewExecutor(
		r.Client,
		r.TemplatePath,
		simple.Namespace,
	)

	// Run Terraform apply
	outputs, err := executor.Apply(ctx, simple)
	if err != nil {
		logger.Error(err, "Failed to apply Terraform")
		simple.Status.Phase = PhaseError
		simple.Status.Message = fmt.Sprintf("Terraform apply failed: %v", err)
		if updateErr := r.Status().Update(ctx, simple); updateErr != nil {
			logger.Error(updateErr, "Failed to update error status")
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, err
	}

	// Extract outputs
	randomValue := outputs["random_value"]
	filePath := outputs["file_path"]

	// Update status
	simple.Status.Phase = PhaseReady
	simple.Status.Message = "SimpleResource is ready"
	simple.Status.RandomValue = randomValue
	simple.Status.FilePath = filePath
	simple.Status.LastAppliedHash = currentHash

	if err := r.Status().Update(ctx, simple); err != nil {
		logger.Error(err, "Failed to update status")
		return ctrl.Result{}, err
	}

	// Create or update Secret with outputs
	if err := r.reconcileSecret(ctx, simple, outputs); err != nil {
		logger.Error(err, "Failed to reconcile Secret")
		return ctrl.Result{}, err
	}

	logger.Info("Successfully reconciled SimpleResource", "randomValue", randomValue, "filePath", filePath)
	return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
}

// reconcileDelete handles deletion with finalizer
func (r *SimpleResourceReconciler) reconcileDelete(ctx context.Context, simple *infrav1alpha1.SimpleResource) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(simple, SimpleFinalizerName) {
		return ctrl.Result{}, nil
	}

	// Check if there's already a running destroy job
	hasRunningJob, err := r.hasRunningJob(ctx, simple)
	if err != nil {
		logger.Error(err, "Failed to check for running jobs")
		return ctrl.Result{}, err
	}
	if hasRunningJob {
		logger.Info("Terraform destroy job already running, waiting for completion")
		simple.Status.Phase = PhaseDestroying
		simple.Status.Message = "Waiting for existing Terraform destroy job to complete"
		if err := r.Status().Update(ctx, simple); err != nil {
			logger.Error(err, "Failed to update status")
		}
		// Requeue quickly to check again
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}

	// Update phase
	if simple.Status.Phase != PhaseDestroying {
		simple.Status.Phase = PhaseDestroying
		simple.Status.Message = "Running Terraform destroy"
		if err := r.Status().Update(ctx, simple); err != nil {
			logger.Error(err, "Failed to update status")
			return ctrl.Result{}, err
		}
	}

	// Create Terraform executor
	executor := terraform.NewExecutor(
		r.Client,
		r.TemplatePath,
		simple.Namespace,
	)

	// Run Terraform destroy
	if err := executor.Destroy(ctx, simple); err != nil {
		logger.Error(err, "Failed to destroy Terraform resources")
		simple.Status.Message = fmt.Sprintf("Terraform destroy failed: %v", err)
		if updateErr := r.Status().Update(ctx, simple); updateErr != nil {
			logger.Error(updateErr, "Failed to update error status")
		}
		return ctrl.Result{RequeueAfter: 1 * time.Minute}, err
	}

	// Remove finalizer
	controllerutil.RemoveFinalizer(simple, SimpleFinalizerName)
	if err := r.Update(ctx, simple); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("Successfully destroyed SimpleResource")
	return ctrl.Result{}, nil
}

// reconcileSecret creates or updates a Secret with Terraform outputs
func (r *SimpleResourceReconciler) reconcileSecret(ctx context.Context, simple *infrav1alpha1.SimpleResource, outputs map[string]string) error {
	secretName := fmt.Sprintf("%s-simple-outputs", simple.Name)

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      secretName,
			Namespace: simple.Namespace,
		},
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		// Set owner reference
		if err := controllerutil.SetControllerReference(simple, secret, r.Scheme); err != nil {
			return err
		}

		// Update secret data
		if secret.Data == nil {
			secret.Data = make(map[string][]byte)
		}

		for key, value := range outputs {
			secret.Data[key] = []byte(value)
		}

		secret.Type = corev1.SecretTypeOpaque
		return nil
	})

	return err
}

// hasRunningJob checks if there's an active Terraform job for this resource
func (r *SimpleResourceReconciler) hasRunningJob(ctx context.Context, simple *infrav1alpha1.SimpleResource) (bool, error) {
	jobList := &batchv1.JobList{}

	// List all jobs with our label
	err := r.List(ctx, jobList,
		client.InNamespace(simple.Namespace),
		client.MatchingLabels{
			"infra.opr8r/simpleresource": simple.Name,
			"app":                        "terraform",
		},
	)
	if err != nil {
		return false, err
	}

	// Check if any job is still running
	for _, job := range jobList.Items {
		// Job is running if it hasn't completed and hasn't failed
		if job.Status.Succeeded == 0 && job.Status.Failed == 0 {
			return true, nil
		}
		// Also check if job has active pods
		if job.Status.Active > 0 {
			return true, nil
		}
	}

	return false, nil
}

// ensureServiceAccount creates the terraform-executor ServiceAccount and RoleBinding if they don't exist
func (r *SimpleResourceReconciler) ensureServiceAccount(ctx context.Context, namespace string) error {
	// Create ServiceAccount
	sa := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "terraform-executor",
			Namespace: namespace,
		},
	}

	err := r.Get(ctx, client.ObjectKey{Name: sa.Name, Namespace: namespace}, sa)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// Create the ServiceAccount
		if err := r.Create(ctx, sa); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create ServiceAccount: %w", err)
		}
	}

	// Create RoleBinding to grant permissions
	rb := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "terraform-executor-binding",
			Namespace: namespace,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     "opr8r-manager-role",
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      "ServiceAccount",
				Name:      "terraform-executor",
				Namespace: namespace,
			},
		},
	}

	err = r.Get(ctx, client.ObjectKey{Name: rb.Name, Namespace: namespace}, rb)
	if err != nil {
		if !apierrors.IsNotFound(err) {
			return err
		}
		// Create the RoleBinding
		if err := r.Create(ctx, rb); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create RoleBinding: %w", err)
		}
	}

	return nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *SimpleResourceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&infrav1alpha1.SimpleResource{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}

package terraform

import (
	"context"
	"fmt"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	infrav1alpha1 "github.com/kbpalko/opr8r/api/v1alpha1"
)

const (
	TerraformImage       = "hashicorp/terraform:1.7.0"
	JobCompletionTimeout = 30 * time.Minute
)

// JobRunner manages Terraform execution via Kubernetes Jobs
type JobRunner struct {
	client    client.Client
	workDir   string
	action    string // "apply" or "destroy"
	namespace string
}

// NewJobRunner creates a new JobRunner instance
func NewJobRunner(client client.Client, workDir, action, namespace string) *JobRunner {
	return &JobRunner{
		client:    client,
		workDir:   workDir,
		action:    action,
		namespace: namespace,
	}
}

// RunJob creates and waits for a Terraform job to complete
func (jr *JobRunner) RunJob(ctx context.Context, simple *infrav1alpha1.SimpleResource) ([]byte, error) {
	logger := log.FromContext(ctx)
	logger.Info("Starting Terraform job", "Action", jr.action, "SimpleResource", simple.Name)

	// Unique job name
	jobName := fmt.Sprintf("terraform-%s-%s-%s", jr.action, simple.Name, time.Now().Format("20060102-150405"))

	// Create ConfigMap with Terraform files
	logger.Info("Creating ConfigMap for Terraform files", "JobName", jobName)
	configMap, err := jr.createConfigMap(ctx, simple, jobName)
	if err != nil {
		return nil, fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	// Create the Job
	logger.Info("Creating Terraform Job", "JobName", jobName)
	job := jr.buildJob(simple, jobName, configMap.Name)
	if err := controllerutil.SetControllerReference(simple, job, jr.client.Scheme()); err != nil {
		return nil, fmt.Errorf("failed to set controller reference: %w", err)
	}

	if err := jr.client.Create(ctx, job); err != nil {
		return nil, fmt.Errorf("failed to create job: %w", err)
	}

	// Wait for job completion
	if err := jr.waitForJobCompletion(ctx, jobName); err != nil {
		return nil, err
	}

	// Get outputs if this was an apply action
	logger.Info("Terraform job completed successfully -- fetching outputs", "JobName", jobName)
	if jr.action == "apply" {
		return jr.getJobOutputs(ctx, jobName)
	}

	return nil, nil
}

// createConfigMap creates a ConfigMap containing the Terraform files
func (jr *JobRunner) createConfigMap(ctx context.Context, simple *infrav1alpha1.SimpleResource, jobName string) (*corev1.ConfigMap, error) {
	// Read the rendered Terraform file
	tfContent, err := readFile(jr.workDir + "/main.tf")
	if err != nil {
		return nil, fmt.Errorf("failed to read terraform file: %w", err)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName + "-config",
			Namespace: jr.namespace,
		},
		Data: map[string]string{
			"main.tf": string(tfContent),
		},
	}

	if err := controllerutil.SetControllerReference(simple, configMap, jr.client.Scheme()); err != nil {
		return nil, err
	}

	if err := jr.client.Create(ctx, configMap); err != nil {
		return nil, err
	}

	return configMap, nil
}

// buildJob constructs the Kubernetes Job specification
func (jr *JobRunner) buildJob(simple *infrav1alpha1.SimpleResource, jobName, configMapName string) *batchv1.Job {
	backoffLimit := int32(1)
	ttlSecondsAfterFinished := int32(3600) // 1 hour

	var tfCommand []string
	var containers []corev1.Container

	if jr.action == "apply" {
		// Terraform container command for apply
		tfCommand = []string{
			"/bin/sh",
			"-c",
			"cp /tf-config/* /workspace/ && cd /workspace && terraform init && terraform apply -auto-approve && terraform output -json > /outputs/outputs.json && touch /outputs/done",
		}

		// Add terraform container
		containers = append(containers, corev1.Container{
			Name:    "terraform",
			Image:   TerraformImage,
			Command: tfCommand,
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "terraform-config",
					MountPath: "/tf-config",
					ReadOnly:  true,
				},
				{
					Name:      "workspace",
					MountPath: "/workspace",
				},
				{
					Name:      "outputs",
					MountPath: "/outputs",
				},
			},
		})

		// Add sidecar container that creates Secret with outputs
		// This container waits for terraform to finish, then creates a Secret
		outputSecretName := jobName + "-outputs"
		sidecarCommand := fmt.Sprintf(`
# Wait for terraform to complete
while [ ! -f /outputs/done ]; do
  echo "Waiting for terraform to complete..."
  sleep 2
done

echo "Terraform completed, creating Secret with outputs..."

# Create Secret with outputs
kubectl create secret generic %s \
  --from-file=outputs.json=/outputs/outputs.json \
  --namespace=%s

echo "Secret created successfully"
`, outputSecretName, jr.namespace)

		containers = append(containers, corev1.Container{
			Name:    "output-handler",
			Image:   "bitnami/kubectl:latest",
			Command: []string{"/bin/sh", "-c", sidecarCommand},
			VolumeMounts: []corev1.VolumeMount{
				{
					Name:      "outputs",
					MountPath: "/outputs",
				},
			},
		})
	} else {
		// Destroy action - only terraform container needed
		tfCommand = []string{
			"/bin/sh",
			"-c",
			"cp /tf-config/* /workspace/ && cd /workspace && terraform init && terraform destroy -auto-approve",
		}

		containers = []corev1.Container{
			{
				Name:    "terraform",
				Image:   TerraformImage,
				Command: tfCommand,
				VolumeMounts: []corev1.VolumeMount{
					{
						Name:      "terraform-config",
						MountPath: "/tf-config",
						ReadOnly:  true,
					},
					{
						Name:      "workspace",
						MountPath: "/workspace",
					},
				},
			},
		}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: jr.namespace,
			Labels: map[string]string{
				"app":                        "terraform",
				"action":                     jr.action,
				"infra.opr8r/simpleresource": simple.Name,
			},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttlSecondsAfterFinished,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"app":                        "terraform",
						"action":                     jr.action,
						"infra.opr8r/simpleresource": simple.Name,
					},
				},
				Spec: corev1.PodSpec{
					RestartPolicy:      corev1.RestartPolicyNever,
					ServiceAccountName: fmt.Sprintf("terraform-executor-%s", simple.Name),
					Containers:         containers,
					Volumes: []corev1.Volume{
						{
							Name: "terraform-config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: configMapName,
									},
								},
							},
						},
						{
							Name: "workspace",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
						{
							Name: "outputs",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{},
							},
						},
					},
				},
			},
		},
	}

	return job
}

// waitForJobCompletion waits for the Job to complete or fail
func (jr *JobRunner) waitForJobCompletion(ctx context.Context, jobName string) error {
	timeout := time.After(JobCompletionTimeout)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			return fmt.Errorf("job %s timed out after %v", jobName, JobCompletionTimeout)
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			job := &batchv1.Job{}
			err := jr.client.Get(ctx, types.NamespacedName{
				Name:      jobName,
				Namespace: jr.namespace,
			}, job)
			if err != nil {
				return fmt.Errorf("failed to get job status: %w", err)
			}

			// Check if job completed successfully
			if job.Status.Succeeded > 0 {
				return nil
			}

			// Check if job failed
			if job.Status.Failed > 0 {
				return fmt.Errorf("job %s failed", jobName)
			}
		}
	}
}

// getJobOutputs retrieves Terraform outputs from the completed job
func (jr *JobRunner) getJobOutputs(ctx context.Context, jobName string) ([]byte, error) {
	// The sidecar container creates a Secret with the outputs
	outputSecretName := jobName + "-outputs"
	outputSecret := &corev1.Secret{}

	// Try to get the outputs Secret (the job sidecar should have created it)
	err := jr.client.Get(ctx, types.NamespacedName{
		Name:      outputSecretName,
		Namespace: jr.namespace,
	}, outputSecret)

	if err != nil {
		if apierrors.IsNotFound(err) {
			// Secret not found, return empty outputs
			return []byte("{}"), nil
		}
		return nil, fmt.Errorf("failed to get outputs: %w", err)
	}

	// Extract the outputs
	outputs := outputSecret.Data["outputs.json"]

	// Delete the temporary Secret - we've extracted what we need
	// The user-facing Secret will be created by reconcileSecret()
	if err := jr.client.Delete(ctx, outputSecret); err != nil && !apierrors.IsNotFound(err) {
		// Log but don't fail - we got the outputs already
		// The Secret will be cleaned up by Job TTL anyway
		return outputs, nil
	}

	return outputs, nil
}

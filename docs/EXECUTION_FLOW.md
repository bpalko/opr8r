# Execution Flow: From kubectl apply to Terraform Success

This document explains the complete flow from when you run `kubectl apply -f simple.yaml` to when Terraform successfully applies and outputs are stored.

## Table of Contents
- [Overview](#overview)
- [Step-by-Step Flow](#step-by-step-flow)
- [Deep Dive: Template Rendering](#deep-dive-template-rendering)
- [Deep Dive: Job Execution](#deep-dive-job-execution)
- [Deep Dive: Output Extraction](#deep-dive-output-extraction)
- [State File Management](#state-file-management)
- [Filesystem Layouts](#filesystem-layouts)
- [Timeline Example](#timeline-example)
- [Current Limitations](#current-limitations)

## Overview

```
kubectl apply -f simple.yaml
    ↓
API Server stores SimpleResource
    ↓
Controller watches and detects new resource
    ↓
Reconciliation loop triggered
    ↓
[Render] → [Create Job] → [Wait] → [Extract Outputs] → [Update Status & Secret]
```

## Step-by-Step Flow

### 1. User Creates Resource

```bash
kubectl apply -f config/samples/simple_v1alpha1_simpleresource.yaml
```

```yaml
apiVersion: infra.opr8r/v1alpha1
kind: SimpleResource
metadata:
  name: simpleresource-sample
  namespace: default
spec:
  length: 16
  prefix: "test"
  vars:
    special: "true"
    upper: "true"
```

**What happens**:
- kubectl sends the resource to the Kubernetes API server
- API server validates the CRD schema
- Resource is persisted to etcd
- API server returns success to kubectl

### 2. Controller Detects Resource

**File**: `cmd/main.go`

The controller-runtime watch mechanism detects the new resource:

```go
// Controller is watching SimpleResource objects
ctrl.NewControllerManagedBy(mgr).
    For(&infrav1alpha1.SimpleResource{}).
    Owns(&corev1.Secret{}).
    Complete(r)
```

**What happens**:
- Controller-runtime's watch detects the new SimpleResource
- Reconcile request is queued
- Worker picks up the request
- `Reconcile()` function is called

### 3. Reconcile Entry Point

**File**: `internal/controller/simpleresource_controller.go`

```go
func (r *SimpleResourceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
    // Fetch the resource
    simple := &infrav1alpha1.SimpleResource{}
    r.Get(ctx, req.NamespacedName, simple)

    // Check for deletion
    if !simple.ObjectMeta.DeletionTimestamp.IsZero() {
        return r.reconcileDelete(ctx, simple)
    }

    // Add finalizer if needed
    if !controllerutil.ContainsFinalizer(simple, SimpleFinalizerName) {
        controllerutil.AddFinalizer(simple, SimpleFinalizerName)
        r.Update(ctx, simple)
    }

    // Normal reconciliation
    return r.reconcileNormal(ctx, simple)
}
```

**What happens**:
- Resource is fetched from API server
- Deletion check performed (not deleting in this case)
- Finalizer added to prevent deletion without cleanup
- Routes to `reconcileNormal()`

### 4. Check if Apply Needed

**File**: `internal/controller/simpleresource_controller.go`

```go
func (r *SimpleResourceReconciler) reconcileNormal(ctx context.Context, simple *infrav1alpha1.SimpleResource) (ctrl.Result, error) {
    // Compute hash of spec
    currentHash := terraform.ComputeSpecHash(&simple.Spec)

    // Check if we need to apply
    needsApply := false
    if simple.Status.Phase == "" || simple.Status.Phase == PhasePending {
        needsApply = true
    } else if simple.Status.LastAppliedHash != currentHash {
        needsApply = true
    }

    if !needsApply && simple.Status.Phase == PhaseReady {
        return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
    }
}
```

**What happens**:
- Spec is hashed using SHA256
- Hash compared to `status.lastAppliedHash`
- If hash differs or phase is empty/pending, apply is needed
- If already Ready with same hash, nothing to do (idempotent)

### 5. Check for Running Jobs

**File**: `internal/controller/simpleresource_controller.go`

```go
// Check if there's already a running job
hasRunningJob, err := r.hasRunningJob(ctx, simple)
if hasRunningJob {
    logger.Info("Terraform job already running, waiting for completion")
    simple.Status.Phase = PhaseApplying
    simple.Status.Message = "Waiting for existing Terraform job to complete"
    r.Status().Update(ctx, simple)
    return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}
```

**What happens**:
- Lists all Jobs with label `infra.opr8r/simpleresource=simpleresource-sample`
- Checks if any have `Status.Active > 0` or not completed
- If running job exists, waits and requeues
- Ensures only one Terraform job runs at a time per resource

### 6. Update Phase to Applying

```go
simple.Status.Phase = PhaseApplying
simple.Status.Message = "Running Terraform apply"
r.Status().Update(ctx, simple)
```

**What happens**:
- Status subresource updated in API server
- Users can see progress: `kubectl get simple -w`

### 7. Create Terraform Executor

**File**: `internal/controller/simpleresource_controller.go`

```go
executor := terraform.NewExecutor(
    r.Client,
    r.TemplatePath,  // "./modules/simple/main.tf.tpl"
    simple.Namespace,
)
```

**File**: `pkg/terraform/executor.go`

```go
func NewExecutor(client client.Client, templatePath, namespace string) *Executor {
    return &Executor{
        client:       client,
        templatePath: templatePath,
        namespace:    namespace,
    }
}
```

**What happens**:
- Executor is created with template path and namespace
- This orchestrates the render → run → parse workflow

### 8. Render Terraform Template

**File**: `pkg/terraform/executor.go`

```go
func (e *Executor) Apply(ctx context.Context, simple *infrav1alpha1.SimpleResource) (map[string]string, error) {
    // Render template
    renderer := NewRenderer(e.templatePath)
    workDir, err := renderer.Render(simple)
    if err != nil {
        return nil, err
    }
}
```

**File**: `pkg/terraform/renderer.go`

```go
func (r *Renderer) Render(simple *infrav1alpha1.SimpleResource) (string, error) {
    // Create working directory
    workDir := filepath.Join("/tmp", fmt.Sprintf("terraform-%s-%s", simple.Namespace, simple.Name))
    os.MkdirAll(workDir, 0755)

    // Read template file
    templateContent, err := os.ReadFile(r.templatePath)

    // Parse template
    tmpl, err := template.New("terraform").Parse(string(templateContent))

    // Prepare data
    data := TemplateData{
        Namespace: simple.Namespace,
        Name:      simple.Name,
        Length:    simple.Spec.Length,
        Prefix:    simple.Spec.Prefix,
        Vars:      simple.Spec.Vars,
    }

    // Execute template
    output := &bytes.Buffer{}
    tmpl.Execute(output, data)

    // Write rendered file
    outputPath := filepath.Join(workDir, "main.tf")
    os.WriteFile(outputPath, output.Bytes(), 0644)

    return workDir, nil
}
```

**What happens**:
- Working directory created: `/tmp/terraform-default-simpleresource-sample/`
- Template file read: `modules/simple/main.tf.tpl`
- Go template parsed and executed with SimpleResource data
- Rendered `main.tf` written to working directory
- See [Deep Dive: Template Rendering](#deep-dive-template-rendering)

### 9. Create Job Runner

**File**: `pkg/terraform/executor.go`

```go
// Create job runner
jobRunner := NewJobRunner(
    e.client,
    workDir,
    "apply",
    e.namespace,
)
```

**What happens**:
- JobRunner instance created with working directory and action
- This handles Kubernetes Job creation and management

### 10. Run Terraform Job

**File**: `pkg/terraform/executor.go`

```go
// Run the job
outputJSON, err := jobRunner.RunJob(ctx, simple)
```

**File**: `pkg/terraform/job_runner.go`

```go
func (jr *JobRunner) RunJob(ctx context.Context, simple *infrav1alpha1.SimpleResource) ([]byte, error) {
    jobName := fmt.Sprintf("terraform-%s-%s-%s", jr.action, simple.Name, time.Now().Format("20060102-150405"))

    // Create ConfigMap with Terraform files
    configMap, err := jr.createConfigMap(ctx, simple, jobName)

    // Create the Job
    job := jr.buildJob(simple, jobName, configMap.Name)
    jr.client.Create(ctx, job)

    // Wait for job completion
    jr.waitForJobCompletion(ctx, jobName)

    // Get outputs if this was an apply action
    if jr.action == "apply" {
        return jr.getJobOutputs(ctx, jobName)
    }

    return nil, nil
}
```

**What happens**:
- ConfigMap created with rendered `main.tf`
- Job created with Terraform container + output handler sidecar
- Controller waits for Job to complete (polling every 5 seconds)
- Outputs extracted from Secret created by sidecar
- See [Deep Dive: Job Execution](#deep-dive-job-execution)

### 11. Create ConfigMap

**File**: `pkg/terraform/job_runner.go`

```go
func (jr *JobRunner) createConfigMap(ctx context.Context, simple *infrav1alpha1.SimpleResource, jobName string) (*corev1.ConfigMap, error) {
    // Read the rendered Terraform file
    tfContent, err := readFile(jr.workDir + "/main.tf")

    configMap := &corev1.ConfigMap{
        ObjectMeta: metav1.ObjectMeta{
            Name:      jobName + "-config",
            Namespace: jr.namespace,
        },
        Data: map[string]string{
            "main.tf": string(tfContent),
        },
    }

    // Set owner reference for cleanup
    controllerutil.SetControllerReference(simple, configMap, jr.client.Scheme())

    jr.client.Create(ctx, configMap)
    return configMap, nil
}
```

**What happens**:
- ConfigMap created with name like `terraform-apply-simpleresource-sample-20250122-143025-config`
- Contains the rendered `main.tf` file
- Owner reference set so it's deleted when SimpleResource is deleted

### 12. Build Job Specification

**File**: `pkg/terraform/job_runner.go`

```go
func (jr *JobRunner) buildJob(simple *infrav1alpha1.SimpleResource, jobName, configMapName string) *batchv1.Job {
    // Two containers for apply action
    if jr.action == "apply" {
        // Terraform container
        containers = append(containers, corev1.Container{
            Name:    "terraform",
            Image:   "hashicorp/terraform:1.7.0",
            Command: []string{"/bin/sh", "-c",
                "cp /tf-config/* /workspace/ && " +
                "cd /workspace && " +
                "terraform init && " +
                "terraform apply -auto-approve && " +
                "terraform output -json > /outputs/outputs.json && " +
                "touch /outputs/done"},
            VolumeMounts: []corev1.VolumeMount{
                {Name: "terraform-config", MountPath: "/tf-config", ReadOnly: true},
                {Name: "workspace", MountPath: "/workspace"},
                {Name: "outputs", MountPath: "/outputs"},
            },
        })

        // Sidecar container
        outputSecretName := jobName + "-outputs"
        sidecarCommand := fmt.Sprintf(`
            while [ ! -f /outputs/done ]; do
              echo "Waiting for terraform to complete..."
              sleep 2
            done
            kubectl create secret generic %s \
              --from-file=outputs.json=/outputs/outputs.json \
              --namespace=%s
        `, outputSecretName, jr.namespace)

        containers = append(containers, corev1.Container{
            Name:    "output-handler",
            Image:   "bitnami/kubectl:latest",
            Command: []string{"/bin/sh", "-c", sidecarCommand},
            VolumeMounts: []corev1.VolumeMount{
                {Name: "outputs", MountPath: "/outputs"},
            },
        })
    }

    // Build Job
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
            Template: corev1.PodTemplateSpec{
                Spec: corev1.PodSpec{
                    ServiceAccountName: "terraform-executor",
                    RestartPolicy:      corev1.RestartPolicyNever,
                    Containers:         containers,
                    Volumes: []corev1.Volume{
                        {Name: "terraform-config", VolumeSource: corev1.VolumeSource{
                            ConfigMap: &corev1.ConfigMapVolumeSource{
                                LocalObjectReference: corev1.LocalObjectReference{
                                    Name: configMapName,
                                }}}},
                        {Name: "workspace", VolumeSource: corev1.VolumeSource{
                            EmptyDir: &corev1.EmptyDirVolumeSource{}}},
                        {Name: "outputs", VolumeSource: corev1.VolumeSource{
                            EmptyDir: &corev1.EmptyDirVolumeSource{}}},
                    },
                },
            },
        },
    }

    return job
}
```

**What happens**:
- Job spec created with two containers (terraform + output-handler)
- Three volumes mounted:
  - `terraform-config`: ConfigMap with main.tf (read-only)
  - `workspace`: EmptyDir for Terraform to write files (writable)
  - `outputs`: EmptyDir shared between containers
- ServiceAccount set to allow kubectl in sidecar to create Secrets
- See [Deep Dive: Output Extraction](#deep-dive-output-extraction)

### 13. Job Executes

**Inside Pod**:

**Container 1: terraform**
```bash
# Copy files from read-only ConfigMap to writable workspace
cp /tf-config/* /workspace/

# Change to workspace
cd /workspace

# Initialize Terraform
terraform init
# Downloads providers: hashicorp/random, hashicorp/local
# Creates .terraform/ directory and .terraform.lock.hcl

# Apply configuration
terraform apply -auto-approve
# Creates random_string resource
# Creates local_file resource
# Waits 60 seconds (time_sleep resource)
# Generates terraform.tfstate

# Export outputs as JSON
terraform output -json > /outputs/outputs.json
# Output: {"random_value": {"value": "Xy9kL..."}, "file_path": {...}}

# Signal completion
touch /outputs/done

# Container exits successfully
```

**Container 2: output-handler** (runs in parallel)
```bash
# Wait for terraform to complete
while [ ! -f /outputs/done ]; do
  echo "Waiting for terraform to complete..."
  sleep 2
done

# Terraform completed, create Secret with outputs
kubectl create secret generic terraform-apply-simpleresource-sample-20250122-143025-outputs \
  --from-file=outputs.json=/outputs/outputs.json \
  --namespace=default

# Container exits successfully
```

**What happens**:
- Both containers start simultaneously
- Terraform container runs terraform and writes outputs
- Sidecar polls for completion marker
- Once done, sidecar creates Secret with outputs
- Both containers exit, Pod marked complete

### 14. Wait for Job Completion

**File**: `pkg/terraform/job_runner.go`

```go
func (jr *JobRunner) waitForJobCompletion(ctx context.Context, jobName string) error {
    timeout := time.After(30 * time.Minute)
    ticker := time.NewTicker(5 * time.Second)

    for {
        select {
        case <-timeout:
            return fmt.Errorf("job timed out")
        case <-ticker.C:
            job := &batchv1.Job{}
            jr.client.Get(ctx, types.NamespacedName{Name: jobName, Namespace: jr.namespace}, job)

            if job.Status.Succeeded > 0 {
                return nil
            }
            if job.Status.Failed > 0 {
                return fmt.Errorf("job failed")
            }
        }
    }
}
```

**What happens**:
- Controller polls Job status every 5 seconds
- Waits for `Status.Succeeded > 0`
- Times out after 30 minutes
- Returns error if job fails

### 15. Extract Outputs from Secret

**File**: `pkg/terraform/job_runner.go`

```go
func (jr *JobRunner) getJobOutputs(ctx context.Context, jobName string) ([]byte, error) {
    outputSecretName := jobName + "-outputs"
    outputSecret := &corev1.Secret{}

    err := jr.client.Get(ctx, types.NamespacedName{
        Name:      outputSecretName,
        Namespace: jr.namespace,
    }, outputSecret)

    if err != nil {
        if apierrors.IsNotFound(err) {
            return []byte("{}"), nil
        }
        return nil, err
    }

    return outputSecret.Data["outputs.json"], nil
}
```

**What happens**:
- Reads Secret created by sidecar: `terraform-apply-simpleresource-sample-...-outputs`
- Extracts `outputs.json` data
- Returns raw JSON bytes

### 16. Parse Outputs

**File**: `pkg/terraform/executor.go`

```go
// Parse outputs
outputs, err := ParseOutputs(outputJSON)
return outputs, nil
```

**File**: `pkg/terraform/outputs.go`

```go
func ParseOutputs(outputJSON []byte) (map[string]string, error) {
    var tfOutputs map[string]TerraformOutput
    json.Unmarshal(outputJSON, &tfOutputs)

    result := make(map[string]string)
    for key, output := range tfOutputs {
        result[key] = output.Value
    }

    return result, nil
}

type TerraformOutput struct {
    Value string `json:"value"`
    Type  string `json:"type"`
}
```

**Input**:
```json
{
  "random_value": {"value": "Xy9kL2mN4pQ8rT1v", "type": "string"},
  "file_path": {"value": "/tmp/opr8r-default-simpleresource-sample.txt", "type": "string"}
}
```

**Output**:
```go
map[string]string{
    "random_value": "Xy9kL2mN4pQ8rT1v",
    "file_path": "/tmp/opr8r-default-simpleresource-sample.txt",
}
```

### 17. Update Status

**File**: `internal/controller/simpleresource_controller.go`

```go
// Extract specific outputs
randomValue := outputs["random_value"]
filePath := outputs["file_path"]

// Update status
simple.Status.Phase = PhaseReady
simple.Status.Message = "SimpleResource is ready"
simple.Status.RandomValue = randomValue
simple.Status.FilePath = filePath
simple.Status.LastAppliedHash = currentHash

r.Status().Update(ctx, simple)
```

**What happens**:
- Status subresource updated with outputs
- Hash stored for future idempotency checks
- Users can see: `kubectl get simple -o yaml`

### 18. Create/Update User-Facing Secret

**File**: `internal/controller/simpleresource_controller.go`

```go
r.reconcileSecret(ctx, simple, outputs)
```

```go
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
        controllerutil.SetControllerReference(simple, secret, r.Scheme)

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
```

**What happens**:
- Secret created/updated: `simpleresource-sample-simple-outputs`
- Contains all Terraform outputs as individual keys
- Owner reference ensures cascade deletion
- Users can reference this Secret in other Pods

### 19. Reconciliation Complete

```go
logger.Info("Successfully reconciled SimpleResource", "randomValue", randomValue, "filePath", filePath)
return ctrl.Result{RequeueAfter: 5 * time.Minute}, nil
```

**What happens**:
- Success logged
- Reconciliation completes
- Requeue scheduled in 5 minutes for periodic checks
- Resource is now in "Ready" state

## Deep Dive: Template Rendering

### Template File Structure

**File**: `modules/simple/main.tf.tpl`

```hcl
terraform {
  required_version = ">= 1.0"

  backend "local" {
    path = "/tmp/terraform-{{ .Namespace }}-{{ .Name }}.tfstate"
  }

  required_providers {
    random = {
      source  = "hashicorp/random"
      version = "~> 3.0"
    }
  }
}

resource "random_string" "value" {
  length  = {{ .Length }}
  special = lookup(var.custom_vars, "special", "true") == "true"
  upper   = lookup(var.custom_vars, "upper", "true") == "true"
}

variable "custom_vars" {
  type    = map(string)
  default = {
    {{- range $key, $value := .Vars }}
    "{{ $key }}" = "{{ $value }}"
    {{- end }}
  }
}

output "random_value" {
  value = random_string.value.result
}
```

### Template Data

```go
type TemplateData struct {
    Namespace string
    Name      string
    Length    int
    Prefix    string
    Vars      map[string]string
}
```

### Example Rendering

**Input**:
```yaml
spec:
  length: 16
  prefix: "test"
  vars:
    special: "true"
    upper: "false"
```

**Output** (`/tmp/terraform-default-simpleresource-sample/main.tf`):
```hcl
terraform {
  backend "local" {
    path = "/tmp/terraform-default-simpleresource-sample.tfstate"
  }
}

resource "random_string" "value" {
  length  = 16
  special = lookup(var.custom_vars, "special", "true") == "true"
  upper   = lookup(var.custom_vars, "upper", "true") == "true"
}

variable "custom_vars" {
  type    = map(string)
  default = {
    "special" = "true"
    "upper" = "false"
  }
}
```

## Deep Dive: Job Execution

### Job Pod Architecture

```
┌─────────────────────────────────────────────────────┐
│ Pod: terraform-apply-simpleresource-sample-...      │
├─────────────────────────────────────────────────────┤
│                                                     │
│  ┌──────────────────┐    ┌──────────────────┐     │
│  │  Container 1     │    │  Container 2     │     │
│  │  terraform       │    │  output-handler  │     │
│  └──────────────────┘    └──────────────────┘     │
│         │                        │                 │
│         ├── /tf-config (ro) ────┤                 │
│         │   (ConfigMap)          │                 │
│         │                        │                 │
│         ├── /workspace (rw) ─────┤                 │
│         │   (EmptyDir)           │                 │
│         │                        │                 │
│         └── /outputs (rw) ───────┘                 │
│             (EmptyDir, shared)                     │
└─────────────────────────────────────────────────────┘
```

### Volume Mount Strategy

1. **terraform-config** (ConfigMap, read-only):
   - Contains rendered `main.tf`
   - Mounted to `/tf-config`
   - Files copied to workspace for modification

2. **workspace** (EmptyDir, writable):
   - Where Terraform actually runs
   - Can create `.terraform/` directory
   - Can write `.terraform.lock.hcl`
   - Can write `terraform.tfstate`

3. **outputs** (EmptyDir, shared):
   - Both containers can access
   - Terraform writes `outputs.json` and `done` marker
   - Sidecar reads and creates Secret

### Why Copy Files?

ConfigMaps are mounted read-only, but Terraform needs to write:
- `.terraform/` directory (provider cache)
- `.terraform.lock.hcl` (dependency lock file)
- `terraform.tfstate` (state file)
- `.terraform.tfstate.lock.info` (state lock)

By copying to `/workspace`, we give Terraform a writable directory.

## Deep Dive: Output Extraction

### The Sidecar Pattern

```
Timeline:

T+0s:   Pod starts
        ├─ terraform: starts terraform init
        └─ output-handler: starts polling for /outputs/done

T+30s:  terraform: init completes, downloads providers

T+35s:  terraform: apply starts

T+95s:  terraform: apply completes
        terraform: writes /outputs/outputs.json
        terraform: touches /outputs/done
        terraform: exits successfully

T+97s:  output-handler: detects /outputs/done
        output-handler: reads /outputs/outputs.json
        output-handler: creates Secret via kubectl
        output-handler: exits successfully

T+100s: Pod marked as Completed (both containers succeeded)
```

### Two Secrets Pattern

#### Temporary Job Secret
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: terraform-apply-simpleresource-sample-20250122-143025-outputs
  namespace: default
  ownerReferences:
  - apiVersion: infra.opr8r/v1alpha1
    kind: SimpleResource
    name: simpleresource-sample
type: Opaque
data:
  outputs.json: eyJyYW5kb21fdmFsdWUiOnsi...  # Base64 encoded
```

**Purpose**: Transfer outputs from Job to Controller
**Lifetime**: Deleted when Job is cleaned up (TTL 1 hour)
**Created by**: Sidecar container using kubectl

#### Persistent User Secret
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: simpleresource-sample-simple-outputs
  namespace: default
  ownerReferences:
  - apiVersion: infra.opr8r/v1alpha1
    kind: SimpleResource
    name: simpleresource-sample
type: Opaque
data:
  random_value: WHk5a0wybU40cFE4clQxdg==  # Base64 encoded
  file_path: L3RtcC9vcHI4ci1kZWZhdWx0...
  file_content: R2VuZXJhdGVkIGJ5IG9wcjhy...
  length: MTY=
```

**Purpose**: Provide outputs to users and other resources
**Lifetime**: Exists as long as SimpleResource exists
**Created by**: Controller via reconcileSecret()

## State File Management

### Local Backend Configuration

```hcl
backend "local" {
  path = "/tmp/terraform-default-simpleresource-sample.tfstate"
}
```

### State File Location

**During Job Execution**:
- **Inside Pod**: `/workspace/terraform.tfstate`
- **On Node**: `/var/lib/kubelet/pods/<pod-uid>/volumes/kubernetes.io~empty-dir/workspace/terraform.tfstate`

**After Job Completion**:
- State file is **lost** (emptyDir is deleted with Pod)
- This is acceptable for SimpleResource (generates random data)
- For production, use remote backend (S3, GCS, Azure Storage)

### State File Contents

```json
{
  "version": 4,
  "terraform_version": "1.7.0",
  "resources": [
    {
      "mode": "managed",
      "type": "random_string",
      "name": "value",
      "provider": "provider[\"registry.terraform.io/hashicorp/random\"]",
      "instances": [
        {
          "attributes": {
            "id": "Xy9kL2mN4pQ8rT1v",
            "length": 16,
            "result": "Xy9kL2mN4pQ8rT1v",
            "special": true,
            "upper": true
          }
        }
      ]
    }
  ]
}
```

### Why Ephemeral State is OK for SimpleResource

1. **Resource is not real infrastructure**: Just generates random strings
2. **Outputs are extracted and stored**: In Secret and Status
3. **Deletion doesn't need state**: Can just delete files directly
4. **Simplifies learning**: No need for S3 buckets or credentials

### For Production Use

Replace with remote backend:

```hcl
backend "s3" {
  bucket = "my-terraform-state"
  key    = "simpleresource/{{ .Namespace }}/{{ .Name }}.tfstate"
  region = "us-east-1"
}
```

## Filesystem Layouts

### Controller Pod Filesystem

```
/
├── app/
│   └── manager                          # Operator binary
├── modules/
│   └── simple/
│       └── main.tf.tpl                  # Template file
└── tmp/
    └── terraform-default-simpleresource-sample/
        └── main.tf                      # Rendered template
```

### Terraform Job Pod Filesystem

```
/
├── tf-config/                           # ConfigMap mount (read-only)
│   └── main.tf                          # From ConfigMap
├── workspace/                           # EmptyDir (writable)
│   ├── main.tf                          # Copied from /tf-config
│   ├── .terraform/                      # Created by terraform init
│   │   └── providers/
│   │       └── registry.terraform.io/
│   │           └── hashicorp/
│   │               └── random/
│   │                   └── 3.6.0/
│   ├── .terraform.lock.hcl              # Created by terraform init
│   └── terraform.tfstate                # Created by terraform apply
└── outputs/                             # EmptyDir (shared, writable)
    ├── outputs.json                     # Created by terraform output
    └── done                             # Marker file
```

### Project Repository Structure

```
.
├── api/
│   └── v1alpha1/
│       ├── simpleresource_types.go      # CRD definition
│       ├── groupversion_info.go
│       └── zz_generated.deepcopy.go
├── cmd/
│   └── main.go                          # Entry point
├── config/
│   ├── crd/
│   │   └── bases/
│   │       └── infra.opr8r_simpleresources.yaml
│   ├── rbac/
│   │   └── role.yaml
│   ├── manager/
│   │   └── manager.yaml
│   └── samples/
│       └── simple_v1alpha1_simpleresource.yaml
├── docs/
│   ├── EXECUTION_FLOW.md
├── internal/
│   └── controller/
│       └── simpleresource_controller.go
├── modules/
│   └── simple/
│       └── main.tf.tpl
├── pkg/
│   └── terraform/
│       ├── executor.go
│       ├── renderer.go
│       ├── job_runner.go
│       ├── outputs.go
│       └── utils.go
├── go.mod
├── go.sum
├── Dockerfile
├── Makefile
└── README.md
```

## Timeline Example

Complete timeline for a SimpleResource creation:

| Time | Event | Component | Details |
|------|-------|-----------|---------|
| T+0s | kubectl apply | User | Sends resource to API server |
| T+0.1s | Resource created | API Server | Validates and persists to etcd |
| T+0.2s | Watch triggered | Controller | Detects new resource |
| T+0.3s | Reconcile queued | Controller | Worker picks up request |
| T+0.5s | Finalizer added | Controller | Updates resource |
| T+1s | Hash computed | Controller | SHA256 of spec |
| T+1.2s | Check running jobs | Controller | Lists Jobs, finds none |
| T+1.5s | Template rendered | Controller | Creates /tmp/terraform-... |
| T+2s | ConfigMap created | Controller | Contains main.tf |
| T+2.5s | Job created | Controller | Two containers spec |
| T+3s | Pod scheduled | Scheduler | Finds node |
| T+5s | Containers starting | Kubelet | Pulls images |
| T+10s | terraform init | Job Pod | Downloads providers |
| T+35s | terraform apply | Job Pod | Creates resources |
| T+95s | Outputs written | Job Pod | /outputs/outputs.json created |
| T+95.5s | Done marker | Job Pod | /outputs/done created |
| T+97s | Secret created | Sidecar | kubectl create secret |
| T+100s | Containers exit | Job Pod | Both succeeded |
| T+105s | Job complete | Controller | Detects completion |
| T+106s | Outputs extracted | Controller | Reads Secret |
| T+107s | Outputs parsed | Controller | JSON to map |
| T+108s | Status updated | Controller | Phase: Ready |
| T+109s | User Secret created | Controller | With all outputs |
| T+110s | Reconcile complete | Controller | Requeue in 5m |

**Total time**: ~110 seconds (mostly Terraform execution)

### Using Multiple Namespaces

The RBAC is configured with a **ClusterRole** and **ClusterRoleBinding**, so the `terraform-executor` ServiceAccount can create Secrets in **any namespace**.

To create SimpleResources in other namespaces:

```bash
# Create the resource in any namespace
kubectl apply -f config/samples/simple_v1alpha1_simpleresource.yaml -n my-namespace

# The Job will run in my-namespace
# It will use default:terraform-executor ServiceAccount
# ClusterRoleBinding grants permissions cluster-wide
```

**Note**: The ServiceAccount itself is in the `default` namespace, but due to the ClusterRoleBinding, it has permissions to create Secrets in all namespaces. This is simpler than creating a ServiceAccount in each namespace.

## Current Limitations

### 1. Ephemeral State Files
- **Issue**: State stored in emptyDir, lost when Pod deleted
- **Impact**: Can't track drift, can't update existing infrastructure
- **Workaround**: Acceptable for SimpleResource (test data only)
- **Future**: Add S3/GCS backend support

### 2. No Drift Detection
- **Issue**: Changes outside operator not detected
- **Impact**: Manual changes to resources won't trigger reconciliation
- **Workaround**: Periodic reconciliation (every 5 minutes)
- **Future**: Add periodic terraform plan checks

### 3. Job Cleanup
- **Issue**: Failed jobs remain until TTL expires (1 hour)
- **Impact**: Can clutter namespace
- **Workaround**: TTL handles cleanup eventually
- **Future**: Add manual cleanup on status update

### 4. Limited Error Handling
- **Issue**: Generic error messages for job failures
- **Impact**: Hard to debug without checking logs
- **Workaround**: Check job logs manually
- **Future**: Extract and surface terraform errors

### 5. Sequential Job Execution
- **Issue**: Only one job per resource at a time
- **Impact**: Can't parallelize operations
- **Workaround**: Necessary to prevent state conflicts
- **Future**: N/A (this is correct behavior)

### 6. No Terraform Plan Preview
- **Issue**: No dry-run capability
- **Impact**: Can't preview changes before applying
- **Workaround**: Review template manually
- **Future**: Add webhook to generate plan preview

### 7. Hard-coded Timeouts
- **Issue**: 30 minute timeout for all jobs
- **Impact**: Long-running infrastructure might timeout
- **Workaround**: Adjust constant in code
- **Future**: Make timeout configurable per resource


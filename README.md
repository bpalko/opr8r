# opr8r

A simple Kubernetes operator that demonstrates managing Terraform resources via Kubernetes Jobs.

## What is This?

This is a learning-focused operator that manages a **SimpleResource** custom resource. When you create a SimpleResource, the operator:

1. Renders a Terraform template with your spec
2. Creates a Kubernetes Job that runs `terraform init && terraform apply`
3. Waits for the Job to complete
4. Extracts Terraform outputs and stores them in a Secret
5. Updates the resource status

When you delete a SimpleResource, a finalizer ensures `terraform destroy` runs first.

## Why This Exists

This project solves a specific problem: running Terraform as a first-class citizen in Kubernetes, managed through the reconciliation loop. Instead of relying on CI/CD pipelines or external tools, the infrastructure becomes part of your cluster's declarative state.

## Features

- **Simple to understand**: Uses local Terraform providers (no cloud credentials needed)
- **Full lifecycle management**: Create → Update → Delete with finalizers
- **Idempotent**: Tracks spec changes via hash, only re-applies when needed
- **Kubernetes-native**: Outputs stored in Secrets, resources tracked as CRDs
- **Observable**: Full status reporting and phase tracking

## Quick Start

### Prerequisites

- Go 1.22+
- [Kind](https://kind.sigs.k8s.io/) or any Kubernetes cluster
- kubectl

### 1. Create a Kind Cluster

```bash
kind create cluster --name opr8r
```

### 2. Install the CRD

```bash
kubectl apply -f config/crd/bases/infra.opr8r_simpleresources.yaml
```

### 3. Run the Operator

```bash
go run cmd/main.go
```

The operator will automatically create the `terraform-executor` ServiceAccount and RoleBinding in each namespace where you create SimpleResources.

### 4. Create a Resource

In another terminal:

```bash
kubectl apply -f config/samples/simple_v1alpha1_simpleresource.yaml
```

### 5. Watch It Work

```bash
# Watch the resource
kubectl get simple -w

# Watch the Terraform jobs
kubectl get jobs -w

# Check the status
kubectl get simple simpleresource-sample -o yaml
```

You'll see:
- A Terraform Job created
- The Job runs terraform init and apply
- Status updates to "Ready"
- A random string generated and stored

### 6. View Outputs

```bash
# Get the secret with outputs
kubectl get secret simpleresource-sample-simple-outputs -o yaml

# View job logs
kubectl logs -l infra.opr8r/simpleresource=simpleresource-sample
```

## How It Works

### The Reconciliation Loop

```
User creates SimpleResource
    ↓
Controller detects new resource
    ↓
Render Terraform template
    ↓
Create Kubernetes Job (with 2 containers)
    ↓
Job Pod starts
    ├─ Container 1: Runs terraform init && apply, writes outputs
    └─ Container 2: Waits for terraform, creates Secret with outputs
    ↓
Controller waits for Job completion
    ↓
Extract outputs from Secret created by sidecar
    ↓
Parse Terraform outputs to map
    ↓
Update resource status (phase, randomValue, etc.)
    ↓
Create/update user-facing Secret with outputs
    ↓
Requeue for periodic reconciliation
```

### The SimpleResource

```yaml
apiVersion: infra.opr8r/v1alpha1
kind: SimpleResource
metadata:
  name: my-resource
spec:
  length: 16  # Length of random string to generate
  prefix: "test"  # Optional prefix
  vars:  # Optional Terraform variables
    special: "true"
    upper: "true"
```

This generates:
- A random string of specified length
- A local file with the output
- Outputs stored in a Kubernetes Secret

### Status Tracking

The operator updates the resource status through these phases:

- **Pending**: Initial state
- **Applying**: Running terraform apply
- **Ready**: Successfully applied
- **Error**: Something failed
- **Destroying**: Running terraform destroy

## Project Structure

```
opr8r/
├── api/v1alpha1/              # CRD definitions
│   ├── simpleresource_types.go
│   └── groupversion_info.go
├── internal/controller/        # Controller logic
│   └── simpleresource_controller.go
├── pkg/terraform/             # Terraform helpers
│   ├── executor.go           # Orchestrates workflow
│   ├── renderer.go           # Template rendering
│   ├── job_runner.go         # Job management
│   └── outputs.go            # Output parsing
├── modules/simple/            # Terraform template
│   └── main.tf.tpl
├── config/                    # Kubernetes manifests
│   ├── crd/                  # CRD YAML
│   ├── rbac/                 # RBAC rules
│   ├── manager/              # Deployment
│   └── samples/              # Example resources
└── cmd/main.go               # Entry point
```

## Development

```bash
# Format code
make fmt

# Run linter
make vet

# Build binary
make build

# Build Docker image
make docker-build IMG=your-registry/opr8r:tag
```

## Testing Different Scenarios

### Test Spec Changes

Modify the resource to trigger a re-apply:

```bash
kubectl patch simple simpleresource-sample --type='json' \
  -p='[{"op": "replace", "path": "/spec/length", "value": 20}]'
```

The operator will:
1. Detect the spec hash changed
2. Create a new Terraform Job
3. Apply the changes
4. Update status with new outputs

### Test Deletion

```bash
kubectl delete simple simpleresource-sample
```

The operator will:
1. Detect deletion timestamp
2. Update status to "Destroying"
3. Create a terraform destroy Job
4. Wait for completion
5. Remove the finalizer
6. Allow Kubernetes to delete the resource

### Test Multiple Resources

```bash
for i in {1..3}; do
  cat <<EOF | kubectl apply -f -
apiVersion: infra.opr8r/v1alpha1
kind: SimpleResource
metadata:
  name: test-$i
spec:
  length: $((i * 5))
EOF
done
```

Watch them all reconcile in parallel.

## Understanding the Code

### Controller (`internal/controller/simpleresource_controller.go`)

The controller implements three main functions:

1. **Reconcile**: Entry point, routes to normal or delete reconciliation
2. **reconcileNormal**: Handles create/update logic
3. **reconcileDelete**: Handles deletion with finalizer

Key patterns:
- Finalizers prevent deletion until cleanup completes
- Hash tracking enables idempotent reconciliation
- Phase tracking provides observability

### Terraform Helpers (`pkg/terraform/`)

**Executor**: High-level orchestration
- `Apply()`: Render → Run Job → Parse outputs
- `Destroy()`: Render → Run destroy Job → Cleanup

**Renderer**: Template processing
- Reads `.tpl` file
- Substitutes values from spec
- Writes rendered `main.tf`

**JobRunner**: Kubernetes Job management
- Creates ConfigMap with Terraform files
- Builds Job spec with sidecar pattern (see below)
- Waits for completion
- Retrieves outputs from Secret created by sidecar

**Output Extraction**: Uses a sidecar container pattern
- Main container runs Terraform and writes outputs to shared volume
- Sidecar container waits for completion, then creates Secret with outputs
- Controller reads outputs from Secret

### Terraform Template (`modules/simple/main.tf.tpl`)

Uses Go template syntax to inject values:

```hcl
resource "random_string" "value" {
  length = {{ .Length }}
}
```

## Troubleshooting

### Job fails with "ImagePullBackOff"

The Kubernetes cluster can't pull `hashicorp/terraform:1.7.0`. Ensure Docker/containerd has internet access.

### Operator can't create Jobs

Check RBAC permissions:
```bash
kubectl describe clusterrole opr8r-manager-role
```

### Terraform apply fails

Check the Job logs:
```bash
kubectl logs -l infra.opr8r/simpleresource=<name>
```

### Resource stuck in "Applying"

The Job might have failed. Check:
```bash
kubectl get jobs
kubectl describe job <job-name>
```

## References

- [Kubebuilder Book](https://book.kubebuilder.io/)
- [controller-runtime](https://github.com/kubernetes-sigs/controller-runtime)
- [Terraform](https://www.terraform.io/)

## License

Apache License 2.0

I retain all copyright to my contributions to this repository.
package terraform

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/client"

	infrav1alpha1 "github.com/kbpalko/opr8r/api/v1alpha1"
)

// Executor orchestrates the full Terraform workflow
type Executor struct {
	client    client.Client
	renderer  *Renderer
	namespace string
}

// NewExecutor creates a new Executor instance
func NewExecutor(client client.Client, templatePath, namespace string) *Executor {
	return &Executor{
		client:    client,
		renderer:  NewRenderer(templatePath),
		namespace: namespace,
	}
}

// Apply renders and applies Terraform configuration for SimpleResource
func (e *Executor) Apply(ctx context.Context, simple *infrav1alpha1.SimpleResource) (map[string]string, error) {
	// Render the Terraform template
	workDir, err := e.renderer.Render(simple)
	if err != nil {
		return nil, fmt.Errorf("failed to render template: %w", err)
	}

	// Create and run the apply job
	jobRunner := NewJobRunner(e.client, workDir, "apply", e.namespace)
	outputsJSON, err := jobRunner.RunJob(ctx, simple)
	if err != nil {
		return nil, fmt.Errorf("failed to run terraform apply: %w", err)
	}

	// Parse outputs
	outputs, err := ParseOutputs(outputsJSON)
	if err != nil {
		return nil, fmt.Errorf("failed to parse outputs: %w", err)
	}

	return outputs, nil
}

// Destroy runs terraform destroy for SimpleResource
func (e *Executor) Destroy(ctx context.Context, simple *infrav1alpha1.SimpleResource) error {
	// Render the Terraform template (needed to know what to destroy)
	workDir, err := e.renderer.Render(simple)
	if err != nil {
		return fmt.Errorf("failed to render template: %w", err)
	}

	// Create and run the destroy job
	jobRunner := NewJobRunner(e.client, workDir, "destroy", e.namespace)
	_, err = jobRunner.RunJob(ctx, simple)
	if err != nil {
		return fmt.Errorf("failed to run terraform destroy: %w", err)
	}

	// Cleanup working directory
	if err := e.renderer.Cleanup(simple); err != nil {
		// Log but don't fail on cleanup errors
		fmt.Printf("Warning: failed to cleanup working directory: %v\n", err)
	}

	return nil
}

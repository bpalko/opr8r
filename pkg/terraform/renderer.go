package terraform

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/template"

	infrav1alpha1 "github.com/kbpalko/opr8r/api/v1alpha1"
)

// TemplateData holds the data for SimpleResource templates
type TemplateData struct {
	Namespace string
	Name      string
	Length    int
	Prefix    string
	Vars      map[string]string
}

// Renderer handles rendering of Terraform templates
type Renderer struct {
	templatePath string
}

// NewRenderer creates a new Renderer instance
func NewRenderer(templatePath string) *Renderer {
	return &Renderer{
		templatePath: templatePath,
	}
}

// Render renders the Terraform template with the given SimpleResource spec
// Returns the path to the rendered directory
func (r *Renderer) Render(simple *infrav1alpha1.SimpleResource) (string, error) {
	// Create working directory
	workDir := filepath.Join("/tmp", fmt.Sprintf("terraform-%s-%s", simple.Namespace, simple.Name))
	if err := os.MkdirAll(workDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create work directory: %w", err)
	}

	// Prepare template data
	data := TemplateData{
		Namespace: simple.Namespace,
		Name:      simple.Name,
		Length:    simple.Spec.Length,
		Prefix:    simple.Spec.Prefix,
		Vars:      simple.Spec.Vars,
	}

	// Read template file
	tmplContent, err := os.ReadFile(r.templatePath)
	if err != nil {
		return "", fmt.Errorf("failed to read template: %w", err)
	}

	// Parse and execute template
	tmpl, err := template.New("terraform").Parse(string(tmplContent))
	if err != nil {
		return "", fmt.Errorf("failed to parse template: %w", err)
	}

	// Create output file
	outputPath := filepath.Join(workDir, "main.tf")
	outputFile, err := os.Create(outputPath)
	if err != nil {
		return "", fmt.Errorf("failed to create output file: %w", err)
	}
	defer outputFile.Close()

	// Execute template
	if err := tmpl.Execute(outputFile, data); err != nil {
		return "", fmt.Errorf("failed to execute template: %w", err)
	}

	return workDir, nil
}

// ComputeSpecHash computes a hash of the SimpleResource spec for change detection
func ComputeSpecHash(spec *infrav1alpha1.SimpleResourceSpec) string {
	h := sha256.New()
	h.Write([]byte(fmt.Sprintf("%d:%s", spec.Length, spec.Prefix)))

	// Sort map keys to ensure consistent hash calculation
	// Go's map iteration order is randomized, so we must sort
	if len(spec.Vars) > 0 {
		keys := make([]string, 0, len(spec.Vars))
		for k := range spec.Vars {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			h.Write([]byte(fmt.Sprintf("%s=%s", k, spec.Vars[k])))
		}
	}

	return fmt.Sprintf("%x", h.Sum(nil))
}

// Cleanup removes the working directory for SimpleResource
func (r *Renderer) Cleanup(simple *infrav1alpha1.SimpleResource) error {
	workDir := filepath.Join("/tmp", fmt.Sprintf("terraform-%s-%s", simple.Namespace, simple.Name))
	return os.RemoveAll(workDir)
}

# Issues
1. We leave behind lots Configmaps. Each update to the `Simple` resource retemplates the Terraform, therefore creating a new ConfigMap + Job to run the Terraform.
1. Templater logic needs to be more robust. Currently it's only built to template a spec for `Simple` object. Ideally, I want a render object that uses the OpenAPI spec of the resource type we're reconciling (`Simple`, `RDS`, etc.). See `TemplateData{}`.
    - All of the templating is rendered into a `tmp` on the controller file system. This probably won't scale worth a damn.
1. Add error handling for failed output extraction
1. Add timeout to sidecar wait loop
1. Validate outputs.json format before creating Secret
1. Consider compression for large outputs
1. Add metrics/events for output extraction failures
1. Advance the logging so the controller writes when it notices new `Simple` resources and their initial reconcilation steps
1. The `Secret` output creation is slow. I spit out one secret with the `output.json`, and moments later I give a mapped secret. It's slow... and maybe the `output.json` key is fine.
1. Configure S3/GCS for persistent state
1. Validate resources before creation
1. Preview changes before applying
1. Export Prometheus metrics for monitoring
1. Create Kubernetes events for status changes
1. Parse and surface Terraform errors
1. Use standard Kubernetes condition types
1. Support different resource types
1. Ensure vars match template expectations
1. Unit tests for renderer, integration tests for controller
1. After the `Simple` resource is deleted, the `ServiceAccount` and `RoleBinding` hang around in the NS. That ain't good...
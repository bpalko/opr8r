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

package v1

import (
	"context"
	"fmt"

	"github.com/robfig/cron"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	validationutils "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	opr8rv1 "opr8r.io/opr8r/api/v1"
)

// nolint:unused
// log is for logging in this package.
var opr8rlog = logf.Log.WithName("opr8r-resource")

// SetupOpr8rWebhookWithManager registers the webhook for Opr8r in the manager.
func SetupOpr8rWebhookWithManager(mgr ctrl.Manager) error {
	return ctrl.NewWebhookManagedBy(mgr).For(&opr8rv1.Opr8r{}).
		WithValidator(&Opr8rCustomValidator{}).
		WithDefaulter(&Opr8rCustomDefaulter{
			DefaultConcurrencyPolicy:          opr8rv1.AllowConcurrent,
			DefaultSuspend:                    false,
			DefaultSuccessfulJobsHistoryLimit: 3,
			DefaultFailedJobsHistoryLimit:     1,
		}).
		Complete()
}

// TODO(user): EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!

// +kubebuilder:webhook:path=/mutate-opr8r-opr8r-io-v1-opr8r,mutating=true,failurePolicy=fail,sideEffects=None,groups=opr8r.opr8r.io,resources=opr8rs,verbs=create;update,versions=v1,name=mopr8r-v1.kb.io,admissionReviewVersions=v1

// Opr8rCustomDefaulter struct is responsible for setting default values on the custom resource of the
// Kind Opr8r when those are created or updated.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as it is used only for temporary operations and does not need to be deeply copied.
type Opr8rCustomDefaulter struct {
	// Default values for various Opr8r fields
	DefaultConcurrencyPolicy          opr8rv1.ConcurrencyPolicy
	DefaultSuspend                    bool
	DefaultSuccessfulJobsHistoryLimit int32
	DefaultFailedJobsHistoryLimit     int32
}

var _ webhook.CustomDefaulter = &Opr8rCustomDefaulter{}

// Default implements webhook.CustomDefaulter so a webhook will be registered for the Kind Opr8r.
func (d *Opr8rCustomDefaulter) Default(ctx context.Context, obj runtime.Object) error {
	opr8r, ok := obj.(*opr8rv1.Opr8r)

	if !ok {
		return fmt.Errorf("expected an Opr8r object but got %T", obj)
	}
	opr8rlog.Info("Defaulting for Opr8r", "name", opr8r.GetName())

	// Set default values
	d.applyDefaults(opr8r)

	return nil
}

// applyDefaults applies default values to Opr8r fields.
func (d *Opr8rCustomDefaulter) applyDefaults(Opr8r *opr8rv1.Opr8r) {
	if Opr8r.Spec.ConcurrencyPolicy == "" {
		Opr8r.Spec.ConcurrencyPolicy = d.DefaultConcurrencyPolicy
	}
	if Opr8r.Spec.Suspend == nil {
		Opr8r.Spec.Suspend = new(bool)
		*Opr8r.Spec.Suspend = d.DefaultSuspend
	}
	if Opr8r.Spec.SuccessfulJobsHistoryLimit == nil {
		Opr8r.Spec.SuccessfulJobsHistoryLimit = new(int32)
		*Opr8r.Spec.SuccessfulJobsHistoryLimit = d.DefaultSuccessfulJobsHistoryLimit
	}
	if Opr8r.Spec.FailedJobsHistoryLimit == nil {
		Opr8r.Spec.FailedJobsHistoryLimit = new(int32)
		*Opr8r.Spec.FailedJobsHistoryLimit = d.DefaultFailedJobsHistoryLimit
	}
}

// TODO(user): change verbs to "verbs=create;update;delete" if you want to enable deletion validation.
// NOTE: The 'path' attribute must follow a specific pattern and should not be modified directly here.
// Modifying the path for an invalid path can cause API server errors; failing to locate the webhook.
// +kubebuilder:webhook:path=/validate-opr8r-opr8r-io-v1-opr8r,mutating=false,failurePolicy=fail,sideEffects=None,groups=opr8r.opr8r.io,resources=opr8rs,verbs=create;update,versions=v1,name=vopr8r-v1.kb.io,admissionReviewVersions=v1

// Opr8rCustomValidator struct is responsible for validating the Opr8r resource
// when it is created, updated, or deleted.
//
// NOTE: The +kubebuilder:object:generate=false marker prevents controller-gen from generating DeepCopy methods,
// as this struct is used only for temporary operations and does not need to be deeply copied.
type Opr8rCustomValidator struct {
	// TODO(user): Add more fields as needed for validation
}

var _ webhook.CustomValidator = &Opr8rCustomValidator{}

// ValidateCreate implements webhook.CustomValidator so a webhook will be registered for the type Opr8r.
func (v *Opr8rCustomValidator) ValidateCreate(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	opr8r, ok := obj.(*opr8rv1.Opr8r)
	if !ok {
		return nil, fmt.Errorf("expected a Opr8r object but got %T", obj)
	}
	opr8rlog.Info("Validation for Opr8r upon creation", "name", opr8r.GetName())

	// TODO(user): fill in your validation logic upon object creation.

	return nil, validateOpr8r(opr8r)
}

// ValidateUpdate implements webhook.CustomValidator so a webhook will be registered for the type Opr8r.
func (v *Opr8rCustomValidator) ValidateUpdate(ctx context.Context, oldObj, newObj runtime.Object) (admission.Warnings, error) {
	opr8r, ok := newObj.(*opr8rv1.Opr8r)
	if !ok {
		return nil, fmt.Errorf("expected a Opr8r object for the newObj but got %T", newObj)
	}
	opr8rlog.Info("Validation for Opr8r upon update", "name", opr8r.GetName())

	// TODO(user): fill in your validation logic upon object update.

	return nil, nil
}

// ValidateDelete implements webhook.CustomValidator so a webhook will be registered for the type Opr8r.
func (v *Opr8rCustomValidator) ValidateDelete(ctx context.Context, obj runtime.Object) (admission.Warnings, error) {
	opr8r, ok := obj.(*opr8rv1.Opr8r)
	if !ok {
		return nil, fmt.Errorf("expected a Opr8r object but got %T", obj)
	}
	opr8rlog.Info("Validation for Opr8r upon deletion", "name", opr8r.GetName())

	// TODO(user): fill in your validation logic upon object deletion.

	return nil, nil
}

// validateOpr8r validates the fields of a Opr8r object.
func validateOpr8r(Opr8r *opr8rv1.Opr8r) error {
	var allErrs field.ErrorList
	if err := validateOpr8rName(Opr8r); err != nil {
		allErrs = append(allErrs, err)
	}
	if err := validateOpr8rSpec(Opr8r); err != nil {
		allErrs = append(allErrs, err)
	}
	if len(allErrs) == 0 {
		return nil
	}

	return apierrors.NewInvalid(
		schema.GroupKind{Group: "batch.tutorial.kubebuilder.io", Kind: "Opr8r"},
		Opr8r.Name, allErrs)
}

/*
Some fields are declaratively validated by OpenAPI schema.
You can find kubebuilder validation markers (prefixed
with `// +kubebuilder:validation`) in the
[Designing an API](api-design.md) section.
You can find all of the kubebuilder supported markers for
declaring validation by running `controller-gen crd -w`,
or [here](/reference/markers/crd-validation.md).
*/

func validateOpr8rSpec(Opr8r *opr8rv1.Opr8r) *field.Error {
	// The field helpers from the kubernetes API machinery help us return nicely
	// structured validation errors.
	return validateScheduleFormat(
		Opr8r.Spec.Schedule,
		field.NewPath("spec").Child("schedule"))
}

/*
We'll need to validate the [cron](https://en.wikipedia.org/wiki/Cron) schedule
is well-formatted.
*/

func validateScheduleFormat(schedule string, fldPath *field.Path) *field.Error {
	if _, err := cron.ParseStandard(schedule); err != nil {
		return field.Invalid(fldPath, schedule, err.Error())
	}
	return nil
}

/*
Validating the length of a string field can be done declaratively by
the validation schema.

But the `ObjectMeta.Name` field is defined in a shared package under
the apimachinery repo, so we can't declaratively validate it using
the validation schema.
*/

func validateOpr8rName(Opr8r *opr8rv1.Opr8r) *field.Error {
	if len(Opr8r.ObjectMeta.Name) > validationutils.DNS1035LabelMaxLength-11 {
		// The job name length is 63 characters like all Kubernetes objects
		// (which must fit in a DNS subdomain). The Opr8r controller appends
		// a 11-character suffix to the Opr8r (`-$TIMESTAMP`) when creating
		// a job. The job name length limit is 63 characters. Therefore Opr8r
		// names must have length <= 63-11=52. If we don't validate this here,
		// then job creation will fail later.
		return field.Invalid(field.NewPath("metadata").Child("name"), Opr8r.ObjectMeta.Name, "must be no more than 52 characters")
	}
	return nil
}

// +kubebuilder:docs-gen:collapse=Validate object name

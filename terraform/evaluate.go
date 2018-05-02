package terraform

import (
	"fmt"
	"os"
	"sync"

	"github.com/hashicorp/terraform/config/configschema"
	"github.com/hashicorp/terraform/config/hcl2shim"

	"github.com/agext/levenshtein"
	"github.com/hashicorp/hcl2/hcl"
	"github.com/zclconf/go-cty/cty"

	"github.com/hashicorp/terraform/addrs"
	"github.com/hashicorp/terraform/configs"
	"github.com/hashicorp/terraform/lang"
	"github.com/hashicorp/terraform/tfdiags"
)

// Evaluator provides the necessary contextual data for evaluating expressions
// for a particular walk operation.
type Evaluator struct {
	// Operation defines what type of operation this evaluator is being used
	// for.
	Operation walkOperation

	// Meta is contextual metadata about the current operation.
	Meta *ContextMeta

	// Config is the root node in the configuration tree.
	Config *configs.Config

	// ProviderSchemas is a map of schemas for all provider configurations
	// that have been initialized so far. This is mutated concurrently, so
	// it must be accessed only while holding ProvidersLock.
	ProviderSchemas map[string]*ProviderSchema
	ProvidersLock   *sync.Mutex

	// RootVariableValues is a map of values for variables defined in the
	// root module, passed in from external sources. This must not be
	// modified during evaluation.
	RootVariableValues map[string]*InputValue

	// State is the current state. During some operations this structure
	// is mutated concurrently, and so it must be accessed only while holding
	// StateLock.
	State     *State
	StateLock *sync.RWMutex
}

// Scope creates an evaluation scope for the given module path and optional
// resource.
//
// If the "self" argument is nil then the "self" object is not available
// in evaluated expressions. Otherwise, it behaves as an alias for the given
// address.
func (e *Evaluator) Scope(modulePath addrs.ModuleInstance, self addrs.Referenceable, key addrs.InstanceKey) *lang.Scope {
	return &lang.Scope{
		Data: &evaluationStateData{
			Evaluator:   e,
			ModulePath:  modulePath,
			InstanceKey: key,
		},
		SelfAddr: self,
		PureOnly: e.Operation != walkApply && e.Operation != walkDestroy,
		BaseDir:  ".", // Always current working directory for now.
	}
}

// evaluationStateData is an implementation of lang.Data that resolves
// references primarily (but not exclusively) using information from a State.
type evaluationStateData struct {
	Evaluator *Evaluator

	// ModulePath is the path through the dynamic module tree to the module
	// that references will be resolved relative to.
	ModulePath addrs.ModuleInstance

	// InstanceKey is the instance key for the object being evaluated, if any.
	// Set to addrs.NoKey if no object repetition is in progress.
	InstanceKey addrs.InstanceKey
}

// evaluationStateData must implement lang.Data
var _ lang.Data = (*evaluationStateData)(nil)

func (d *evaluationStateData) GetCountAttr(addr addrs.CountAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	switch addr.Name {

	case "index":
		key := d.InstanceKey
		// key might not be set at all (addrs.NoKey) or it might be a string
		// if we're actually in a for_each block, so we'll check first and
		// produce a nice error if this is being used in the wrong context.
		intKey, ok := key.(addrs.IntKey)
		if !ok {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  `Reference to "count" in non-counted context`,
				Detail:   fmt.Sprintf(`The "count" object can be used only in "resource" and "data" blocks, and only when the "count" argument is set.`),
				Subject:  rng.ToHCL().Ptr(),
			})
			return cty.UnknownVal(cty.Number), diags
		}
		return cty.NumberIntVal(int64(intKey)), diags

	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  `Invalid "count" attribute`,
			Detail:   fmt.Sprintf(`The "count" object does not have an attribute named %q. The only supported attribute is count.index, which is the index of each instance of a resource block that has the "count" argument set.`, addr.Name),
			Subject:  rng.ToHCL().Ptr(),
		})
		return cty.DynamicVal, diags
	}
}

func (d *evaluationStateData) GetInputVariable(addrs.InputVariable, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	panic("not yet implemented")
}

func (d *evaluationStateData) GetLocalValue(addr addrs.LocalValue, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	// First we'll make sure the requested value is declared in configuration,
	// so we can produce a nice message if not.
	moduleConfig := d.Evaluator.Config.DescendentForInstance(d.ModulePath)
	if moduleConfig == nil {
		// should never happen, since we can't be evaluating in a module
		// that wasn't mentioned in configuration.
		panic(fmt.Sprintf("local variable read from module %s, which has no configuration", d.ModulePath))
	}

	config := moduleConfig.Module.Locals[addr.Name]
	if config == nil {
		var suggestions []string
		for k := range moduleConfig.Module.Locals {
			suggestions = append(suggestions, k)
		}
		suggestion := nameSuggestion(addr.Name, suggestions)
		if suggestion != "" {
			suggestion = fmt.Sprintf(" Did you mean %q?", suggestion)
		}

		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  `Reference to undeclared local value`,
			Detail:   fmt.Sprintf(`A local value with the name %q has not been declared.%s`, addr.Name, suggestion),
			Subject:  rng.ToHCL().Ptr(),
		})
		return cty.DynamicVal, diags
	}

	// Now we'll retrieve the value from the state, which means we need to hold
	// the state lock.
	d.Evaluator.StateLock.RLock()
	defer d.Evaluator.StateLock.RUnlock()

	ms := d.Evaluator.State.ModuleByPath(d.ModulePath)
	if ms == nil {
		// Not evaluated yet?
		return cty.DynamicVal, diags
	}

	rawV, exists := ms.Locals[addr.Name]
	if !exists {
		// Not evaluated yet?
		return cty.DynamicVal, diags
	}

	// The state structures haven't yet been updated to the new type system,
	// so we'll need to shim here.
	// FIXME: Remove this once ms.Locals is itself a map[string]cty.Value.
	val := hcl2shim.HCL2ValueFromConfigValue(rawV)

	return val, diags
}

func (d *evaluationStateData) GetModuleInstance(addrs.ModuleCallInstance, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	panic("not yet implemented")
}

func (d *evaluationStateData) GetModuleInstanceOutput(addrs.ModuleCallOutput, tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	panic("not yet implemented")
}

func (d *evaluationStateData) GetPathAttr(addr addrs.PathAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	switch addr.Name {

	case "cwd":
		wd, err := os.Getwd()
		if err != nil {
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  `Failed to get working directory`,
				Detail:   fmt.Sprintf(`The value for path.cwd cannot be determined due to a system error: %s`, err),
				Subject:  rng.ToHCL().Ptr(),
			})
			return cty.DynamicVal, diags
		}
		return cty.StringVal(wd), diags

	case "module":
		moduleConfig := d.Evaluator.Config.DescendentForInstance(d.ModulePath)
		if moduleConfig == nil {
			// should never happen, since we can't be evaluating in a module
			// that wasn't mentioned in configuration.
			panic(fmt.Sprintf("module.path read from module %s, which has no configuration", d.ModulePath))
		}
		sourceDir := moduleConfig.Module.SourceDir
		return cty.StringVal(sourceDir), diags

	case "root":
		sourceDir := d.Evaluator.Config.Module.SourceDir
		return cty.StringVal(sourceDir), diags

	default:
		suggestion := nameSuggestion(addr.Name, []string{"cwd", "module", "root"})
		if suggestion != "" {
			suggestion = fmt.Sprintf(" Did you mean %q?", suggestion)
		}
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  `Invalid "path" attribute`,
			Detail:   fmt.Sprintf(`The "path" object does not have an attribute named %q.%s`, addr.Name, suggestion),
			Subject:  rng.ToHCL().Ptr(),
		})
		return cty.DynamicVal, diags
	}
}

func (d *evaluationStateData) GetResourceInstance(addr addrs.ResourceInstance, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	// Although we are giving a ResourceInstance address here, if it has
	// a key of addrs.NoKey then it might actually be a request for all of
	// the instances of a particular resource. The reference resolver can't
	// resolve the ambiguity itself, so we must do it in here.

	// We need to shim our address to the legacy form still used in the state structs.
	addrKey := NewLegacyResourceInstanceAddress(addr.Absolute(d.ModulePath)).stateId()

	// We'll get the values for the instance(s) from state, so we'll need a read lock.
	d.Evaluator.StateLock.RLock()
	defer d.Evaluator.StateLock.RUnlock()

	ms := d.Evaluator.State.ModuleByPath(d.ModulePath)
	if ms == nil {
		// Not evaluated yet?
		return cty.DynamicVal, diags
	}

	// Note that the state structs currently have confusing legacy names:
	// ResourceState is actually the state for what we call an "instance"
	// elsewhere, and then InstanceState is the state for a particular _phase_
	// of that instance (primary vs. deposed). This should be addressed when
	// we revise the state structs to natively support the HCL type system.
	rs := ms.Resources[addrKey]

	// If we have an exact match for the requested instance and it has non-nil
	// primary data then we'll use it directly. This is the easy path.
	if rs != nil && rs.Primary != nil {
		providerAddr, err := rs.ProviderAddr()
		if err != nil {
			// This indicates corruption of or tampering with the state file
			diags = diags.Append(&hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  `Invalid provider address in state`,
				Detail:   fmt.Sprintf("The state for the referenced resource refers to a syntactically-invalid provider address %q. This can occur if the state data is incorrectly edited by hand.", rs.Provider),
				Subject:  rng.ToHCL().Ptr(),
			})
			return cty.DynamicVal, diags
		}
		return d.getResourceInstanceSingle(addr, rng, rs.Primary, providerAddr)
	}

	// If we get down here then we might have a request for the list of all
	// instances of a particular resource, but only if we have a no-key address.
	// If we have a _keyed_ address then instead it's a single instance that
	// isn't evaluated yet.
	if addr.Key != addrs.NoKey {
		return d.getResourceInstancePending(addr, rng)
	}

	return d.getResourceInstancesAll(addr.ContainingResource(), rng, ms)
}

func (d *evaluationStateData) getResourceInstanceSingle(addr addrs.ResourceInstance, rng tfdiags.SourceRange, is *InstanceState, providerAddr addrs.AbsProviderConfig) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	// To properly decode the "flatmap"-based values from the state, we need
	// to know the resource's schema, which we should already have cached
	// from when the provider was initialized.
	d.Evaluator.ProvidersLock.Lock()
	defer d.Evaluator.ProvidersLock.Unlock()

	schema := d.getResourceSchema(addr.ContainingResource(), providerAddr)
	if schema == nil {
		// This shouldn't happen, since validation before we get here should've
		// taken care of it, but we'll show a reasonable error message anyway.
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  `Missing resource type schema`,
			Detail:   fmt.Sprintf("No schema is available for %s in %s. This is a bug in Terraform and should be reported.", addr, providerAddr),
			Subject:  rng.ToHCL().Ptr(),
		})
		return cty.DynamicVal, diags
	}

	// TODO: Finish this
	return cty.DynamicVal, diags

}

func (d *evaluationStateData) getResourceInstancePending(addr addrs.ResourceInstance, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	// We'd ideally like to return a properly-typed unknown value here, in
	// order to give the type checker maximum information to detect type
	// mismatches even if concrete values aren't yet known.
	//
	// To do this we need to know the resource's schema, which we should
	// already have cached from when the provider was initialized.  However, we
	// first need to look in configuration to find out which provider address
	// will be responsible for creating this.
	moduleConfig := d.Evaluator.Config.DescendentForInstance(d.ModulePath)
	if moduleConfig == nil {
		// should never happen, since we can't be evaluating in a module
		// that wasn't mentioned in configuration.
		panic(fmt.Sprintf("reference to instance from module %s, which has no configuration", d.ModulePath))
	}

	// Everything after here is best-effort: if we can't gather enough
	// information to return a typed value then we'll give up and return an
	// entirely-untyped value, assuming that we're in a special situation
	// such as accessing an orphaned resource, which should get error-checked
	// elsewhere.
	rc := moduleConfig.Module.ResourceByAddr(addr.ContainingResource())
	if rc == nil {
		return cty.DynamicVal, diags
	}
	providerAddr := rc.ProviderConfigAddr().Absolute(d.ModulePath)
	schema := d.getResourceSchema(addr.ContainingResource(), providerAddr)
	if schema == nil {
		return cty.DynamicVal, diags
	}

	return cty.UnknownVal(schema.ImpliedType()), diags
}

func (d *evaluationStateData) getResourceInstancesAll(addr addrs.Resource, rng tfdiags.SourceRange, ms *ModuleState) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	// TODO: Finish this
	return cty.DynamicVal, diags
}

func (d *evaluationStateData) getResourceSchema(addr addrs.Resource, providerAddr addrs.AbsProviderConfig) *configschema.Block {
	d.Evaluator.ProvidersLock.Lock()
	defer d.Evaluator.ProvidersLock.Unlock()

	providerSchema := d.Evaluator.ProviderSchemas[providerAddr.String()]
	if providerSchema == nil {
		return nil
	}

	var schema *configschema.Block
	switch addr.Mode {
	case addrs.ManagedResourceMode:
		schema = providerSchema.ResourceTypes[addr.Type]
	case addrs.DataResourceMode:
		schema = providerSchema.DataSources[addr.Type]
	}
	return schema
}

func (d *evaluationStateData) GetTerraformAttr(addr addrs.TerraformAttr, rng tfdiags.SourceRange) (cty.Value, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics
	switch addr.Name {

	case "workspace":
		workspaceName := d.Evaluator.Meta.Env
		return cty.StringVal(workspaceName), diags

	case "env":
		// Prior to Terraform 0.12 there was an attribute "env", which was
		// an alias name for "workspace". This was deprecated and is now
		// removed.
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  `Invalid "terraform" attribute`,
			Detail:   `The terraform.env attribute was deprecated in v0.10 and removed in v0.12. The "state environment" concept was rename to "workspace" in v0.12, and so the workspace name can now be accessed using the terraform.workspace attribute.`,
			Subject:  rng.ToHCL().Ptr(),
		})
		return cty.DynamicVal, diags

	default:
		diags = diags.Append(&hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  `Invalid "terraform" attribute`,
			Detail:   fmt.Sprintf(`The "terraform" object does not have an attribute named %q. The only supported attribute is terraform.workspace, the name of the currently-selected workspace.`, addr.Name),
			Subject:  rng.ToHCL().Ptr(),
		})
		return cty.DynamicVal, diags
	}
}

// nameSuggestion tries to find a name from the given slice of suggested names
// that is close to the given name and returns it if found. If no suggestion
// is close enough, returns the empty string.
//
// The suggestions are tried in order, so earlier suggestions take precedence
// if the given string is similar to two or more suggestions.
//
// This function is intended to be used with a relatively-small number of
// suggestions. It's not optimized for hundreds or thousands of them.
func nameSuggestion(given string, suggestions []string) string {
	for _, suggestion := range suggestions {
		dist := levenshtein.Distance(given, suggestion, nil)
		if dist < 3 { // threshold determined experimentally
			return suggestion
		}
	}
	return ""
}

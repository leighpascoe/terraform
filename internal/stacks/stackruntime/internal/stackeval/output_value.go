package stackeval

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform/internal/promising"
	"github.com/hashicorp/terraform/internal/stacks/stackaddrs"
	"github.com/hashicorp/terraform/internal/stacks/stackconfig"
	"github.com/hashicorp/terraform/internal/stacks/stackconfig/typeexpr"
	"github.com/hashicorp/terraform/internal/stacks/stackplan"
	"github.com/hashicorp/terraform/internal/tfdiags"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
)

// OutputValue represents an input variable belonging to a [Stack].
type OutputValue struct {
	addr stackaddrs.AbsOutputValue

	main *Main

	resultValue perEvalPhase[promising.Once[withDiagnostics[cty.Value]]]
}

var _ Plannable = (*OutputValue)(nil)

func newOutputValue(main *Main, addr stackaddrs.AbsOutputValue) *OutputValue {
	return &OutputValue{
		addr: addr,
		main: main,
	}
}

func (v *OutputValue) Addr() stackaddrs.AbsOutputValue {
	return v.addr
}

func (v *OutputValue) Config(ctx context.Context) *OutputValueConfig {
	configAddr := stackaddrs.ConfigForAbs(v.Addr())
	stackConfig := v.main.StackConfig(ctx, configAddr.Stack)
	if stackConfig == nil {
		return nil
	}
	return stackConfig.OutputValue(ctx, configAddr.Item)
}

func (v *OutputValue) Stack(ctx context.Context, phase EvalPhase) *Stack {
	return v.main.Stack(ctx, v.Addr().Stack, phase)
}

func (v *OutputValue) Declaration(ctx context.Context) *stackconfig.OutputValue {
	cfg := v.Config(ctx)
	if cfg == nil {
		return nil
	}
	return cfg.Declaration(ctx)
}

func (v *OutputValue) ResultType(ctx context.Context) (cty.Type, *typeexpr.Defaults) {
	decl := v.Declaration(ctx)
	if decl == nil {
		// If we get here then something odd must be going on, but
		// we don't have enough context to guess why so we'll just
		// return, in effect, "I don't know".
		return cty.DynamicPseudoType, &typeexpr.Defaults{
			Type: cty.DynamicPseudoType,
		}
	}
	return decl.Type.Constraint, decl.Type.Defaults
}

func (v *OutputValue) ResultValue(ctx context.Context, phase EvalPhase) cty.Value {
	val, _ := v.CheckResultValue(ctx, phase)
	return val
}

func (v *OutputValue) CheckResultValue(ctx context.Context, phase EvalPhase) (cty.Value, tfdiags.Diagnostics) {
	return withCtyDynamicValPlaceholder(doOnceWithDiags(
		ctx, v.resultValue.For(phase), v.main,
		func(ctx context.Context) (cty.Value, tfdiags.Diagnostics) {
			var diags tfdiags.Diagnostics

			ty, defs := v.ResultType(ctx)

			stack := v.Stack(ctx, phase)
			if stack == nil {
				// If we're in a stack whose expansion isn't known yet then
				// we'll return an unknown value placeholder so that
				// downstreams can at least do type-checking.
				return cty.UnknownVal(ty), diags
			}
			result, moreDiags := EvalExprAndEvalContext(ctx, v.Declaration(ctx).Value, phase, stack)
			diags = diags.Append(moreDiags)
			if moreDiags.HasErrors() {
				return cty.UnknownVal(ty), diags
			}

			var err error
			if defs != nil {
				result.Value = defs.Apply(result.Value)
			}
			result.Value, err = convert.Convert(result.Value, ty)
			if err != nil {
				diags = diags.Append(result.Diagnostic(
					tfdiags.Error,
					"Invalid output value result",
					fmt.Sprintf("Unsuitable value for output %q: %s.", v.Addr().Item.Name, tfdiags.FormatError(err)),
				))
				return cty.UnknownVal(ty), diags
			}

			return result.Value, diags
		},
	))
}

// PlanChanges implements Plannable as a plan-time validation of the variable's
// declaration and of the caller's definition of the variable.
func (v *OutputValue) PlanChanges(ctx context.Context) ([]stackplan.PlannedChange, tfdiags.Diagnostics) {
	var diags tfdiags.Diagnostics

	_, moreDiags := v.CheckResultValue(ctx, PlanPhase)
	diags = diags.Append(moreDiags)

	return nil, diags
}

func (v *OutputValue) tracingName() string {
	return v.Addr().String()
}

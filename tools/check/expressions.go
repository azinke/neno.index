package main

import (
	"encoding/json"
	"fmt"

	neno "github.com/azinke/neno/api"
	"github.com/azinke/neno/api/core"
)

// The public API verifies these fixtures against its pinned core-tools-1
// registry. The synthetic execution identity is used only for validation;
// it is never installed, persisted, or used to authorize an action.
const catInput = `{"type":"object","properties":{"scope":{"type":"string","maxLength":63,"minLength":1},"path":{"type":"string","maxLength":4096,"minLength":1},"max-bytes":{"type":"integer","minimum":1,"maximum":65536}},"required":["scope","path","max-bytes"],"additionalProperties":false}`
const catOutput = `{"type":"object","properties":{"text":{"type":"string","maxLength":65536},"size":{"type":"integer","minimum":0,"maximum":65536}},"required":["text","size"],"additionalProperties":false}`

type workflowStep struct {
	ID     string   `json:"id"`
	Needs  []string `json:"needs"`
	When   string   `json:"when"`
	Assert string   `json:"assert"`
	Action *struct {
		Uses      string `json:"uses"`
		Arguments struct {
			Expr string `json:"expr"`
		} `json:"arguments"`
	} `json:"action"`
	Model *struct {
		Context struct {
			Expr string `json:"expr"`
		} `json:"context"`
		OutputSchema json.RawMessage `json:"output-schema"`
	} `json:"model"`
}

func contract(data []byte) (neno.Contract, error) {
	value, err := core.Parse(data, core.DefaultLimits())
	if err != nil {
		return neno.Contract{}, err
	}
	return neno.CompileContract(value, core.DefaultLimits())
}

func workflowScope(source neno.Source) (neno.ExpressionScope, []workflowStep, error) {
	var resource struct {
		Spec struct {
			Inputs json.RawMessage `json:"inputs"`
			Steps  []workflowStep  `json:"steps"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(source.Value().Bytes(), &resource); err != nil {
		return neno.ExpressionScope{}, nil, err
	}
	inputs, err := contract(resource.Spec.Inputs)
	if err != nil {
		return neno.ExpressionScope{}, nil, err
	}
	boolean, _ := contract([]byte(`{"type":"boolean"}`))
	scope := neno.ExpressionScope{Inputs: inputs, Steps: map[string]neno.ExpressionStep{}}
	for _, step := range resource.Spec.Steps {
		result := boolean
		if step.Model != nil {
			result, err = contract(step.Model.OutputSchema)
		} else if step.Action != nil {
			if step.Action.Uses != "core/cat" {
				return scope, nil, fmt.Errorf("add a verified action contract fixture for %s", step.Action.Uses)
			}
			fixture := `{"id":"core/cat","input-schema":` + catInput + `,"output-schema":` + catOutput + `,"effect":"none","execution":{"adapter":"validation-only","digest":"` + core.Hash([]byte("validation-only")).String() + `","isolation-profile":"validation-only","isolation-digest":"` + core.Hash([]byte("validation-only")).String() + `"},"timeout-seconds":1,"max-result-bytes":1048576,"credentials":[],"network":[]}`
			value, parseErr := core.Parse([]byte(fixture), core.DefaultLimits())
			if parseErr != nil {
				return scope, nil, parseErr
			}
			if _, err = neno.ValidateCoreToolDescriptor(value, core.DefaultLimits()); err != nil {
				return scope, nil, err
			}
			result, err = contract([]byte(catOutput))
		} else if step.Assert == "" {
			return scope, nil, fmt.Errorf("add expression checking for step %s", step.ID)
		}
		if err != nil {
			return scope, nil, err
		}
		scope.Steps[step.ID] = neno.ExpressionStep{Result: result, MaySkip: step.When != ""}
	}
	return scope, resource.Spec.Steps, nil
}

func checkWorkflowExpressions(source neno.Source) error {
	scope, steps, err := workflowScope(source)
	if err != nil {
		return err
	}
	for _, step := range steps {
		scope.Location, scope.Needs = neno.StepExpression, step.Needs
		scope.Guard, scope.RequireBoolean = "", true
		if step.When != "" {
			if _, err := neno.CompileExpression(step.When, scope); err != nil {
				return fmt.Errorf("%s when: %w", step.ID, err)
			}
		}
		scope.Guard = step.When
		body := step.Assert
		if step.Model != nil {
			body, scope.RequireBoolean = step.Model.Context.Expr, false
		}
		if step.Action != nil {
			body, scope.RequireBoolean = step.Action.Arguments.Expr, false
		}
		if body != "" {
			if _, err := neno.CompileExpression(body, scope); err != nil {
				return fmt.Errorf("%s expression: %w", step.ID, err)
			}
		}
	}
	scope.Location, scope.Guard, scope.RequireBoolean = neno.OutputExpression, "", false
	value, _ := neno.LookupPointer(source.Value(), "/spec/outputs/value/expr")
	var output string
	if value.Bytes() != nil {
		_ = json.Unmarshal(value.Bytes(), &output)
	}
	if output != "" {
		_, err = neno.CompileExpression(output, scope)
	}
	return err
}

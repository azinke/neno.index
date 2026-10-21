package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	neno "github.com/azinke/neno/api"
	"github.com/azinke/neno/api/core"
)

func testPaths(t *testing.T) (string, string) {
	t.Helper()
	root, catalog := os.Getenv("NENO_REGISTRY_ROOT"), os.Getenv("NENO_CATALOG_ROOT")
	if root == "" || catalog == "" {
		t.Fatal("run make test with NENO_SOURCE and NENO_CATALOG pointing to the selected checkouts")
	}
	return root, catalog
}

func TestRegistry(t *testing.T) {
	root, catalog := testPaths(t)
	if err := check(context.Background(), root, catalog); err != nil {
		t.Fatal(err)
	}
}

func TestRejectInvalidRegistry(t *testing.T) {
	root, catalog := testPaths(t)
	for _, test := range []struct {
		name, file, from, to, want string
	}{
		{"missing export", "package.yml", "./agents/planner.agent.yml", "./agents/missing.agent.yml", "missing"},
		{"wrong instruction kind", "agents/planner.agent.yml", "../instructions/planning.instruction.yml", "../agents/reviewer.agent.yml", "kind"},
		{"unknown Agent model", "agents/writer.agent.yml", "openai/luna/default", "openai/missing/default", "absent from the selected Catalog"},
		{"step widens models", "workflows/review-change.workflow.yml", "        models: [openai/sol/allrounder]", "        models: [openai/missing/default]", "outside Workflow capabilities"},
		{"step omits Agent model", "workflows/review-change.workflow.yml", "        models: [openai/sol/allrounder]", "        models: []", "missing from step capabilities"},
		{"step widens actions", "workflows/review-change.workflow.yml", "        actions: []", "        actions: [core/ls]", "outside Workflow capabilities"},
		{"undeclared Agent tool", "agents/reviewer.agent.yml", "  tools: []", "  tools: [core/ls]", "missing from step capabilities"},
		{"invalid sample input", "examples/plan-inputs.json", `"request":`, `"unexpected":`, "contract-value"},
		{"invalid sample output", "examples/review-output.json", `"medium"`, `"urgent"`, "contract-value"},
		{"Workflow cycle", "workflows/plan-change.workflow.yml", "needs: [check-input, read-evidence, check-evidence]", "needs: [plan]", "cycle"},
		{"invisible evidence result", "workflows/plan-change.workflow.yml", "needs: [check-input, read-evidence, check-evidence]", "needs: [check-input, check-evidence]", "field"},
		{"unguarded evidence input", "workflows/plan-change.workflow.yml", "when: has(inputs.evidence)", "when: 'true'", "optional"},
		{"unknown result field", "workflows/plan-change.workflow.yml", "result.goal", "result.nonexistent", "field"},
		{"undeclared direct action", "workflows/plan-change.workflow.yml", "uses: core/cat", "uses: core/ls", "missing from step capabilities"},
		{"unsafe sample path", "examples/checks.json", "examples/plan-inputs.json", "../plan-inputs.json", "unsafe or empty path"},
		{"unknown sample field", "examples/checks.json", `"workflow":`, `"workflo":`, "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			directory := copyRegistry(t, root)
			path := filepath.Join(directory, test.file)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(data), test.from) {
				t.Fatal("mutation no longer matches fixture")
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(data), test.from, test.to, 1)), 0644); err != nil {
				t.Fatal(err)
			}
			err = check(context.Background(), directory, catalog)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v; want rejection containing %q", err, test.want)
			}
		})
	}
}

func TestWorkflowChecks(t *testing.T) {
	root, _ := testPaths(t)
	_, documents, err := readYAML(root, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ workflow, prefix, model string }{
		{"plan-change", "plan", "plan"}, {"review-change", "review", "review"}, {"design-tests", "test", "design"},
	} {
		t.Run(fixture.workflow, func(t *testing.T) {
			scope, steps, err := workflowScope(documents["workflows/"+fixture.workflow+".workflow.yml"])
			if err != nil {
				t.Fatal(err)
			}
			inputValue, err := readJSON(root, "examples/"+fixture.prefix+"-inputs.json")
			if err != nil {
				t.Fatal(err)
			}
			outputValue, err := readJSON(root, "examples/"+fixture.prefix+"-output.json")
			if err != nil {
				t.Fatal(err)
			}
			var inputs, output map[string]any
			_ = json.Unmarshal(inputValue.Bytes(), &inputs)
			_ = json.Unmarshal(outputValue.Bytes(), &output)
			records := map[string]any{
				"check-input":    map[string]any{"status": "succeeded", "result": true},
				"read-evidence":  map[string]any{"status": "skipped", "result": nil},
				"check-evidence": map[string]any{"status": "succeeded", "result": true},
				fixture.model:    map[string]any{"status": "succeeded", "result": output},
			}
			evaluate := func(id string) neno.ExpressionEvaluation {
				t.Helper()
				for _, step := range steps {
					if step.ID != id {
						continue
					}
					scope.Needs, scope.Guard, scope.RequireBoolean = step.Needs, step.When, true
					body := step.Assert
					if step.Action != nil {
						body, scope.RequireBoolean = step.Action.Arguments.Expr, false
					}
					if step.Model != nil {
						body, scope.RequireBoolean = step.Model.Context.Expr, false
					}
					expression, err := neno.CompileExpression(body, scope)
					if err != nil {
						t.Fatal(err)
					}
					visible := map[string]any{}
					for _, need := range step.Needs {
						visible[need] = records[need]
					}
					data, _ := json.Marshal(map[string]any{"inputs": inputs, "ctx": map[string]any{}, "steps": visible})
					env, err := core.Parse(data, core.DefaultLimits())
					if err != nil {
						t.Fatal(err)
					}
					record, err := expression.Evaluate(context.Background(), env, neno.DefaultExpressionLimits())
					if err != nil {
						t.Fatal(err)
					}
					return record
				}
				t.Fatalf("missing step %s", id)
				return neno.ExpressionEvaluation{}
			}
			assertBoolean := func(id string, want bool) {
				t.Helper()
				record := evaluate(id)
				if record.Skipped || record.Value == nil || string(record.Value.Bytes()) != fmt.Sprint(want) {
					t.Fatalf("%s: got %+v; want %v", id, record, want)
				}
			}
			if !evaluate("read-evidence").Skipped {
				t.Fatal("read must skip without optional evidence")
			}
			assertBoolean("check-evidence", true)
			// A skipped read must never be dereferenced by the model context.
			if evaluate(fixture.model).Value == nil {
				t.Fatal("missing model context")
			}
			inputs["evidence"] = map[string]any{"scope": "project", "path": "source.txt"}
			if evaluate("read-evidence").Skipped {
				t.Fatal("requested read was skipped")
			}
			records["read-evidence"] = map[string]any{"status": "succeeded", "result": map[string]any{"text": "", "size": 0}}
			assertBoolean("check-evidence", false)
			records["read-evidence"] = map[string]any{"status": "succeeded", "result": map[string]any{"text": "source evidence", "size": 15}}
			assertBoolean("check-evidence", true)
			var modelContext map[string]any
			_ = json.Unmarshal(evaluate(fixture.model).Value.Bytes(), &modelContext)
			if modelContext["evidence"] != "source evidence" {
				t.Fatal("tool evidence did not reach model")
			}
			assertBoolean("check-result", true)
			switch fixture.model {
			case "plan":
				output["validation"] = []any{}
			case "design":
				output["cases"] = []any{}
			case "review":
				output["findings"] = []any{}
				assertBoolean("check-result", true) // A clean review is valid.
				output["findings"] = []any{map[string]any{"severity": "high", "location": "", "issue": "Defect", "recommendation": ""}}
			}
			assertBoolean("check-result", false)
		})
	}
}

func TestEveryWorkflowRequiresSample(t *testing.T) {
	root, catalog := testPaths(t)
	directory := copyRegistry(t, root)
	path := filepath.Join(directory, "examples/checks.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var samples []sample
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(samples[:len(samples)-1])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	if err := check(context.Background(), directory, catalog); err == nil || !strings.Contains(err.Error(), "has no sample") {
		t.Fatalf("got %v; want missing sample rejection", err)
	}
}

func copyRegistry(t *testing.T, root string) string {
	t.Helper()
	destination := t.TempDir()
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative != "." && strings.HasPrefix(entry.Name(), ".") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(destination, relative), 0755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(destination, relative), data, 0644)
	}); err != nil {
		t.Fatal(err)
	}
	return destination
}

// Validate the registry against the public API of the selected Neno checkout.
// Run from that checkout so its module supplies the implementation and dependencies.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	neno "github.com/azinke/neno/api"
	"github.com/azinke/neno/api/core"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 3 {
		return fmt.Errorf("usage: checker PACKAGE_DIRECTORY CATALOG_DIRECTORY (run via make validate)")
	}
	return check(context.Background(), os.Args[1], os.Args[2])
}

func check(ctx context.Context, directory, catalogDirectory string) error {
	snapshot, documents, err := readYAML(directory, true)
	if err != nil {
		return err
	}
	limits := neno.SourceLimits{
		Values: core.DefaultLimits(), MaxResources: 1000, MaxBindings: 10000,
		MaxFiles: 1000, MaxSourceBytes: 16 << 20,
	}
	syntax := func(text string) error {
		_, err := neno.CheckExpression(text)
		return err
	}
	root, ok := documents["package.yml"]
	if !ok {
		return fmt.Errorf("package.yml is missing")
	}
	kind, _ := root.Kind()
	if kind != neno.PackageSource {
		return fmt.Errorf("package.yml must be a Package")
	}
	// Discovery validates every export and follows Agent instructions and Workflow
	// references, detecting missing files, wrong resource kinds, and cycles.
	closure, err := neno.DiscoverSnapshotClosure(ctx, snapshot, "package.yml",
		neno.LockSource{Kind: neno.WorkspaceLockSource}, nil, limits, syntax)
	if err != nil {
		return err
	}
	reached := map[string]bool{"package.yml": true}
	for _, binding := range closure.Bindings {
		reached[binding.Target.Path] = true
	}
	allBindings := append([]neno.LockBinding(nil), closure.Bindings...)
	for _, name := range documentNames(documents) {
		if strings.HasPrefix(name, "examples/") {
			exampleClosure, err := neno.DiscoverSnapshotClosure(ctx, snapshot, name,
				neno.LockSource{Kind: neno.WorkspaceLockSource}, nil, limits, syntax)
			if err != nil {
				return err
			}
			allBindings = append(allBindings, exampleClosure.Bindings...)
		} else if !reached[name] {
			return fmt.Errorf("%s is not reachable from package.yml; export it or reference it", name)
		}
	}

	catalogSnapshot, _, err := readYAML(catalogDirectory, false)
	if err != nil {
		return err
	}
	origin := neno.CatalogLockSource{Kind: neno.WorkspaceCatalogLockSource}
	catalogRoot, err := neno.NewCatalogLockRoot(catalogSnapshot, "catalog.yml", []neno.CatalogLockSource{origin})
	if err != nil {
		return err
	}
	catalog, err := neno.ComposeCatalog(ctx, neno.CatalogCompositionSpec{
		Entry:       neno.CatalogAddress{Root: catalogRoot.ID(), Path: "catalog.yml"},
		Sources:     []neno.SourceTree{{Root: catalogRoot.ID(), Snapshot: catalogSnapshot}},
		RootSources: []neno.CatalogLockSource{origin},
	}, neno.DefaultCatalogCompositionLimits())
	if err != nil {
		return err
	}
	models := map[string]bool{}
	for _, model := range catalog.Models() {
		models[model.ID] = true
	}
	for _, name := range documentNames(documents) {
		if err := checkModels(name, documents[name], documents, allBindings, models); err != nil {
			return err
		}
	}
	samples, err := readSamples(directory)
	if err != nil {
		return err
	}
	covered := map[string]bool{}
	for _, sample := range samples {
		if err := checkSample(ctx, directory, sample, snapshot, documents, catalog, limits, syntax); err != nil {
			return fmt.Errorf("%s: %w", sample.Workflow, err)
		}
		covered[sample.Workflow] = true
	}
	for _, name := range documentNames(documents) {
		kind, _ := documents[name].Kind()
		if kind == neno.WorkflowSource && !covered[name] {
			return fmt.Errorf("%s has no sample in examples/checks.json", name)
		}
	}
	var manifest struct {
		Spec struct {
			Exports map[string]string `json:"exports"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(root.Value().Bytes(), &manifest); err != nil {
		return err
	}
	fmt.Printf("Valid registry: %d exports, %d source documents, %d input/output examples with static Workflow checks; %d Catalog models.\n",
		len(manifest.Spec.Exports), len(documents), len(samples), len(models))
	return nil
}

func documentNames(documents map[string]neno.Source) []string {
	names := make([]string, 0, len(documents))
	for name := range documents {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

type capabilities struct {
	Models  []string `json:"models"`
	Actions []string `json:"actions"`
}

func checkModels(name string, source neno.Source, documents map[string]neno.Source, bindings []neno.LockBinding, models map[string]bool) error {
	var resource struct {
		Spec struct {
			Model        string       `json:"model"`
			Capabilities capabilities `json:"capabilities"`
			Allow        capabilities `json:"allow"`
			Steps        []struct {
				Capabilities capabilities `json:"capabilities"`
				Action       *struct {
					Uses string `json:"uses"`
				} `json:"action"`
				Model *struct {
					Agent string `json:"agent"`
				} `json:"model"`
			} `json:"steps"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(source.Value().Bytes(), &resource); err != nil {
		return err
	}
	references := append([]string{}, resource.Spec.Capabilities.Models...)
	references = append(references, resource.Spec.Allow.Models...)
	if resource.Spec.Model != "" {
		references = append(references, resource.Spec.Model)
	}
	for i, step := range resource.Spec.Steps {
		references = append(references, step.Capabilities.Models...)
		for _, id := range step.Capabilities.Models {
			if !slices.Contains(resource.Spec.Capabilities.Models, id) {
				return fmt.Errorf("%s step %d model %q is outside Workflow capabilities", name, i, id)
			}
		}
		for _, id := range step.Capabilities.Actions {
			if !slices.Contains(resource.Spec.Capabilities.Actions, id) {
				return fmt.Errorf("%s step %d action %q is outside Workflow capabilities", name, i, id)
			}
		}
		if step.Action != nil && !slices.Contains(step.Capabilities.Actions, step.Action.Uses) {
			return fmt.Errorf("%s step %d action %q is missing from step capabilities", name, i, step.Action.Uses)
		}
		if step.Model == nil {
			continue
		}
		pointer := fmt.Sprintf("/spec/steps/%d/model/agent", i)
		found := false
		for _, binding := range bindings {
			if binding.Owner.Path != name || binding.Pointer != pointer {
				continue
			}
			found = true
			modelValue, ok := neno.LookupPointer(documents[binding.Target.Path].Value(), "/spec/model")
			if !ok {
				return fmt.Errorf("%s: cannot read model of Agent %s", name, binding.Target.Path)
			}
			var id string
			if err := json.Unmarshal(modelValue.Bytes(), &id); err != nil {
				return err
			}
			if !slices.Contains(step.Capabilities.Models, id) {
				return fmt.Errorf("%s step %d Agent model %q is missing from step capabilities", name, i, id)
			}
			if value, exists := neno.LookupPointer(documents[binding.Target.Path].Value(), "/spec/tools"); exists {
				var tools []string
				if err := json.Unmarshal(value.Bytes(), &tools); err != nil {
					return err
				}
				for _, tool := range tools {
					if !slices.Contains(step.Capabilities.Actions, tool) {
						return fmt.Errorf("%s step %d Agent tool %q is missing from step capabilities", name, i, tool)
					}
				}
			}
		}
		if !found {
			return fmt.Errorf("%s: unresolved Agent at %s", name, pointer)
		}
	}
	for _, id := range references {
		if !models[id] {
			return fmt.Errorf("%s references model %q absent from the selected Catalog", name, id)
		}
	}
	return nil
}

type sample struct {
	Workflow string `json:"workflow"`
	Inputs   string `json:"inputs"`
	Output   string `json:"output"`
}

func readSamples(directory string) ([]sample, error) {
	value, err := readJSON(directory, "examples/checks.json")
	if err != nil {
		return nil, err
	}
	var samples []sample
	decoder := json.NewDecoder(bytes.NewReader(value.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&samples); err != nil {
		return nil, fmt.Errorf("examples/checks.json: %w", err)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("examples/checks.json must contain at least one sample")
	}
	seen := map[string]bool{}
	for _, sample := range samples {
		for _, path := range []string{sample.Workflow, sample.Inputs, sample.Output} {
			if !filepath.IsLocal(path) || strings.Contains(path, "\\") {
				return nil, fmt.Errorf("examples/checks.json: unsafe or empty path %q", path)
			}
		}
		key := sample.Workflow + "\x00" + sample.Inputs
		if seen[key] {
			return nil, fmt.Errorf("examples/checks.json: duplicate sample for %s", sample.Workflow)
		}
		seen[key] = true
	}
	return samples, nil
}

func readJSON(directory, name string) (core.Value, error) {
	file, err := os.Open(filepath.Join(directory, name))
	if err != nil {
		return core.Value{}, err
	}
	defer file.Close()
	value, err := core.Read(file, core.DefaultLimits())
	if err != nil {
		return core.Value{}, fmt.Errorf("%s: %w", name, err)
	}
	return value, nil
}

func checkSample(ctx context.Context, directory string, sample sample, snapshot neno.Snapshot, documents map[string]neno.Source, catalog neno.CatalogComposition, limits neno.SourceLimits, syntax func(string) error) error {
	source, exists := documents[sample.Workflow]
	kind, _ := source.Kind()
	if !exists || kind != neno.WorkflowSource {
		return fmt.Errorf("sample must name an existing Workflow")
	}
	inputs, err := readJSON(directory, sample.Inputs)
	if err != nil {
		return err
	}
	output, err := readJSON(directory, sample.Output)
	if err != nil {
		return err
	}
	for _, check := range []struct {
		pointer, name string
		value         core.Value
	}{
		{"/spec/inputs", sample.Inputs, inputs},
		{"/spec/outputs/schema", sample.Output, output},
	} {
		schema, _ := neno.LookupPointer(source.Value(), check.pointer)
		contract, err := neno.CompileContract(schema, core.DefaultLimits())
		if err != nil {
			return err
		}
		if err := contract.Validate(check.value); err != nil {
			return fmt.Errorf("%s: %w", check.name, err)
		}
	}
	closure, err := neno.DiscoverSnapshotClosure(ctx, snapshot, sample.Workflow,
		neno.LockSource{Kind: neno.WorkspaceLockSource}, nil, limits, syntax)
	if err != nil {
		return err
	}
	lock, err := neno.CreateLock(neno.LockSpec{
		Entry: closure.Entry, Roots: closure.Roots, Bindings: closure.Bindings,
		CatalogClosure: catalog.Closure(), CatalogDigest: catalog.Digest(),
	}, neno.DefaultLockLimits())
	if err != nil {
		return err
	}
	object, _ := core.Parse([]byte(`{}`), core.DefaultLimits())
	array, _ := core.Parse([]byte(`[]`), core.DefaultLimits())
	budget, _ := core.Parse([]byte(`{"max-expanded-steps":1000}`), core.DefaultLimits())
	declarationValue, exists := neno.LookupPointer(source.Value(), "/spec/context")
	if !exists {
		declarationValue = object
	}
	declaration, err := neno.ValidateContextDeclaration(declarationValue)
	if err != nil {
		return err
	}
	// These examples require no ambient context collection. A future Workflow
	// requiring context needs an explicit fixture rather than host-derived data.
	workflowContext, err := neno.RecordContextSnapshot(declaration, object)
	if err != nil {
		return err
	}
	input, err := neno.NewCompileInput(neno.CompileSpec{
		Entry: closure.Entry, Lock: lock, Sources: closure.Sources,
		Catalog: catalog.Effective(), PolicyBindings: array, Config: object,
		Grants: object, Budget: budget, Profiles: object, Compiler: object,
		Invocation: neno.CompileInvocation{Inputs: inputs, PolicyContext: object, WorkflowContext: workflowContext},
	})
	if err != nil {
		return err
	}
	_, err = neno.ValidateCompileStatic(input, neno.DefaultCompileStaticLimits())
	if err != nil {
		return err
	}
	return checkWorkflowExpressions(source)
}

// Only authored YAML enters the validation snapshot. Documentation and build
// tooling do not affect these checks; Git acquisition hashes the full subtree.
func readYAML(directory string, validate bool) (neno.Snapshot, map[string]neno.Source, error) {
	const maxBytes = 16 << 20
	const maxFiles = 1000
	var total int64
	var entries []neno.SnapshotEntry
	documents := map[string]neno.Source{}
	err := filepath.WalkDir(directory, func(name string, item fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if item.IsDir() {
			if strings.HasPrefix(item.Name(), ".") && name != directory {
				return filepath.SkipDir
			}
			return nil
		}
		if extension := filepath.Ext(name); extension != ".yml" && extension != ".yaml" {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || len(entries) >= maxFiles || info.Size() > maxBytes-total {
			return fmt.Errorf("unsupported file or checker limit exceeded: %s", name)
		}
		file, err := os.Open(name)
		if err != nil {
			return err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, maxBytes-total+1))
		closeErr := file.Close()
		if readErr != nil {
			return readErr
		}
		if closeErr != nil {
			return closeErr
		}
		total += int64(len(data))
		if total > maxBytes {
			return fmt.Errorf("checker byte limit exceeded")
		}
		relative, err := filepath.Rel(directory, name)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		entries = append(entries, neno.SnapshotEntry{Path: relative, Data: data, Mode: info.Mode().Perm()})
		if validate {
			source, err := neno.ParseSource(data, relative, core.DefaultLimits())
			if err != nil {
				return err
			}
			if diagnostics := neno.ValidateSource(source, core.DefaultLimits()); len(diagnostics) > 0 {
				return fmt.Errorf("%s: %v", relative, diagnostics)
			}
			documents[relative] = source
		}
		return nil
	})
	if err != nil {
		return neno.Snapshot{}, nil, err
	}
	snapshot, err := neno.VerifySnapshot(entries, neno.DefaultSnapshotLimits())
	return snapshot, documents, err
}

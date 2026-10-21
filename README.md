# Neno Index

Reusable agents, instructions, workflows, and policy for
[Neno](https://github.com/azinke/neno). [`package.yml`](package.yml) lists the
exports; models come from [`neno-catalog`](https://github.com/azinke/neno-catalog).

- [Agents](agents/): planner, reviewer, debugger, implementer, writer, and test designer.
- [Instructions](instructions/): shared guidance and procedural skills (`skill-*`).
- [Workflows](workflows/): review a change, plan a change, and design tests.
- [Policy](policies/starter.policy.yml): optional; applied only when selected by a host.

Procedural skills are Neno `Instruction` resources, composed through
`spec.instructions`. The writer can search and read project files; other agents
use supplied context. No shipped agent or workflow edits files or runs tests.

## Install

Replace the commit placeholders with reviewed full commit IDs and
`PACKAGE_REPOSITORY_URL` with this repository's published Git remote:

```sh
neno catalog install --name neno-models --format git-catalog-1 \
  --commit CATALOG_COMMIT_ID --entry catalog.yml \
  https://github.com/azinke/neno-catalog.git

neno package install --name starters --format git-package-1 \
  --commit PACKAGE_COMMIT_ID --entry package.yml \
  PACKAGE_REPOSITORY_URL
```

From an initialized project, configure credentials and authorize the model you
will use. For the reviewer:

```sh
neno config set --secret credentials.openai
neno model access set allow openai/sol/allrounder
```

See Neno's [model setup guide](https://github.com/azinke/neno/blob/main/docs/models-and-agents.md)
for other models and providers. Installation does not grant execution access.

## Run

Preview a review, inspect the Plan, then execute it:

```sh
neno run --package starters/reviewer \
  -p 'Review this Go function: func ratio(total, count int) int { return total / count }. Callers may pass count == 0.' \
  --dry-run --output review.plan.json
neno plan inspect review.plan.json
neno run --plan review.plan.json --receipt review-receipt.json
```

Execution sends a potentially billable provider request.

Workflows accept JSON inputs. Start with the examples for
[review-change](examples/review-inputs.json),
[plan-change](examples/plan-inputs.json), or
[design-tests](examples/test-inputs.json):

```sh
neno run --package starters/review-change \
  --inputs /path/to/neno.index/examples/review-inputs.json \
  --dry-run --output review-change.plan.json
```

Authorize `core/cat` in the host even when the optional `evidence` input is
omitted. To read a file at execution time, add
`"evidence": {"scope": "project", "path": "src/client.go"}` to the inputs;
the host must map and authorize that scope.

### Collect project context

Run the helper from this checkout with Bash, Python 3.9+, and Git:

```sh
bash tools/workflows/prepare-inputs.sh review-change \
  --repo /path/to/project --base HEAD --file README.md > /tmp/review-inputs.json

bash tools/workflows/prepare-inputs.sh plan-change \
  --repo /path/to/project --request 'Add an optional client timeout' \
  --file src/client.go > /tmp/plan-inputs.json

bash tools/workflows/prepare-inputs.sh design-tests \
  --repo /path/to/project --request 'Protect zero-count behavior' \
  --file src/ratio.go > /tmp/test-inputs.json
```

Repeat `--file` for more context. Review mode collects tracked staged and
unstaged changes against the base; include untracked files explicitly with
`--file`. Collection fails on unsupported or oversized content instead of
truncating it. Keep output outside the files being collected.

Inspect the JSON, then pass it to the matching workflow with `--inputs`.
The helper needs no Neno credentials and runs no model or project commands.

## Customize

[`examples/custom-reviewer.agent.yml`](examples/custom-reviewer.agent.yml)
shows how to reuse exports. Its local import works in this checkout. For another
project, copy the example and replace its entire `imports` mapping with the
output of:

```sh
neno package reference --alias starters starters
```

Keep the `starters/...` instruction references, then register and run it:

```sh
neno agent add --file custom-reviewer.agent.yml
neno run --agent custom-reviewer -p 'Your supplied diff and context'
```

For web research, copy and customize the [writer](agents/writer.agent.yml) with
separately configured search and page-reading actions; see Neno's
[MCP setup guide](https://github.com/azinke/neno/blob/main/docs/mcp-transport.md).
Workflows calling a custom agent must declare its tools at both workflow and
model-step scope, with suitable budgets and host permissions.

## Validate and contribute

Check out `neno`, `neno-catalog`, and `neno.index` alongside each other. Use Go
matching Neno's `go.mod`, Bash, Python 3.9+, and Git:

```sh
make check         # Validation and regression tests
make validate      # Sources and examples
make test-scripts  # Input helper only; no Go or Neno required

# Override checkout locations:
make check NENO_SOURCE=/path/to/neno NENO_CATALOG=/path/to/neno-catalog
```

Checks validate resources, references, capabilities, expressions, and example
contracts without installing packages or calling providers. Output fixtures are
structural examples, not model quality evaluations. Use a CLI supporting installed
source Packages and three-segment model IDs; these sources were checked against
Neno `900c652` and Catalog `9ecf226`.

Add resources to the matching directory and export them in `package.yml`. New
workflows also need input/output fixtures listed in
[`examples/checks.json`](examples/checks.json). Bump changed resource and Package
versions, run `make check`, and publish a reviewed immutable revision.

For ref-tracking installations, preview updates with
`neno package update --dry-run starters`, then apply with
`neno package update starters`. Commit-pinned installations require an explicit
install of the new revision. Regenerate portable imports and refresh affected
Locks after updates. See the
[installed Package guide](https://github.com/azinke/neno/blob/main/docs/installed-packages.md)
for lifecycle details.

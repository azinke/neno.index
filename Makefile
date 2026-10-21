NENO_SOURCE ?= ../neno
NENO_CATALOG ?= ../neno-catalog
.DEFAULT_GOAL := check
CHECK_SOURCES := "$(CURDIR)/tools/check/main.go" "$(CURDIR)/tools/check/expressions.go"

.PHONY: validate test test-scripts check check-source

check-source:
	@test -f "$(NENO_SOURCE)/go.mod" || { \
		echo "Neno source not found at $(NENO_SOURCE); set NENO_SOURCE=/path/to/neno" >&2; \
		exit 1; \
	}
	@test -f "$(NENO_CATALOG)/catalog.yml" || { \
		echo "Model Catalog not found at $(NENO_CATALOG); set NENO_CATALOG=/path/to/neno-catalog" >&2; \
		exit 1; \
	}

validate: check-source
	cd "$(NENO_SOURCE)" && go run $(CHECK_SOURCES) "$(CURDIR)" "$(abspath $(NENO_CATALOG))"

test: check-source test-scripts
	cd "$(NENO_SOURCE)" && NENO_REGISTRY_ROOT="$(CURDIR)" NENO_CATALOG_ROOT="$(abspath $(NENO_CATALOG))" \
		go test $(CHECK_SOURCES) "$(CURDIR)/tools/check/main_test.go"

test-scripts:
	PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s tools/workflows -p 'test_*.py'

check: validate test

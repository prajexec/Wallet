.SHELL := /bin/bash

.PHONY: docs-check

docs-check:
	./scripts/check-docs.sh

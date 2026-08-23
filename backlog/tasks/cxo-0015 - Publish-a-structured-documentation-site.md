---
id: CXO-0015
title: Publish a structured documentation site
status: Done
assignee: []
created_date: '2026-08-23 12:28'
updated_date: '2026-08-23 12:45'
labels: []
dependencies: []
type: docs
ordinal: 15000
---

## Description

<!-- SECTION:DESCRIPTION:BEGIN -->
Add project-owned documentation and the fleet-standard child-site manifest and sync workflow so the public repository is documented at m7kni.io/codexlb2otel/.
<!-- SECTION:DESCRIPTION:END -->

## Acceptance Criteria
<!-- AC:BEGIN -->
- [x] #1 The repository contains a navigable docs site covering purpose, architecture, installation, configuration, signals, dashboards, operations, security, and troubleshooting
- [x] #2 The child manifest follows the m7kni.io inverted site model and shared assets remain hub-owned
- [x] #3 Documentation changes dispatch a scoped docs-update event to the hub without a durable GitHub token
- [x] #4 The child documentation build and repository gate pass
<!-- AC:END -->

## Definition of Done
<!-- DOD:BEGIN -->
- [x] #1 make check passes: gofmt -l . reports nothing, go vet ./... clean, go test ./... green
- [x] #2 go build ./... succeeds
<!-- DOD:END -->

## Implementation Plan

<!-- SECTION:PLAN:BEGIN -->
1. Derive the child-site shape from the hub and the closest observability repositories.

2. Add concise project docs, docs.toml, hub-owned asset ignores, and the scoped docs-sync workflow.

3. Provision and structurally verify the repository-bound docs-sync broker role and required WIF identifiers.

4. Build the child through the hub pipeline, run the repository gate, review the diff, commit, and push.
<!-- SECTION:PLAN:END -->

## Implementation Notes

<!-- SECTION:NOTES:BEGIN -->
Added the project-owned Markdown site, docs.toml manifest, hub-injected asset ignores, and a minimal scoped sync workflow. Installed the repository WIF identifiers and created the OpenBao role docs-sync-codexlb2otel; read-back matches the proven docs-sync role shape except for the pinned repository id.

Verification: docs.toml and workflow YAML parse; actionlint and git diff --check pass; production-shape child build renders 10 pages with injected assets, no external subresources, unique titles, and one h1 per page; make check and go build ./... pass. CodeRabbit skipped because the change is documentation, declarative manifest data, and CI YAML with no branching logic.
<!-- SECTION:NOTES:END -->

## Final Summary

<!-- SECTION:FINAL_SUMMARY:BEGIN -->
Published a structured ten-page documentation site using the central m7kni.io child-manifest model. Provisioned repository-bound WIF and OpenBao docs-sync access, proved the dispatch workflow live, passed the production-shape child build and project gate, and verified the deployed pages, raw Markdown, sitemap, llms.txt, project assets, and landing-page card.
<!-- SECTION:FINAL_SUMMARY:END -->

---
slug: "verify-a-change"
area: "operations"
order: 5
title: "Verify and publish a change"
summary: "Follow changes through source checks, browser validation, and the published site."
references: [["Development and testing", "/Development-And-Testing/"], ["Documentation maintenance", "/Documentation-Maintenance/"]]
covers: ["ops:develop", "ops:test", "ops:ci", "ops:release", "ops:docs"]
frames:
  - id: verify
    title: "Check the changed behavior"
    behavior: "A contributor runs the checks that exercise their change."
    mechanics: "Application changes use the relevant source tests and CI. Site changes build the Jekyll pages and exercise the rendered routes and animations."
    story:
      - ["change", "Contributor", "Change a Dense-Mem example", ""]
      - ["check", "Checks", "Verify the resulting behavior", "result"]
    flow:
      - ["change", "Candidate revision", "Run focused checks", ""]
      - ["check", "Public entry point", "Verify the rendered result", "gate"]
    fields: ["pages-revision"]
  - id: publish
    title: "Check the published page"
    behavior: "After merge, the contributor opens the published example and its bookmark."
    mechanics: "GitHub Pages builds from gh-pages. Verify the deployed revision and load the affected links under the project path."
    story:
      - ["change", "Documentation source", "Merged revision", ""]
      - ["check", "Published site", "Open the rendered lesson", "result"]
    flow:
      - ["change", "gh-pages commit", "Build and deploy", ""]
      - ["check", "Public URL", "Verify the same revision and deep links", "result"]
    fields: ["pages-revision"]
---

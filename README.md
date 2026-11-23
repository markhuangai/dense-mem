# Dense-Mem documentation site

GitHub Pages builds this branch with Jekyll. Open the published project at
[markhuangai.github.io/dense-mem/](https://markhuangai.github.io/dense-mem/).
The repository's [documentation delivery policy](https://github.com/markhuangai/dense-mem/blob/main/.github/workflows/README.md#publish-documentation)
covers PR scope, review, and publication.

## Edit a guide or walkthrough

- Edit lookup material in the owning file under _guides. Keep its permalink
  and existing heading IDs stable. Its workflows list links to related tours.
- Edit a step in _steps/<workflow>. Its front matter supplies a stable
  anchor, order, chapter, caption, and source links. Its Markdown body is the
  explanation used in both playback and read-all views. Keep the Atlas legacy
  step aliases when reordering or inserting steps.
- Edit the Atlas fixture in _data/examples/atlas.json and its detailed scene
  sequences in _data/examples/atlas_sequences.yml. Use the same Evidence IDs,
  source text, and topic definitions across diagrams and payloads. The tour
  derives code-point offsets, hashes, and rank scores from that input.
- Use a Mermaid fence for a reference diagram. Keep the fence as the editable
  source. The site loads the local renderer when a reader opens a diagram.
- Add a workflow to _data/workflows.json and put its steps in the matching
  directory. The shared layout reads the ordered steps and draws their flow.
  Give the tour a route and link it from the owning guides.

The [migration inventory](_data/migration.json) covers the source wiki revision,
its files, heading IDs, and diagram counts. The old branch retains its Git history.
The site preserves migrated content at the corresponding project URLs.

## Build and verify

The Documentation site workflow builds with the GitHub Pages Jekyll action and
runs the link and browser checks. To run them locally with Docker, Node 24,
npm, and Python 3, run these commands from the branch root.

    mkdir -p _preview/dense-mem
    set -o pipefail
    tar --exclude=.git --exclude=node_modules --exclude=_preview --exclude=test-results --exclude=playwright-report -cf - . \
      | docker run --rm -i --entrypoint /bin/sh ghcr.io/actions/jekyll-build-pages:v1.0.13 \
        -c 'set -e; mkdir -p /workspace/site; tar -xf - -C /workspace/site;
            GITHUB_WORKSPACE=/workspace INPUT_SOURCE=site INPUT_DESTINATION=build /entrypoint.sh >&2;
            tar -C /workspace/build -cf - .' \
      | tar -xf - -C _preview/dense-mem
    python3 tests/check-site.py _preview/dense-mem
    npm ci --ignore-scripts
    npx playwright install chromium
    npm test

Use http://127.0.0.1:4173/dense-mem/ to inspect a served build. To start the
server without running tests, run python3 with the built preview directory.

    python3 -m http.server --bind 127.0.0.1 --directory _preview 4173

The link check verifies cited files and lines at their pinned commits. It
reads GitHub's public source for historical commits that are absent from the
checkout. The migration inventory check reads its source revision from Git.

## Browser assets

The Source Sans 3 Latin variable font comes from the application's Fontsource
dependency. Its license accompanies the font. Mermaid 12.3.0 comes from the
[Mermaid npm package](https://www.npmjs.com/package/mermaid/v/12.3.0) and its
bundle is served only for expanded reference diagrams. The distribution and
license are under assets/vendor. The bundle SHA-256 is
336df803d87a38b70a01e736e666c8f7601d2ce42f5e47c8fafe5d721074b9a3.

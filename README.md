# Dense-Mem documentation site

GitHub Pages builds this branch with Jekyll. Open the published project at
[markhuangai.github.io/dense-mem/](https://markhuangai.github.io/dense-mem/).
The repository's [documentation delivery policy](https://github.com/markhuangai/dense-mem/blob/main/.github/workflows/README.md#publish-documentation)
covers PR scope, review, and publication.

## Edit an interactive lesson

- Use `_lessons` for new lessons. Place the question in an area from
  `_data/system.yml`, give each frame a stable `id`, and link the canonical
  reference guides. The system index lists lessons by area and order.
- Each frame shares a moment across `story`, `flow`, and `fields`. A tile is
  `[id, label, value, style]`. Keep the same tile ID across frames when the
  same object moves. An optional fifth value identifies the earlier tile or
  `fragment-room` and `fragment-gear` when an exact quote splits from the note.
- Story and flow show the visible change. The data view uses the field IDs in
  `_data/lesson_fields.yml` to show a concrete value, producer, and consumer.
  Use `$key` for a tile value from `_data/rehearsal.yml`. A variant overrides
  the affected frames while keeping their IDs. Use `flow_notes` for optional
  explanations keyed by tile ID. The tile label exposes the note on hover,
  keyboard focus, or activation. Source links point to the pinned application
  revision in `_config.yml`.
- Record the supported surfaces in `covers`. The rendered-site check verifies
  public tools, portal sections, workers, and operational coverage. Use one
  owner for each technical explanation and link to it from related lessons.

## Edit a reference or detailed walkthrough

- Edit lookup material in the owning file under _guides. Keep its permalink
  and existing heading IDs stable. Lessons link back by their `references` list.
- Edit a step in _steps/<workflow>. Its front matter supplies a stable
  anchor, order, chapter, caption, and source links. Its Markdown body serves
  playback and read-all views. The detailed Atlas tour is at
  `/walkthroughs/atlas/`. Its original homepage anchors still resolve.
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

With Go on `PATH`, run the registry and rendered example check against a
checkout at `lesson_source_revision` from `_config.yml`.

    python3 tests/contracts.py /path/to/application-checkout --preview _preview/dense-mem

After updating the pinned revision, regenerate `_data/contracts.json` with
`python3 tests/contracts.py /path/to/application-checkout --write`.

The link check verifies cited files and lines at their pinned commits. It
reads GitHub's public source for historical commits that are absent from the
checkout. The migration inventory check reads its source revision from Git.

## Browser assets

The rehearsal photographs in `assets/images/` are ImageGen artwork for the
fictional band story. They are served as WebP, with a smaller rehearsal image
available for narrow screens.

The Source Sans 3 Latin variable font comes from the application's Fontsource
dependency. Its license accompanies the font. Mermaid 12.3.0 comes from the
[Mermaid npm package](https://www.npmjs.com/package/mermaid/v/12.3.0) and its
bundle is served only for expanded reference diagrams. The distribution and
license are under assets/vendor. The bundle SHA-256 is
336df803d87a38b70a01e736e666c8f7601d2ce42f5e47c8fafe5d721074b9a3.

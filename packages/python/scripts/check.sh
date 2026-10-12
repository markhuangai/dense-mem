#!/usr/bin/env bash
set -euo pipefail

PACKAGE_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYTHON="${DENSEMEM_TEST_PYTHON:-python3}"
OUTPUT="${DENSEMEM_TEST_OUTPUT:-}"
SDK="${DENSEMEM_TEST_SDK:-latest}"
while (($#)); do
  case "$1" in
    --python) PYTHON="$2"; shift 2 ;;
    --output) OUTPUT="$2"; shift 2 ;;
    --sdk) SDK="$2"; shift 2 ;;
    *) printf 'Unknown argument: %s\n' "$1" >&2; exit 2 ;;
  esac
done
[[ "$SDK" == minimum || "$SDK" == latest ]] || exit 2
WORK="$(mktemp -d /tmp/densemem-python-check.XXXXXX)"
trap 'rm -r -- "$WORK"' EXIT
if [[ -z "$OUTPUT" ]]; then OUTPUT="$WORK/output"; fi
mkdir -p "$OUTPUT"
OUTPUT="$(cd "$OUTPUT" && pwd)"
export PYTHONDONTWRITEBYTECODE=1
export PIP_CACHE_DIR="$WORK/pip-cache"
export RUFF_CACHE_DIR="$WORK/ruff-cache"
export MYPY_CACHE_DIR="$WORK/mypy-cache"
if [[ -z "${SOURCE_DATE_EPOCH:-}" ]]; then
  SOURCE_DATE_EPOCH="$(git -C "$PACKAGE_ROOT" show -s --format=%ct HEAD)"
fi
[[ "$SOURCE_DATE_EPOCH" =~ ^[0-9]+$ ]] || { printf 'A valid SOURCE_DATE_EPOCH is required\n' >&2; exit 1; }
export SOURCE_DATE_EPOCH
"$PYTHON" -m venv "$WORK/build"
BUILD="$WORK/build/bin/python"
"$BUILD" -m pip install -r "$PACKAGE_ROOT/requirements-dev.txt"
"$BUILD" -m build --no-isolation --wheel --sdist --outdir "$OUTPUT/dist" "$PACKAGE_ROOT"
"$BUILD" -m twine check "$OUTPUT"/dist/*
"$BUILD" -m build --no-isolation --wheel --sdist --outdir "$WORK/repeat" "$PACKAGE_ROOT"
"$BUILD" - "$OUTPUT" "$WORK" <<'PY'
import hashlib, pathlib, tarfile, sys
output, work = map(pathlib.Path, sys.argv[1:])
wheel = next((output / 'dist').glob('*.whl'))
repeat = next((work / 'repeat').glob('*.whl'))
assert wheel.read_bytes() == repeat.read_bytes(), 'wheel is not reproducible'
sdist = next((output / 'dist').glob('*.tar.gz'))
assert sdist.read_bytes() == next((work / 'repeat').glob('*.tar.gz')).read_bytes(), 'sdist is not reproducible'
with tarfile.open(sdist) as archive:
    archive.extractall(work / 'sdist', filter='data')
(output / 'checksums.txt').write_text(''.join(
    hashlib.sha256(p.read_bytes()).hexdigest() + '  ' + p.name + '\n'
    for p in sorted((output / 'dist').iterdir())
))
PY
"$BUILD" -m build --no-isolation --wheel --outdir "$WORK/from-sdist" "$WORK"/sdist/*
cmp "$OUTPUT"/dist/*.whl "$WORK"/from-sdist/*.whl
"$PYTHON" -m venv "$WORK/installed"
INSTALLED="$WORK/installed/bin/python"
"$INSTALLED" -m pip install 'pip==26.2.1' 'setuptools==84.0.0'
"$INSTALLED" -m pip install "$OUTPUT"/dist/*.whl
if [[ "$SDK" == minimum ]]; then "$INSTALLED" -m pip install 'mcp==1.30.0'; fi
"$INSTALLED" -m pip check
"$INSTALLED" - "$OUTPUT" <<'PY'
import importlib.metadata as md, json, pathlib, sys
import densemem
assert '/site-packages/' in densemem.__file__, densemem.__file__
assert pathlib.Path(densemem.__file__).with_name('py.typed').is_file()
metadata = md.metadata('densemem')
assert not metadata.get_all('Provides-Extra')
for name in ('google-adk', 'langchain', 'langchain-core'):
    try: md.version(name)
    except md.PackageNotFoundError: continue
    raise AssertionError('core unexpectedly installed ' + name)
pathlib.Path(sys.argv[1], 'installed.json').write_text(json.dumps({
    'python': sys.version, 'densemem': md.version('densemem'), 'mcp': md.version('mcp'),
    'httpx': md.version('httpx'), 'import': densemem.__file__, 'framework_dependencies': False,
}, indent=2) + '\n')
pathlib.Path(sys.argv[1], 'runtime-dependencies.txt').write_text(''.join(
    f'{dist.metadata["Name"]}=={dist.version}\n'
    for dist in sorted(md.distributions(), key=lambda d: d.metadata['Name'].lower())
    if dist.metadata['Name'].lower() != 'densemem'
))
PY
"$BUILD" -m pip_audit -r "$OUTPUT/runtime-dependencies.txt" --no-deps --disable-pip --strict --format json --output "$OUTPUT/audit.json"
"$BUILD" -m cyclonedx_py environment "$INSTALLED" --of JSON -o "$OUTPUT/sbom.json"
"$INSTALLED" -m pip install 'pytest==9.1.1' 'pytest-asyncio==1.4.0'
"$BUILD" -m pip install "$OUTPUT"/dist/*.whl
cd "$PACKAGE_ROOT"
"$BUILD" -m ruff check .
"$BUILD" -m mypy
cp -a tests "$WORK/tests"
cp -a scripts "$WORK/scripts"
cd "$WORK"
"$INSTALLED" -m pytest -c "$PACKAGE_ROOT/pyproject.toml" "$WORK/tests" -p no:cacheprovider
printf 'densemem package checks passed: SDK=%s\n' "$SDK"

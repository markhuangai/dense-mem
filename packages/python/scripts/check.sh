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
assert set(metadata.get_all('Provides-Extra')) == {'adk', 'langchain'}
for name in ('google-adk', 'langchain', 'langchain-core'):
    try: md.version(name)
    except md.PackageNotFoundError: continue
    raise AssertionError('core unexpectedly installed ' + name)
try:
    import densemem.langchain
except ImportError as error:
    assert 'densemem[langchain]' in str(error)
else:
    raise AssertionError('LangChain adapter imported without its extra')
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
"$BUILD" -m pip install "$(find "$OUTPUT/dist" -name '*.whl' -print -quit)[adk,langchain]"
"$BUILD" -m pip check
"$BUILD" - "$OUTPUT" <<'PY'
import importlib.metadata as md, json, pathlib, sys
from densemem.adk import DenseMemMemoryService
from densemem.langchain import DenseMemRetriever
pathlib.Path(sys.argv[1], 'combined-installed.json').write_text(json.dumps({
    'google-adk': md.version('google-adk'), 'langchain-core': md.version('langchain-core'),
    'combined_extra_imports': True,
}, indent=2) + '\n')
PY
cd "$PACKAGE_ROOT"
"$BUILD" -m ruff check .
"$BUILD" -m mypy
cp -a tests "$WORK/tests"
cp -a scripts "$WORK/scripts"
cd "$WORK"
"$INSTALLED" -m pytest -c "$PACKAGE_ROOT/pyproject.toml" "$WORK/tests" -p no:cacheprovider
for extra in adk langchain; do
"$PYTHON" -m venv "$WORK/$extra"
EXTRA_PYTHON="$WORK/$extra/bin/python"
"$EXTRA_PYTHON" -m pip install 'pip==26.2.1' 'setuptools==84.0.0'
"$EXTRA_PYTHON" -m pip install "$(find "$OUTPUT/dist" -name '*.whl' -print -quit)[$extra]"
if [[ "$SDK" == minimum ]]; then "$EXTRA_PYTHON" -m pip install 'mcp==1.30.0'; fi
"$EXTRA_PYTHON" -m pip check
"$EXTRA_PYTHON" - "$OUTPUT" "$extra" <<'PY'
import importlib.metadata as md, json, pathlib, sys
extra = sys.argv[2]
if extra == 'adk':
    from densemem.adk import DenseMemMemoryService
    from google.adk.memory.base_memory_service import BaseMemoryService
    assert md.version('google-adk') == '2.7.0'
    assert issubclass(DenseMemMemoryService, BaseMemoryService)
    framework = {'adk': md.version('google-adk')}
else:
    from densemem.langchain import DenseMemRetriever
    from langchain_core.retrievers import BaseRetriever
    assert md.version('langchain-core') == '1.5.4'
    assert issubclass(DenseMemRetriever, BaseRetriever)
    try: md.version('google-adk')
    except md.PackageNotFoundError: pass
    else: raise AssertionError('LangChain extra installed ADK')
    framework = {'langchain-core': md.version('langchain-core')}
assert '/site-packages/' in __import__('densemem').__file__
pathlib.Path(sys.argv[1], extra + '-installed.json').write_text(json.dumps({
    'python': sys.version, **framework, 'mcp': md.version('mcp'),
}, indent=2) + '\n')
pathlib.Path(sys.argv[1], extra + '-dependencies.txt').write_text(''.join(
    f'{dist.metadata["Name"]}=={dist.version}\n' for dist in md.distributions()
    if dist.metadata['Name'].lower() != 'densemem'
))
PY
if [[ "$extra" == adk ]]; then
  "$BUILD" "$PACKAGE_ROOT/scripts/audit.py" "$OUTPUT/$extra-dependencies.txt" "$OUTPUT/$extra-audit.json"
else
  "$BUILD" -m pip_audit -r "$OUTPUT/$extra-dependencies.txt" --no-deps --disable-pip --strict --format json --output "$OUTPUT/$extra-audit.json"
fi
"$BUILD" -m cyclonedx_py environment "$EXTRA_PYTHON" --of JSON -o "$OUTPUT/$extra-sbom.json"
"$EXTRA_PYTHON" -m pip install 'pytest==9.1.1' 'pytest-asyncio==1.4.0'
"$EXTRA_PYTHON" -m pytest -c "$PACKAGE_ROOT/pyproject.toml" "$WORK/tests" -p no:cacheprovider
if [[ "$extra" == langchain ]]; then
  "$EXTRA_PYTHON" -m pip install --no-deps 'langchain-core==1.5.3'
  "$EXTRA_PYTHON" - <<'PY'
try:
    import densemem.langchain
except ImportError as error:
    assert 'langchain-core==1.5.4' in str(error)
else:
    raise AssertionError('Unsupported LangChain version imported')
PY
fi
done
printf 'densemem package checks passed: SDK=%s\n' "$SDK"

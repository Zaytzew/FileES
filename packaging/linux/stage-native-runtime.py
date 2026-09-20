#!/usr/bin/env python3
"""Stage a trusted, locally built ELF helper and its measured library closure.

Run on the oldest supported Linux build image. glibc/loader remain host imports;
SVN, APR and every other linked library travel with the helper. No downloads.
"""
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys

CORE = re.compile(r"^(?:lib(?:c|m|pthread|dl|rt|resolv)\.so\.[0-9]+|ld-linux-x86-64\.so\.2)$")
SAFE = re.compile(r"^lib[A-Za-z0-9_-]+(?:[.][A-Za-z0-9_-]+)*[.]so(?:[.][0-9]+)*$")


def run(*args):
    return subprocess.check_output(args, text=True, env={**os.environ, "LC_ALL": "C"})


def dependencies(helper):
    result = {}
    for line in run("ldd", str(helper)).splitlines():
        if "not found" in line:
            raise RuntimeError(line.strip())
        match = re.match(r"\s*(\S+) => (/\S+) \(", line)
        if match:
            name, path = match.groups()
            if not SAFE.fullmatch(name):
                raise RuntimeError(f"unsafe library name: {name}")
            result[name] = Path(path)
        elif "linux-vdso" not in line and "ld-linux-x86-64.so.2" not in line:
            raise RuntimeError(f"unrecognized ldd entry: {line}")
    if not any(name.startswith("libsvn_client-") for name in result):
        raise RuntimeError("helper has no dynamic SVN client dependency")
    return result


def licenses(path):
    path = path.resolve()
    if shutil.which("rpm"):
        package = run("rpm", "-qf", str(path)).strip()
        files = run("rpm", "-q", "--licensefiles", package).splitlines()
        # Some RPMs still mark LICENSE/NOTICE as %%doc rather than %%license.
        files += [name for name in run("rpm", "-ql", package).splitlines()
                  if re.match(r"^(LICENSE|NOTICE|COPYING|COPYRIGHT)([.-].*)?$", Path(name).name, re.I)]
    elif shutil.which("dpkg-query"):
        package = run("dpkg-query", "-S", str(path)).split(": ", 1)[0].strip()
        files = [f"/usr/share/doc/{package.split(':')[0]}/copyright"]
    else:
        raise RuntimeError("RPM or dpkg license metadata is required")
    if not files and shutil.which("rpm"):
        # License-only subpackages (e.g. pcre2-syntax) share the exact source RPM.
        source = run("rpm", "-q", "--qf", "%{SOURCERPM}", package)
        for entry in run("rpm", "-qa", "--qf", "%{SOURCERPM} %{NAME}\n").splitlines():
            candidate_source, candidate = entry.split(" ", 1)
            if candidate_source == source:
                files += run("rpm", "-q", "--licensefiles", candidate).splitlines()
    texts = []
    for name in sorted(set(files)):
        file = Path(name)
        if file.is_file():
            texts.append(f"\n--- {package}: {file.name} ---\n" + file.read_text(errors="replace"))
    if not texts:
        raise RuntimeError(f"missing license texts for {path} ({package})")
    return package, "".join(texts)


def sha(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def main():
    if len(sys.argv) != 4:
        sys.exit("usage: stage-native-runtime.py SOURCE_ROOT BUILT_HELPER NEW_STAGE")
    root, helper, stage = (Path(arg).resolve() for arg in sys.argv[1:])
    header = run("readelf", "-h", str(helper))
    if "ELF64" not in header or "X86-64" not in header:
        raise RuntimeError("only linux-amd64 ELF is supported")
    dynamic = run("readelf", "-d", str(helper))
    if not re.search(r"\(RPATH\).*\[\$ORIGIN\]", dynamic) or "(RUNPATH)" in dynamic:
        raise RuntimeError("helper must have transitive DT_RPATH=$ORIGIN")
    deps = dependencies(helper)
    stage.mkdir()  # Refuse to mix builds or overwrite a caller's directory.
    (stage / "notices").mkdir()
    shutil.copyfile(helper, stage / "filees-svn")
    (stage / "filees-svn").chmod(0o755)
    packages = {}
    for name, path in sorted(deps.items()):
        if CORE.fullmatch(name):
            continue
        package, text = licenses(path)
        packages[package] = text
        shutil.copyfile(path, stage / name)  # Dereference SDK symlinks.
    # Check the loader actually selects the private closure, including indirect
    # dependencies. An SDK with incompatible absolute RPATHs must fail here.
    for name, path in dependencies(stage / "filees-svn").items():
        if not CORE.fullmatch(name) and path.parent.resolve() != stage:
            raise RuntimeError(f"library escapes staged runtime: {name}: {path}")
    notice = stage / "notices"
    shutil.copyfile(root / "LICENSE", notice / "FileES-LICENSE.txt")
    (notice / "LIBRARY-LICENSES.txt").write_text("".join(packages[key] for key in sorted(packages)))
    (notice / "RUNTIME-NOTICE.txt").write_text(
        "FileES Linux native SVN runtime. Private ELF library closure.\n"
        "Built from the source hashes in DEPENDENCIES.json and the distribution packages below.\n"
        "glibc and the ELF loader are host dependencies; SSH uses the host OpenSSH client.\n"
        "Build releases on the oldest supported distribution; this archive does not lower the glibc ABI baseline.\n"
        + "\n".join(sorted(packages)) + "\n")
    sources = [root / "native/filees-svn/CMakeLists.txt"]
    sources += sorted((root / "native/filees-svn/src").glob("*.c"))
    sources += sorted((root / "native/filees-svn/src").glob("*.h"))
    inventory = {
        "schema": "filees.native-runtime/v1", "platform": "linux-amd64",
        "files": [{"name": path.name, "sha256": sha(path)} for path in sorted(stage.iterdir()) if path.is_file()],
        "sources": [{"name": str(path.relative_to(root / "native/filees-svn")), "sha256": sha(path)} for path in sorted(sources)],
        "system_imports": sorted([name for name in deps if CORE.fullmatch(name)] + ["ld-linux-x86-64.so.2"]),
    }
    (notice / "DEPENDENCIES.json").write_text(json.dumps(inventory, indent=2) + "\n")
    print(stage)


if __name__ == "__main__":
    main()

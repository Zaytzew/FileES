# Windows x64 — przepis budowy helpera

Apache **nie wydaje** SDK Windows. SlikSVN/Tortoise/VisualSVN to `svn.exe`
i DLL runtime, nie nagłówki/`*.lib`. Ten przepis składa spójny prefix
1.14.5 na maszynie z MSVC, potem buduje `filees-svn.exe`.

To nie jest odbiór: Unicode, case-only, long paths i closure DLL trzeba
zmierzyć na tej maszynie. Cross-compile z Linuksa nie zastępuje.

## 1. Zależności (vcpkg, triplet `x64-windows`)

```bat
git clone https://github.com/microsoft/vcpkg C:\src\vcpkg
C:\src\vcpkg\bootstrap-vcpkg.bat -disableMetrics
C:\src\vcpkg\vcpkg install --triplet x64-windows apr apr-util expat zlib sqlite3 serf openssl
```

Nie instaluj portu `subversion` z vcpkg jako źródła prawdy: jego wersja
jest snapshotem, nie pinem 1.14.5.

## 2. Apache Subversion 1.14.5 ze źródeł

Pobierz [subversion-1.14.5](https://subversion.apache.org/download.cgi).
Buduj CMake + ten sam toolset MSVC co vcpkg, wskazując prefix vcpkg
(`CMAKE_TOOLCHAIN_FILE`). Prefix wynikowy musi zawierać `svn_client.h`
oraz `svn_client-1.lib` / `libsvn_client-1.dll` (nazwy zależą od generatora).

## 3. Helper FileES

Z korzenia checkoutu, PowerShell:

```powershell
$svn = "C:\src\svn-1.14.5-prefix"
$apr = "C:\src\vcpkg\installed\x64-windows"
cmake -S native/filees-svn -B dist/native-svn-build -G "Visual Studio 17 2022" -A x64 `
  -DCMAKE_BUILD_TYPE=RelWithDebInfo `
  -DSVN_INCLUDE_DIR="$svn\include\subversion-1" `
  -DAPR_INCLUDE_DIR="$apr\include" `
  -DSVN_CLIENT_LIBRARY="$svn\lib\svn_client-1.lib" `
  -DSVN_WC_LIBRARY="$svn\lib\svn_wc-1.lib" `
  -DSVN_SUBR_LIBRARY="$svn\lib\svn_subr-1.lib" `
  -DAPR_LIBRARY="$apr\lib\apr-1.lib"
cmake --build dist/native-svn-build --config RelWithDebInfo
```

Połóż `filees-svn.exe` i **cały** zestaw DLL (libsvn_*, APR, serf, sqlite,
OpenSSL, VCRUNTIME) w jednym katalogu. Nie wołaj systemowego `svn.exe`.

```powershell
$env:FILEES_SVN_PROBE = "C:\path\filees-svn.exe"
$env:FILEES_PROBE_SVN = "C:\path\svn.exe"          # osobny CLI tylko do fixture
$env:FILEES_PROBE_SVNADMIN = "C:\path\svnadmin.exe"
go test -count=1 -buildvcs=false -tags=native_svn_probe ./native/filees-svn ./pkg/client
```

Symlink fixture jest wymagany. `FILEES_NATIVE_SVN` w usłudze Windows
włącza helper dla `record-move` **oraz** czasowników WC-lokalnych
(status/add/delete/prop/cleanup/revert/resolve). RA (checkout, update,
commit, lock, log, cat) nadal woła CLI, dopóki te czasowniki nie wejdą
do helpera.

Linux w tej rozbudowie nie uczestniczy: daemon Linuksa nadal używa
helpera wyłącznie do `record-move`.

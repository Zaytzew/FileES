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
  -DSVN_CLIENT_LIBRARY="$svn\lib\libsvn_client-1.lib" `
  -DSVN_WC_LIBRARY="$svn\lib\libsvn_wc-1.lib" `
  -DSVN_SUBR_LIBRARY="$svn\lib\libsvn_subr-1.lib" `
  -DAPR_LIBRARY="$apr\lib\libapr-1.lib"
cmake --build dist/native-svn-build --config RelWithDebInfo
```

Na potrzeby ręcznej próby helper wymaga DLL obok EXE. Wydanie buduje
`packaging/windows/stage-native-runtime.ps1`: świeży build C, rekurencyjny
odczyt PE imports przez dumpbin, tylko jawne prefixy SVN/vcpkg/MSVC CRT.
Nie kopiować całych SDK ani bibliotek z System32. Skrypt odmawia nieznanej
zależności, różnych bajtów tej samej DLL, braku notices i niezgodnego helpera.
Uruchamia --version z PATH ograniczonym do Windows/System32.

```powershell
$env:FILEES_SVN_PROBE = "C:\path\filees-svn.exe"
$env:FILEES_PROBE_SVN = "C:\path\svn.exe"          # osobny CLI tylko do fixture
$env:FILEES_PROBE_SVNADMIN = "C:\path\svnadmin.exe"
go test -count=1 -buildvcs=false -tags=native_svn_probe ./native/filees-svn ./pkg/client
```

Symlink fixture jest wymagany. Windows obsługuje już natywne WC-local
**i RA**, w tym provisioning/service-WC, zdalne info i obserwacje blokad.
CLI jest zabronione w tak wybranym kliencie. `FILEES_NATIVE_SVN` pozostaje
jawnym nadpisaniem developerskim; bundle Windows wybiera osadzony helper
domyślnie. Linux pozostaje przy record-move, reszta przez CLI.

## 4. Samowystarczalny bundle/MSI

Przykład po przygotowaniu SDK (pełne ścieżki do narzędzi właściwe dla hosta):

```powershell
packaging/windows/stage-native-runtime.ps1 `
  -CMake '<ścieżka>/cmake.exe' -Dumpbin '<ścieżka>/dumpbin.exe' `
  -OutputDir 'tmp/native-runtime-candidate' `
  -SvnPrefix 'C:/src/svn-1.14.5-prefix' -SvnSource 'C:/src/subversion-1.14.5' `
  -VcpkgPrefix 'C:/src/vcpkg/installed/x64-windows' `
  -CRTRoot '<MSVC>/Redist/MSVC/<wersja>/x64/Microsoft.VC143.CRT' `
  -RedistNotice '<Visual Studio>/Licenses/1045/Redist.txt'
```

Z Git Bash:

```sh
FILEES_NATIVE_RUNTIME="$PWD/tmp/native-runtime-candidate" REVISION=NNN \
  packaging/build-client-bundle.sh tmp/client-candidate-NNN
```

Nowy output musi być nieistniejący. Skrypt zachowuje osobny katalog budowy C
do diagnostyki. `filees-native-package` porównuje hashe źródeł C/CMake ze
stagingiem, weryfikuje manifest DLL, tworzy deterministyczny ZIP i Go overlay;
nie podmienia assetu w WC. Build daemon: `native_svn_bundle`. Bez overlay
placeholder jest niepoprawnym runtime i startup odmawia, nie wraca do CLI.

ZIP obejmuje notices bibliotek (także statycznego serf); BSD FileES nie
przelicencjonowuje zależności. Payload jest częścią EXE, więc dotychczasowy
czteroplikowy updater i MSI przenoszą go bez zmiany protokołu. Runtime trafia
do `%LOCALAPPDATA%/FileES/native-svn/<SHA256-ZIP>/`; atomowy rename całego
katalogu, weryfikacja istniejących bajtów i brak naprawy w miejscu. Starsze
runtime pozostają: brak procesu helpera nie dowodzi zakończenia starego demona.
GC nie jest jeszcze zaimplementowany. Nie jest to ochrona przed złośliwym
procesem z tymi samymi uprawnieniami użytkownika.

`filees.exe native-runtime` przygotowuje i drukuje ścieżkę bez startu demona.
Test odbioru bundle (izolowany katalog i cache, bez normalnej instalacji):

```powershell
$env:FILEES_TEST_NATIVE_BUNDLE = (Resolve-Path 'tmp/client-candidate-NNN').Path
go test -tags native_svn_package ./internal/clientupdate -run TestNativeBundleThroughFourFileInstaller -count=1
```

Dla testów z odciętym PATH wskaż także `FILEES_PROBE_SVNLOOK`; tylko fixture
i niezależny oracle mogą używać tych trzech zewnętrznych narzędzi. Raport
pakowania: `reports/NATIVE_WINDOWS_PACKAGING_2026-09-09.md`.

Linux w tej rozbudowie nie uczestniczy: daemon Linuksa nadal używa
helpera wyłącznie do `record-move`.

Plan sesji bliźniaka (kolejność, zakazy, kryterium):
[WINDOWS_SESSION.md](WINDOWS_SESSION.md).

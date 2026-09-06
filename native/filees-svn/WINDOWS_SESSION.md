# Sesja Windows — odbiór native `filees-svn`

Dla bliźniaka Grok na Windows. Źródła: **r911** (Linux). Przepis CMake:
[WINDOWS.md](WINDOWS.md). Kontrakt: [README.md](README.md). Zakres produktu:
[concepts/DESKTOP_SVN_CLIENT_SCOPE.md](../../concepts/DESKTOP_SVN_CLIENT_SCOPE.md)
(załącznik Linux CLI / Windows helper).

To **odbiór natywny**, nie nowy pion i nie drugi Subversion. Serwer Apache
zostaje. Nie zastępujemy `svnserve`/`svnadmin`. Nie wołamy Tortoise/Slik
jako SDK.

## Stan, od którego startujesz

Linux złożył i przetestował helper na libsvn 1.14.5:

- `record-move` (bez zmian semantyki);
- WC-lokalne: `status`, `add`, `delete`, `propget`/`propset`/`propdel`,
  `cleanup`, `revert`, `resolve`;
- `filees-svn --version` / `verbs` wypisuje listę;
- daemon Linuksa **nadal** używa helpera wyłącznie do `record-move`;
- na Windows `FILEES_NATIVE_SVN` w `cmd/filees/native_svn.go` już może
  się włączyć i wtedy WC-lokalne idą przez helper. RA zostaje na CLI.

Cross-compile Go daemona z Linuksa **nie** jest tym odbiorem. `filees-svn.exe`
musi powstać na MSVC z coherentnym prefixem 1.14.5.

## Powierzchnia

Ruszaj: `native/filees-svn/`, `pkg/client/native_*.go`, w razie potrzeby
tylko layout DLL obok exe. Raport do `reports/`.

Nie ruszaj: autolock / `pkg/passport` / control-v1, Android, Upload Channel,
Whale, Wails, podpisany FILEES-BIN, `master.passwd`, cudzych brudnych plików
(ACTIVE_WORK Codexa). Nie włączaj helpera w produkcyjnym daemonie właściciela,
dopóki probe nie jest PASS. Nie dokładaj czasowników RA (checkout, update,
commit, lock, log, cat). Nie zmieniaj polityki Linuksa (tylko `record-move`).

Swoja WC, `svn update` do HEAD ≥ r911. Nie startuj z r868.

## Kolejność

1. **Czysta WC, HEAD.** `svn status` pusty zanim cokolwiek budujesz.
2. **SDK 1.14.5**, nie snapshot vcpkg `subversion`. Kroki w WINDOWS.md:
   vcpkg `x64-windows` tylko na APR/serf/sqlite/OpenSSL/zlib/expat; SVN
   1.14.5 ze źródeł Apache, CMake, ten sam MSVC co vcpkg. Slik/Tortoise
   CLI **nie** jest SDK (brak nagłówków/`*.lib`). Mogą być osobnym CLI
   do fixture (`FILEES_PROBE_SVN` / `SVNADMIN`), nie do linkowania helpera.
3. **Zbuduj `filees-svn.exe`.** CMake jak w WINDOWS.md. `wmain` musi wejść
   (wide argv). Połóż exe i **cały** closure DLL w jednym katalogu:
   libsvn_*, APR, serf, sqlite, OpenSSL, VCRUNTIME. Zapisz listę plików
   i skąd pochodzą. Nie polegaj na PATH z Tortoise.
4. **`--version`.** Runtime i nagłówki 1.14.x, `ok:true`, lista czasowników
   jak na Linuksie. Inna gałąź SVN = stop, nie „prawie 1.14”.
5. **Harness.** Developer Mode albo inna realna możliwość `os.Symlink`.
   Brak symlinka = `t.Fatalf` w probe; to **nie** jest PASS. Osobny
   `svn.exe`/`svnadmin.exe` 1.14 tylko do fixture, nie mieszany z helperem.
   ```
   go test -count=1 -buildvcs=false -tags=native_svn_probe ./native/filees-svn ./pkg/client
   ```
   Musi przejść dotychczasowy `record-move` **oraz** `TestWCLocalAddStatusPropRevertDelete`.
6. **Windows-only, poza Linuxowym PASS.** Osobne dowody, nie skip-as-green:
   - polskie znaki i spoza łaciny w ścieżce (smoke już ma Łódź/Zażółć);
   - case-only rename, jeśli NTFS na to pozwala — wynik opisać, nie zgadywać;
   - ścieżka dłuższa niż MAX_PATH (`\\?\` albo wyłączony limit) — zmierzyć,
     nie deklarować;
   - helper działa gdy `svn.exe` **nie** jest na PATH.
7. **Nie MSI, nie para produkcyjna.** Lab opt-in `FILEES_NATIVE_SVN` na
   jednorazowej WC FileES jest dozwolony po punkcie 5, z backupem i bez
   danych właściciela. To obserwacja, nie odbiór daemona.
8. **Raport** `reports/NATIVE_SVN_WINDOWS_YYYY-MM-DD.md`: host, toolset MSVC,
   pin 1.14.5 (SHA źródeł), lista DLL, wynik probe, Unicode/case/long-path,
   czy symlink był możliwy, czego nie ruszano. Commit raportu i ewentualnych
   poprawek C/`#ifdef _WIN32` potrzebnych do kompilacji. Nie commitować
   exe/DLL/SDK.

## Kryterium sesji

Gotowe, gdy: natywny `filees-svn.exe` 1.14.5, probe PASS na tej maszynie,
closure DLL spisany, Windows-only punkty zmierzone albo jawnie FAIL z
powodem. Niegotowe, gdy: tylko `GOOS=windows` z Linuksa, Slik jako SDK,
symlink pominięty, albo daemon produkcyjny przestawiony na helper.

RA, MSI, licencje redystrybucji i odcięcie PATH `svn.exe` u użytkownika
to następna sesja, po tym raporcie.

# Jak wydać klienta desktopowego na Windows

Przepis operacyjny, w tym samym kształcie co `HOWTO-BUILD-SERVER-BUNDLE.md`.
Kończysz na **niepodpisanym** wydaniu zacommitowanym do `FILEES-BIN`. Podpis
składa właściciel na osobnej maszynie; klucz prywatny nie ma prawa pojawić się
na hoście budującym.

## Czego to dotyczy, a czego nie

Do 2026-09-04 kanał klienta **nie istniał dla żadnej platformy**. W `FILEES-BIN`
był wyłącznie kanał serwerowy (`channels/alpha.json`, schemat v1, dla
`filees-install`) — ani jednego `.v2.json`, ani jednego komponentu klienckiego.
Czyli linuksowy self-update, który jest w kodzie od dawna, też nigdy nie miał z
czym rozmawiać.

To jest procedura tworzenia tego kanału i wypuszczania w nim wydań Windows.
Serwer ma własną, osobną i niezmienioną.

## Dwie drogi aktualizacji, jeden katalog

| droga | kiedy | co robi |
|---|---|---|
| kanał | między wydaniami | klient sam pobiera bundel, sprawdza podpis, podmienia binarki w miejscu |
| MSI | pierwsza instalacja, naprawa i wdrożenie ręczne | `MajorUpgrade` kładzie to samo, plus skrót autostartu |

Obie kończą w `%LOCALAPPDATA%\Programs\FileES`. **MSI powstaje z tego samego
stagingu co bundel i oba są artefaktami jednego podpisanego manifestu** — inaczej instalator i kanał rozjechałyby się, a
pierwszym objawem byłaby aktualizacja zgłaszająca zmianę tam, gdzie nic się nie
zmieniło.

## Przepis

```sh
RELEASE_ID=rNNN \
SEQUENCE=NNN \
SECURITY_EPOCH=1 \
KEY_ID=<identyfikator klucza, którym podpiszesz> \
FILEES_BIN_WC=/ścieżka/do/FILEES-BIN \
tools/prepare-client-release-windows.sh
```

Skrypt odmówi, jeśli: kopia źródeł ma zmiany albo nie jest na HEAD, kopia
`FILEES-BIN` ma cokolwiek lokalnego, albo wydanie o tym `RELEASE_ID` już
istnieje. **Wydanie jest niezmienne** — przebudowa, która po cichu zastępuje
podpisane, to sposób, w jaki dwie różne binarki zaczynają dzielić jeden
identyfikator.

Skrypt buduje parę binarek, bundel aktualizatora oraz MSI. Nie istnieje drugi,
ręczny krok kompilacji instalatora: wynik trafia od razu obok bundla i zostaje
ujęty w `manifest.json` jako artefakt `kind: installer`.

## Wersje: trzy postacie tej samej liczby

To jest miejsce, w którym najłatwiej się pomylić.

| gdzie | postać | przykład |
|---|---|---|
| klient (`filees version`) | `base+rREW` | `0.1.15+r825` |
| `VERSION` w bundlu, nazwa MSI | `base.REW` | `0.1.15.825` |
| `ProductVersion` w MSI | `major.minor.REW` | `0.1.825` |

Trzecia nie jest kaprysem: **Windows Installer porównuje tylko trzy pola**
(`major.minor.build`). Czwarte bywa przyjmowane przez narzędzia i **ignorowane
przy porównaniu**, więc `0.1.15.819` i `0.1.15.825` nie uporządkowałyby się jako
aktualizacja. Rewizja idzie w pole `build`, bo tylko ona rośnie globalnie.
Rewizja musi się zmieścić w `0..65535`.

Wszystkie trzy pochodzą z **jednej** liczby zapisanej przez skrypt wydania.
Dwa niezależne źródła to sposób, w jaki instalator zaczyna kłamać o tym, co
zainstalował.

## Co podpisujesz

```
releases/<RELEASE_ID>/desktop/windows-amd64/manifest.json   ->  manifest.json.sig
releases/<RELEASE_ID>/channel.v2.json                       ->  channels/alpha.v2.json (+ .sig)
```

Manifest wiąże sumą i rozmiarem zarówno `filees-client-windows-amd64.tar.gz`,
jak i `filees-<wersja>.msi`. Aktualizator klienta wybiera tylko artefakt
`kind: bundle`; MSI jest w tym samym wydaniu dla pobrania i audytu, nie jest
uruchamiany automatycznie.

Obie muszą być podpisane **tym samym kluczem** — resolver pobiera klucz z
koperty i weryfikuje nim manifest, więc rozjazd kluczy jest odrzuceniem, nie
ostrzeżeniem.

Koperta **jest dokumentem całego kanału**, nie jednej platformy. Skrypt scala
istniejącą, jeśli jest; gdybyś generował ją od zera, wydanie Windows
zatrzymałoby aktualizacje klientom linuksowym, a dowiedziałby się o tym
użytkownik Linuksa.

## Na co klient musi być przygotowany

Build dystrybucyjny ma zaszyte publiczne: adres `FILEES-BIN`, nazwę kanału i
klucz weryfikacyjny. Bez wpisu `update` w `config.json` klient automatycznie
sprawdza podpisany kanał i pokazuje dostępną wersję w GUI. Pobranie planu oraz
instalacja nadal wymagają świadomej akcji użytkownika.

Jawne `"update":{"enabled":false}` jest opt-out i wyłącza domyślny kanał
dystrybucyjny. Pełny wpis `enabled:true` zastępuje jego publiczne lokalizacje,
ale nigdy nie może zastąpić klucza zaufania wkompilowanego w program.

```json
"update": {
  "enabled": true,
  "repo_url": "svn+ssh://_filees-release@<host>/FILEES-BIN",
  "channel": "alpha",
  "component": "desktop",
  "platform": "windows-amd64",
  "state_path": "C:\\Users\\<user>\\.local\\share\\filees\\update\\state.json",
  "stage_root": "C:\\Users\\<user>\\.local\\share\\filees\\update\\stage",
  "svn_program": "svn"
}
```

`platform` musi zgadzać się z `GOOS-GOARCH` działającego klienta — inaczej demon
**odmawia startu**, i to celowo: klient aktualizowany cudzym bundlem jest gorszy
niż klient nieaktualizowany.

## Czego wymaga host budujący

- Go z cross-kompilacją na `windows/amd64` (bez cgo, więc wystarczy sam Go)
- SVN i kopie robocze obu repozytoriów
### Łańcuch narzędzi do MSI — cztery pułapki, każda kosztowała czas

**Wersja WiX-a: piątka, nie najnowsza.** WiX **v6 i v7 wymagają akceptacji
licencji Open Source Maintenance Fee** i odmawiają uruchomienia bez niej
(`error WIX7015`). To zobowiązanie licencyjne, nie techniczne — decyzja
właściciela, nie budującego. **WiX v5 jest sprzed tej zmiany, na MIT, i używa
tego samego schematu `v4`**, więc pliki `.wxs` działają bez zmian.

```powershell
dotnet tool install --global wix --version "5.*"
wix extension add --global WixToolset.UI.wixext/5.0.2   # wersję TRZEBA przypiąć
```

Bez przypięcia rozszerzenie wciągnie się w wersji 7 i WiX v5 odmówi
(`WIX6101: Could not find expected package root folder wixext5`).

**.NET SDK bez administratora.** `winget install Microsoft.DotNet.SDK.8` żąda
podniesienia uprawnień i zawiesza się na monicie UAC. Oficjalny skrypt instaluje
do profilu i **nie wymaga admina**:

```powershell
Invoke-WebRequest https://dot.net/v1/dotnet-install.ps1 -OutFile dotnet-install.ps1
./dotnet-install.ps1 -Channel 8.0 -InstallDir "$env:USERPROFILE\.dotnet"
```

**`DOTNET_ROOT` po instalacji per-user.** `wix.exe` szuka runtime'u w
systemowym `C:\Program Files\dotnet`, znajduje tam starsze wersje i odmawia
startu komunikatem o brakującym frameworku — **nie o tym, gdzie szukał**.
`build-msi.ps1` ustawia `DOTNET_ROOT` sam, jeśli widzi instalację w profilu.

**Strona kodowa.** Polskie znaki w komunikatach MSI wymagają
`Package/@Codepage="1250"`. Domyślna 1252 ich nie ma i budowa pada z `WIX0311`,
wskazując linię, a nie znak.

Klucz publiczny bierz z `FILEES-BIN/FILEESrelease.pub`. **Nie z drzewa źródeł** —
`cmd/filees/assets/release.pub` to zaślepka, dokładnie jak po stronie serwera.
Skrypt przekazuje do buildu również publiczny URL repozytorium oraz kanał.
Klucz prywatny nie jest potrzebny ani do kompilacji, ani do utworzenia MSI.

## Co się dzieje przy aktualizacji na żywym kliencie

Windows nie pozwala nadpisać działającego pliku wykonywalnego, ale **pozwala go
przemianować**. Instalator odsuwa starą binarkę pod nazwę
`filees.exe.superseded-<data>`, kładzie nową na jej miejsce i zostawia proces
działający ze starego obrazu do restartu. Odsunięte pliki sprząta następna
aktualizacja — wcześniej się nie da, bo coś je jeszcze uruchamia.

Stąd `restart_required` w wyniku: aktualizacja jest kompletna na dysku i
**niekompletna w pamięci**, dopóki demon nie wstanie ponownie.

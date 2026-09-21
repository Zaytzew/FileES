# FileES na Windows — instalacja i kanały aktualizacji

## Co dostajesz

Jeden plik `filees-<wersja>.msi`. Instaluje się **bez uprawnień administratora**,
do `%LOCALAPPDATA%\Programs\FileES`, i zakłada skrót w folderze Autostart, więc
po najbliższym zalogowaniu FileES wstaje sam.

W środku są dwie rzeczy, nie jedna:

| | |
|---|---|
| `filees.exe` | demon — to on pilnuje plików; działa w tle, bez okna |
| `filees-gui-wails.exe` | interfejs — okno, które oglądasz |

Demon jest usługą i **ma chodzić zawsze**. Interfejs możesz zamknąć kiedy
chcesz; nic go nie otworzy z powrotem do następnego zalogowania. To celowe.

## SmartScreen powie, że to niebezpieczne

Powie, i będzie miał rację w tym sensie, w jakim ją ma zawsze: **ten instalator
nie jest podpisany certyfikatem**. To alfa, certyfikatu jeszcze nie ma, i nie
udajemy, że jest.

Co zobaczysz: „System Windows ochronił Twój komputer". Żeby przejść dalej —
**Więcej informacji** → **Uruchom mimo to**.

Jeśli nie ufasz źródłu, z którego dostałeś ten plik, **nie klikaj tego**. To
zdanie jest tu na serio: obejście SmartScreena jest dokładnie tym, o co prosi
każdy złośliwy instalator, i jedyne, co je tutaj usprawiedliwia, to że wiesz,
od kogo masz plik.

## Pierwsze uruchomienie

Instalator **nie tworzy konfiguracji**. Robi to nadzorca przy pierwszym starcie,
bo dopiero wtedy wiadomo, w czyim profilu ma ona żyć. Najpierw otwiera się
okno FileES z wyborem przyszłych aktualizacji: beta (wybrane, sprawdzone
wydania) lub alpha (nowe funkcje i testy). Potwierdzenie zapisuje wybór;
anulowanie zatrzymuje start i pozwala ponowić go później. Powstaje minimalny
`config.json` wskazujący na `%USERPROFILE%\.local\share\filees` — i od tej pory
jest Twój. Żadna aktualizacja go nie nadpisze; instalator ani go nie kładzie,
ani nie kasuje.

Dalej: otwórz interfejs i aktywuj aplikację na swoim serwerze.
Aktualizacja istniejącej instalacji nie pyta ponownie i nie zmienia jej
kanału. Wybór nie oznacza pobrania innego wydania podczas instalacji.
Beta nie została jeszcze opublikowana — to opis przygotowanej ścieżki.

## Aktualizacje

Są dwie drogi i **obie prowadzą do tego samego katalogu**:

1. **Kanał** — klient sam pyta `FILEES-BIN` o nowe wydanie na swoim kanale i
   sprawdza podpis. GUI pokazuje dostępną wersję; instalację potwierdzasz
   przyciskiem. Publiczny klucz i domyślny kanał są częścią buildu dystrybucyjnego.
   Jawny wybór kanału w `config.json` ma pierwszeństwo i przetrwa aktualizację.
2. **Nowy MSI** — instalacja na wierzchu poprzedniej, przez `MajorUpgrade`.

Nowe wydanie 0.1.17 udostępnia polecenie trwałego wyboru kanału (bez ręcznej
edycji konfiguracji). W PowerShell, po utworzeniu konfiguracji przez pierwsze
uruchomienie aplikacji:

```powershell
& "$env:LOCALAPPDATA\Programs\FileES\filees.exe" update-channel beta --config "$env:LOCALAPPDATA\Programs\FileES\config.json"
```

Bez argumentu `beta` polecenie tylko wyświetla aktualne ustawienie.
Po zmianie trzeba zrestartować całą parę. Polecenie nie instaluje wydania,
nie zeruje ochrony przed cofnięciem wersji ani nie włącza wyłączonych
aktualizacji. Nie dotyczy Store. Beta nie jest jeszcze publicznie uruchomiona
w momencie dodania tej instrukcji — stan publikacji sprawdza się oddzielnie.

Jawne `"update":{"enabled":false}` pozostaje opt-out. Jeśli aktualizacja
przyszła kanałem, a potem uruchomisz **Napraw** z listy programów, wrócisz do
wersji z MSI. Klient rozpozna faktycznie uruchomioną starszą wersję i ponownie
zaproponuje bieżące wydanie bez obniżania zabezpieczenia przed rollbackiem.

## Odinstalowanie

Standardowo, z listy zainstalowanych aplikacji. Zniknie: para binarek, skrypty
autostartu, skrót, logi demona.

**Nie zniknie:** Twoja konfiguracja, Twoje kopie robocze i Twoje pliki. FileES
nie kasuje pracy, którą pilnował — ani przy aktualizacji, ani przy usunięciu
samego siebie.

## Dla budującego

Najpierw zbuduj staging natywnego helpera zgodnie z
`native/filees-svn/WINDOWS.md`. Bundle Windows wymaga `FILEES_NATIVE_RUNTIME`;
nie wyda już po cichu wariantu zależnego od obcego svn.exe. Staging sprzed
zmiany źródeł C jest odrzucany. Wskaż nowy katalog wyjściowy bundle.

```sh
# przygotuj wydanie (buduje parę, bundel i MSI, stage'uje je w FILEES-BIN)
RELEASE_ID=rNNN SEQUENCE=NNN KEY_ID=<klucz> \
  FILEES_NATIVE_RUNTIME=/ścieżka/do/native-runtime \
  FILEES_BIN_WC=/ścieżka/do/FILEES-BIN \
  tools/prepare-client-release-windows.sh
```

MSI i bundel powstają z tego samego stagingu i są opisane jednym manifestem — inaczej instalator i kanał
rozjechałyby się, a pierwszym objawem byłaby aktualizacja zgłaszająca zmianę
tam, gdzie nic się nie zmieniło.
GUI w bundlu jest wariantem produkcyjnym `windowsgui`; autostart nie powinien
otwierać obok panelu czarnego okna konsoli.

Bundle zachowuje czytelną wersję `major.minor.patch.revision`. Windows
Installer porównuje tylko trzy pola, dlatego `MajorUpgrade` dostaje
`major.minor.revision`; pełna wersja pozostaje w nazwie MSI i rejestrze. Rewizja
musi mieścić się w zakresie pola build MSI (`0..65535`).

Wymaga **WiX Toolset v4** (`dotnet tool install --global wix`), a to wymaga
.NET SDK. Sam runtime nie wystarczy.

Helper i jego biblioteki/notices są osadzone w demonie. Pierwszy start
rozpakowuje prywatny zestaw do `%LOCALAPPDATA%/FileES/native-svn/<hash>/`.
Odinstalowanie MSI nie usuwa tego cache; różne wersje są zachowywane, aby
nie usunąć bibliotek starszemu, nadal działającemu demonowi. GC pozostaje
otwarte. `filees.exe native-runtime` pokazuje wybrany zestaw bez startu usługi.

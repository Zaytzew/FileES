# FileES na Windows — instalacja i kanał aktualizacji (alfa)

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
bo dopiero wtedy wiadomo, w czyim profilu ma ona żyć. Powstaje minimalny
`config.json` wskazujący na `%USERPROFILE%\.local\share\filees` — i od tej pory
jest Twój. Żadna aktualizacja go nie nadpisze; instalator ani go nie kładzie,
ani nie kasuje.

Dalej: otwórz interfejs i aktywuj klienta na swoim serwerze.

## Aktualizacje

Są dwie drogi i **obie prowadzą do tego samego katalogu**:

1. **Kanał** — klient sam pyta `FILEES-BIN` o nowe wydanie na swoim kanale i
   sprawdza podpis. GUI pokazuje dostępną wersję; instalację potwierdzasz
   przyciskiem. Kanał i publiczny klucz są częścią buildu dystrybucyjnego,
   więc nie wymagają dopisywania sekretów ani ustawień do `config.json`.
2. **Nowy MSI** — instalacja na wierzchu poprzedniej, przez `MajorUpgrade`.

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

```sh
# przygotuj wydanie (buduje parę, bundel i MSI, stage'uje je w FILEES-BIN)
RELEASE_ID=rNNN SEQUENCE=NNN KEY_ID=<klucz> \
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

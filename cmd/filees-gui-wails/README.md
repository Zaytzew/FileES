# filees-gui-wails

Docelowy klient GUI oparty na Wails v3. Zastępuje warstwę prezentacji Fyne,
korzystając z tego samego demona i wspólnej logiki repozytoriów.

Granica jest taka sama jak w GUI Fyne:

```text
WebView/CSS -> GUIService -> internal/gui/app -> pkg/ipcclient -> daemon
```

Klient renderuje pełną projekcję stanu, reaguje na zdarzenia IPC oraz
przekazuje techniczne intencje `Refresh` i `Reconnect`. Interfejs akcyjny
udostępnia `Otwórz`, `Zablokuj` i `Zwolnij` tylko wtedy, gdy bieżąca projekcja
repozytorium na to pozwala. `GUIService.Trigger` tłumaczy gest WebView na
zamknięty zbiór istniejących intencji, a wspólny `internal/gui/actions`
ponownie sprawdza stan i jako jedyny woła IPC. Frontend jest celowo bez
Node/Vite, aby build klienta nie zależał od drugiego toolchainu.

Na Windows okno jest bez natywnej ramki (`Frameless`) i ma własny pasek z
przeciąganiem oraz kontrolkami okna. CSS ukrywa scrollbar WebView, ale nie
wyłącza przewijania kółkiem, touchpadem ani klawiaturą.

Po otrzymaniu projekcji renderer mierzy naturalną szerokość nazw i całych
zestawów akcji. Początkowe okno tylko rośnie do rozmiaru potrzebnego panelom,
nie przekraczając obszaru roboczego bieżącego monitora i nie ingerując w stan
zmaksymalizowany. Ręczne zmniejszenie wstrzymuje automatyczne rozszerzanie do
czasu ponownego powiększenia; gdy ekran jest za mały, poziomy overflow pozostaje
ograniczony do listy folderów zamiast ucinać przyciski pod ramką serwera.

Pełna projekcja zawiera także aktywne rezerwacje plikowe. Renderer dostaje
opaque identyfikator wiersza, opis i capability zwolnienia, ale nigdy token
fencingowy SVN. Przycisk `Zwolnij` wraca do wspólnego kontrolera, który po
potwierdzeniu ponownie rozwiązuje identyfikator i przekazuje token demonowi.

Przycisk `Dziennik` rozwija w panelu tę samą chronologię, którą Fyne buduje w
`internal/gui/journal`. Skrót używa czasu względnego, pełny widok dokładnego
`dd:mm:yy hh:mm`, a powtarzane historyczne `NET-4007` są scalone per
repozytorium. Nowy błąd łączności trafia do dziennika dopiero po 45 sekundach
trwałej awarii; wcześniej wystarcza natychmiastowa ikona stanu.

Foldery zachowują slot pierwszego wykrycia pomiędzy kolejnymi snapshotami;
chwilowe zniknięcie nie usuwa zapamiętanej pozycji, ale nie zostawia pustego
wiersza. Ta sama reguła stabilizuje kolejność serwerów we wszystkich
rendererach wspólnego modelu. Wails grupuje foldery pod nagłówkiem serwera, a
wewnątrz rozdziela własne, gościnne i niezaklasyfikowane. WebView dostaje tylko
bezpieczną etykietę własności, nigdy surowy identyfikator realmu.

Kolory panelu dziedziczą jasny lub ciemny motyw systemowy przez
`prefers-color-scheme`. Radar w bannerze jest animacją CSS należącą do tego
frontendu, nie assetem Wails ani zewnętrznym GIF-em.

Daemon projektuje dla każdego repozytorium własny harmonogram pracy:
`cycle_id`, fazę oraz czas ostatniego i następnego ticka. Wails zachowuje ten
zegar na potrzeby diagnostyki i prowadzenia badge, ale nie pokazuje użytkownikowi
jawnego odliczania ani nie używa czasu jako dowodu zakończenia działania. Lock,
unlock i zwolnienie rezerwacji tworzą
badge w wspólnym modelu GUI. Po sukcesie badge przechodzi w „Potwierdzanie
aktualnego stanu” i znika dopiero po pełnym snapshotcie, którego pobieranie
zaczęło się po zakończeniu operacji. Refresh sprzed operacji ani częściowy
event repozytorium nie może przedwcześnie skasować badge. Dla obecnych mutacji
lock badge wymaga dodatkowo znanej, autorytatywnej listy rezerwacji oraz zmiany
licznika we właściwym kierunku względem stanu sprzed gestu.

Proces tworzy własny tray Wails z ikoną bieżącego stanu, liczbą repozytoriów i
blokad. Pozycja `Pokaż panel` przywraca okno, a jego zamknięcie ukrywa je do
traya. Podmenu `FileES` zawiera kontrolowany restart oraz `Zakończ…`. Obie
akcje przechodzą przez wspólny kontroler i IPC demona, wymagają potwierdzenia
z możliwością `Anuluj` i dotyczą pary daemon + GUI, a nie samego renderera.

Trybik w nagłówku każdego panelu serwera otwiera pojedyncze, niezależne okno
`Ustawienia FileES` już w kontekście jego `server_id`. Kolejne otwarcie zmienia
kontekst istniejącego okna zamiast mnożyć WebView. `SettingsService` projektuje
gotowy model serwera i folderów; JavaScript nie wylicza capabilities ani nie
woła IPC. Pierwszą aktywną mutacją jest limit czasu transferu. Po wyborze wraca
ona do wspólnego kontrolera, używa natywnego promptu i adaptera IPC, a badge
znika dopiero wtedy, gdy pełny snapshot pokaże żądaną liczbę minut. Pozostałe
ustawienia są na tym etapie informacyjne i nie mają martwych przycisków.

Trybik w każdym wierszu folderu otwiera trzecie, pojedyncze okno
`Działania folderu FileES` pod `/repository.html`. Żądanie wspólnego kontrolera
jest ograniczone do jednej pary `server_id` + `repo_id`; `RepositoryService`
ponownie odrzuca obcy kontekst i akcję nieobecną w projekcji. Okno obsługuje
połączenie nieprzypiętego folderu, odpięcie lokalne, trwałe usunięcie oraz
pełną listę udziałów publicznych. Pickery, hasła i potwierdzenia destrukcyjne
pozostają po stronie wspólnego kontrolera i natywnej platformy. Attach,
detach i delete utrzymują badge do pełnego snapshotu pokazującego odpowiednio
folder przypięty, odpięty albo repozytorium w trwałym stanie
`server_deleted`. Usunięte repo pozostaje do końca retencji w osobnej grupie
„Usunięte · archiwa”; renderer odlicza lokalnie przekazany przez daemon
`retain_until`, pokazuje niezależny stan czyszczenia `.svn` i oferuje
„Pobierz archiwum” wyłącznie przy wydanej capability recovery. Lista udziałów po każdej
mutacji jest ponownie pobierana przez IPC przed kolejnym pokazaniem okna.

Wymagania:

- Go 1.25 lub nowszy;
- Wails v3.0.0-beta.6;
- WebView2 Runtime na Windows;
- GTK4 i WebKitGTK 6.0 na Linux (wariant GTK3 jest osobnym build tagiem Wails).

Bindings po zmianie publicznej metody lub modelu usługi:

```powershell
wails3 generate bindings -b -d cmd/filees-gui-wails/frontend/bindings ./cmd/filees-gui-wails
```

Build testowy na Windows:

```powershell
go build -tags production -ldflags '-H=windowsgui' -o tmp/filees-gui-wails.exe ./cmd/filees-gui-wails
```

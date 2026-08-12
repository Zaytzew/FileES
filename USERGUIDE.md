# FileES GUI — podręcznik użytkownika

**Wersja:** 0.1.0  
**Dotyczy:** `filees-gui` (aplikacja tray); daemon `filees` opisany jest osobno w README.md

---

## Czym jest FileES GUI

`filees-gui` to ikona w zasobniku systemowym (tray), która pozwala obserwować stan synchronizacji i wykonywać podstawowe operacje — bez otwierania terminala. Demon `filees` odpowiada za całą synchronizację i działa niezależnie; zamknięcie GUI nie zatrzymuje ani nie zakłóca synchronizacji.

---

## Wymagania

### Linux
- Pulpit z obsługą SNI (GNOME wymaga rozszerzenia **AppIndicator/KStatusNotifierItem**; KDE, XFCE, MATE działają bez dodatków)
- `zenity` lub `kdialog` — do wyboru plików przy operacjach blokowania
- `yad` — do okna „Zarządzaj serwerem…” i dialogów dostępu/widoczności strefy
- `xdg-open` — do otwierania katalogów
- `notify-send` — opcjonalnie, do powiadomień systemowych; brak narzędzia nie zatrzymuje GUI

### Windows
- Windows 10 lub nowszy
- PowerShell — do wyboru plików i powiadomień toast
- instalacja MSI — wymagana do poprawnej rejestracji AUMID i podpisanych powiadomień toast; sam `filees-gui.exe` może działać jako tray, ale bez gwarancji powiadomień

---

## Instalacja

### Linux (bundle z `build-gui.sh`)

```bash
# rozpakować bundle filees-gui-linux-amd64/
chmod +x install-user.sh
./install-user.sh                   # instaluje do ~/.local/
ENABLE_AUTOSTART=1 ./install-user.sh  # instaluje + włącza autostart
```

Skrypt kopiuje binary do `~/.local/bin/`, ikonę do `~/.local/share/icons/` i plik desktop do `~/.local/share/applications/`.

### Windows (MSI)

Uruchomić `build-msi.ps1` w PowerShellu na maszynie z WiX Toolset (`wix`; sprawdzone z WiX v7), następnie zainstalować wygenerowany `.msi`. Instalacja jest per-user — nie wymaga uprawnień administratora. Skrót w menu Start ma ustawiony atrybut `System.AppUserModel.ID`, który umożliwia powiadomienia toast. Przy pierwszym użyciu WiX v7 trzeba jednorazowo zaakceptować jego EULA: `wix eula accept wix7`.

---

## Uruchamianie

```bash
filees-gui                           # łączy z domyślnym socketem daemona
filees-gui --socket /ścieżka/daemon.sock  # niestandardowy socket
filees-gui --version                 # pokazuje wersję GUI i kończy działanie
```

GUI można uruchomić przed daemonem — będzie czekać na połączenie z narastającym interwałem (1 s → 2 s → 5 s → 10 s → 30 s).

GUI chroni się przed wielokrotnym uruchomieniem w obrębie sesji użytkownika. Druga instancja kończy się przed utworzeniem ikony tray — także wtedy, gdy podano jej inny socket.

---

## Autostart

Autostart uruchamia GUI po zalogowaniu użytkownika i jest niezależny od daemona.

```bash
filees-gui --autostart enable    # włącz autostart
filees-gui --autostart disable   # wyłącz autostart
filees-gui --autostart status    # sprawdź bieżący stan
```

Przykładowy wynik `status`:

```
autostart: enabled (/home/user/.config/autostart/filees-gui.desktop)
autostart: enabled-stale (/home/user/.config/autostart/filees-gui.desktop)
autostart: disabled (/home/user/.config/autostart/filees-gui.desktop)
```

- `enabled` — wpis uruchamia bieżący executable z aktualnymi argumentami;
- `enabled-stale` — wpis istnieje, ale wskazuje inną ścieżkę lub argumenty; napraw go poleceniem `filees-gui --autostart enable` uruchomionym z docelowej instalacji;
- `disabled` — autostart jest wyłączony.

Na Linuksie autostart tworzy plik `.desktop` w `~/.config/autostart/`. Na Windows zapisuje wpis w `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`.

> Przed odinstalowaniem MSI na Windows należy wyłączyć autostart poleceniem `--autostart disable`. Instalator nie usuwa wpisu rejestru automatycznie.

---

## Ikona tray — znaczenie stanów

| Ikona | Stan | Znaczenie |
|-------|------|-----------|
| zielona | **Aktywne** | Wszystkie repozytoria zsynchronizowane i online |
| pomarańczowa | **Praca w toku** | Trwa commit, aktualizacja, inicjalizacja lub baselining |
| szara | **Offline** | Co najmniej jedno repozytorium nie ma połączenia z serwerem |
| czerwona | **Wymaga uwagi** | Konflikt, degradacja lub błąd wymagający działania użytkownika |
| przyciemniona/brak | **Brak połączenia** | GUI nie może połączyć się z daemonem |

Gdy jest wiele repozytoriów, ikona pokazuje najpoważniejszy stan spośród wszystkich. Repozytorium zdrowe nie maskuje problemu w innym.

---

## Menu tray

Kliknięcie ikony otwiera menu. Układ jest stały; elementy wyszarzone to informacje (nie są klikalne).

### Nagłówek

```
FileES — Połączono          ← stan połączenia (wyszarzony)
Ostatnia aktualizacja: 14:32:01   ← czas ostatniego odświeżenia
─────────────────────────
```

Możliwe etykiety stanu:
- **Połączono** — aktywna sesja z daemonem
- **Odświeżanie** — sesja aktywna, dane właśnie odświeżane po reconnect (stan przejściowy)
- **Brak połączenia** — demon nieosiągalny; dane z poprzedniej sesji mogą być widoczne jako nieaktualne

Gdy dane są nieaktualne, czas odświeżenia uzupełniany jest dopiskiem *(dane nieaktualne)*.

### Lista repozytoriów

Każde repozytorium jest podmenu z nazwą i bieżącym stanem:

```
▶ projektA — Aktywne
    Stan: Aktywne
    Rewizja: 1042 / 1042
    Oczekujące zmiany: 3
    ─────────────────────
    Otwórz katalog
    Zablokuj pliki…
    Odblokuj pliki…
```

Gdy brak repozytoriów, w miejscu listy pojawia się wpis *Brak repozytoriów*.

### Dziennik

Sekcja jest widoczna, gdy daemon udostępnia aktywność albo błędy. Łączy oba
rodzaje wpisów od najnowszego, zamiast dzielić je na dwie rzadko kompletne
listy:

```
▶ Dziennik · ⚠ 1
    ⚠ BŁĄD · Dokumenty — [LOCK-2001] Plik jest zablokowany
    Dokumenty / projekt.dwg — opublikowano · r1042
    Otwórz log…
```

Tray pokazuje maksymalnie 12 zagregowanych wpisów. Wiele ścieżek tej samej
rewizji lub etapu może zostać złączonych, a nieudana aktywność nie dubluje
powiązanego błędu. Wpisy są informacyjne; tooltip zawiera szczegóły lub
wskazówkę. **Otwórz log…** pokazuje cały dostępny snapshot w natywnym oknie.
Na Windows błędy są dodatkowo pogrubione i ciemnoczerwone.

### Akcje globalne

```
─────────────────────────
Połącz ponownie
Aktualizacja klienta — w przygotowaniu
Uruchom FileES ponownie…
Zamknij FileES…
```

- **Połącz ponownie** — wymusza natychmiastową próbę połączenia z daemonem, pomijając czas oczekiwania backoffu. Działa zarówno przy braku połączenia, jak i w trakcie sesji (resetuje sesję).
- **Aktualizacja klienta — w przygotowaniu** — nieaktywne miejsce dla przyszłego
  UX aktualizacji; gdy daemon zgłosi rzeczywiście dostępne, podpisane wydanie,
  zastępuje je aktywne menu aktualizacji.
- **Uruchom FileES ponownie…** — kontrolowanie opróżnia kolejkę i zatrzymuje
  runtime, po czym uruchamia ponownie daemon i GUI.
- **Zamknij FileES…** — kontrolowanie zatrzymuje cały stack kliencki: daemon,
  watchery i GUI.

---

## Stany repozytorium

| Etykieta w menu | Znaczenie |
|-----------------|-----------|
| Aktywne | Repozytorium zsynchronizowane, operacje normalne |
| Praca w toku | Trwa commit lub aktualizacja |
| Inicjalizacja | Pierwsze uruchomienie — tworzenie kopii roboczej |
| Budowanie stanu bazowego | Generowanie stanu bazowego do śledzenia zmian |
| Wstrzymane | Synchronizacja chwilowo wstrzymana |
| Zatrzymywanie | Repozytorium jest wyłączane |
| Offline | Brak połączenia z serwerem SVN |
| Wymaga uwagi | Konflikt, degradacja lub błąd wymagający działania |
| Stan nieznany | Nierozpoznany stan — demon ma nowszy protokół |

**Rewizja** — para `lokalna / HEAD`. Gdy są równe, kopia robocza jest aktualna. Różnica oznacza trwającą lub oczekującą aktualizację.

**Oczekujące zmiany** — liczba plików zmodyfikowanych lokalnie, czekających na commit.

---

## Dodawanie lokalnego folderu jako nowego repozytorium

W podmenu serwera wybierz **Dodaj pierwszy/kolejny folder do FileES…**, wskaż
istniejący folder i potwierdź jego nazwę. Tworzenie repozytorium oraz pierwszy
commit działają w tle. Dla dużego folderu może to potrwać kilka minut.

Po utworzeniu bytu serwerowego, ale przed końcem pierwszego commitu, Ustawienia
pokazują wybraną ścieżkę i stan **import początkowy w toku**. Nie jest to
repozytorium nieprzypięte: FileES trwale zajmuje ten lokalny root, lecz do czasu
zakończenia importu nie uruchamia synchronizacji ani operacji na plikach.

---

## Dołączenie do istniejącej strefy i wybór repozytoriów

Administrator może wystawić zaproszenie do już istniejącej strefy. Po
zaproszeniu i OTP klient dziedziczy jej tożsamość oraz prawa, lecz nie pobiera
automatycznie wszystkich repozytoriów na dysk.

1. Otwórz podmenu serwera i wybierz **Zarządzaj serwerem…**.
2. Ustawienia pokazują wszystkie repozytoria strefy: podłączone i niepodłączone.
3. Na Windows zaznacz jedno lub wiele niepodłączonych repozytoriów i wybierz
   **Połącz**. Na Linux wybierz pojedynczy wiersz.
4. Dla każdego repozytorium wskaż albo utwórz lokalny folder.
5. Po akceptacji wiersz natychmiast pokazuje ścieżkę i `łączenie…`. Pierwszy
   checkout działa w tle; końcowy sukces albo błąd pojawi się w powiadomieniu.

Stary, nadal zaznaczony wiersz już podłączonego repozytorium nie blokuje
podłączania kolejnego. Nie wybieraj ponownie tego samego folderu, gdy wiersz ma
stan `łączenie…`.

---

## Uprawnienia gości i udostępnienia publiczne

W **Zarządzaj serwerem…** właściciel repozytorium ma dwie odrębne akcje:

- **Uprawnienia gości** pokazuje widoczne strefy oraz każdą ukrytą strefę,
  która nadal ma aktywny grant do tego repozytorium. Kolumna „Aktualne
  uprawnienie” pokazuje brak, tylko odczyt albo odczyt i zapis. Można ustawić
  `r`, `rw` lub cofnąć dostęp; każda zmiana wymaga potwierdzenia.
- **Udostępnienia publiczne** pokazuje kanały wydawania plików. Widać adres,
  stan, folder źródłowy, odbiorców, ochronę hasłem oraz `HEAD` albo zamrożoną
  rewizję. Dostępne są **Nowe**, **Edytuj**, **Cofnij** i **Usuń**.
- Kolumna **Edycja** pokazuje, czy repozytorium pracuje zwykle, czy **wymaga
  wypożyczenia**. Właściciel podłączonego repozytorium może użyć akcji
  **Zasady edycji**, aby przełączyć tę politykę dla wszystkich komputerów.
  Gość widzi stan, ale nie może zmienić polityki całego repozytorium.

Tworzenie albo edycja kanału wymaga podłączonej lokalnej kopii roboczej:

1. Wybierz root WC albo dowolny folder wewnątrz niego. FileES zbierze zwykłe
   pliki rekurencyjnie wraz z rozmiarami; `.svn` i `.filees` są pomijane,
   symlinki i ponad 4096 plików są odrzucane. Pusty folder jest poprawnym
   placeholderem; po dodaniu do niego plików trzeba jawnie edytować kanał.
2. Dla nowego kanału wpisz końcówkę adresu (`slug`): 3–64 małe litery, cyfry
   lub pojedyncze myślniki.
3. Opcjonalnie wpisz adresy e-mail oddzielone przecinkami lub średnikami.
   Taki kanał jest zamknięty i każdy odbiorca dostaje osobny token. Puste pole
   tworzy kanał otwarty.
4. Kanał otwarty może dostać wspólne hasło (co najmniej 8 znaków). Hasło jest
   hashowane lokalnie; plaintext nie trafia do serwera ani dziennika. Przy
   edycji można zachować dotychczasowe hasło bez ponownego wpisywania.
5. Puste pole rewizji śledzi `HEAD`; dodatni numer zamraża kanał na tej rewizji.

Odbiorca widzi pod publicznym adresem zwijane drzewo folderów podobne do widoku
„Szczegóły” w Eksploratorze: nazwę, typ, rozmiar i przycisk pobrania. Widok nie
wykonuje JavaScriptu i w pierwszej wersji nie generuje miniaturek zdjęć. Kanały
utworzone przez starszego klienta pozostają zgodne, ale mogą pokazywać kreskę
zamiast nieznanego rozmiaru do czasu ich edycji.

**Cofnij** natychmiast przestaje wydawać pliki, ale zachowuje rekord i adres.
**Usuń** usuwa politykę kanału; adres pozostaje zarezerwowany jako tombstone i
nie może zostać przejęty przez późniejszy kanał.

---

## Blokowanie i odblokowanie plików

`Zablokuj pliki…` i `Odblokuj pliki…` są dostępne w podmenu repozytorium,
gdy daemon zgłasza odpowiednie capabilities. W zwykłym repozytorium wykonują
blokadę SVN; w repozytorium oznaczonym w kolumnie **Edycja** jako **wymaga
wypożyczenia** nabywają lub zwalniają edit-passport. Politykę ustawia raz
właściciel repozytorium i otrzymują ją wszystkie podłączone instalacje — nie
konfiguruje się jej osobno na każdym komputerze. Udane wypożyczenie przywraca
plikowi lokalną zapisywalność. Blokada trzymana przez inną osobę pokazuje jej
alias oraz czas wygaśnięcia zamiast kończyć się bez komunikatu.

Operacja dostępna wyłącznie gdy:
- GUI jest połączone z daemonem (**Połączono**, nie **Odświeżanie**)
- Daemon zadeklarował capability `repo.lock` / `repo.unlock`

W stanie **Odświeżanie** akcje blokowania i odblokowania są ukryte. Pojawiają się dopiero po odebraniu świeżego snapshotu i zmianie stanu na **Połączono**.

### Przebieg operacji

1. Kliknąć **Zablokuj pliki…** lub **Odblokuj pliki…** w podmenu repozytorium.
2. Otworzy się natywny selektor plików — **zenity** (Linux) lub okno Windows Forms (Windows). Można wybrać wiele plików.
3. Selektor ogranicza wybór do katalogu repozytorium; pliki spoza niego są odrzucane.
4. Po zatwierdzeniu demon wykonuje operację. Wynik pojawi się jako powiadomienie systemowe:
   - sukces: *Zablokowano N plik(ów)* / *Odblokowano N plik(ów)*
   - błąd: np. *Błąd operacji (lock) — LOCK-2001*, z lokalizowanym opisem i bezpieczną wskazówką dalszego postępowania

Anulowanie selektora (ESC lub przycisk Anuluj) nie wykonuje żadnej operacji i nie pokazuje powiadomienia.

Dla tego samego repozytorium można mieć aktywną tylko jedną operację naraz. Kliknięcie ponownie podczas trwającej operacji jest ignorowane.

## Lista rezerwacji

W nagłówku menu wybierz **Lista rezerwacji plikowych…**. Pozycja jest aktywna,
gdy lokalne kopie robocze mają co najmniej jedną rezerwację, i otwiera jedną
listę blokad SVN ze wszystkich aktywnych serwerów. Wiersz podaje serwer, kopię
roboczą, ścieżkę względną, właściciela i czas `GG:MM DD-MM-RRRR`. Listę można
odświeżyć bez zamykania okna. To nie jest globalny panel administracyjny:
blokady w repozytorium bez lokalnej working copy nie pojawią się na liście.

**Zwolnij** zawsze wymaga potwierdzenia. Gdy folder zawiera lokalne zmiany lub
rezerwacja ma aktywny paszport edycji, pojawia się dodatkowe ostrzeżenie o
możliwych niezapisanych danych. FileES nie sprawdza, czy edytor ma „otwarty
plik”, ponieważ nowoczesne edytory często zapisują przez atomową podmianę i
taki wskaźnik byłby niewiarygodny. Rezerwacja aktywna na innym urządzeniu lub
należąca do innego użytkownika ma działanie **Poproś o zwolnienie (wkrótce)**;
nie można jej dziś zwolnić z tego klienta. **Zwolnij wszystko** wymaga jednego
potwierdzenia i zwalnia wyłącznie moje rezerwacje, każdą z aktualnym tokenem.
Po zatwierdzeniu daemon ponownie sprawdza blokadę i jej token, więc odświeżony
lub podmieniony wiersz nie zwolni przypadkowo innej rezerwacji.

### Stały alias

Alias jest publiczną nazwą realmu widoczną przy rezerwacjach; nie jest adresem
e-mail ani UID. **Ustaw stały alias…** pojawia się tylko dla świeżej, pustej
strefy. Klient dołączony do istniejącej strefy dziedziczy jej alias i nie
powinien być pytany o nową nazwę. Jeżeli repozytoria są już widoczne, a aliasu
brakuje, jest to niepełna projekcja serwera — nie próbuj tworzyć innego aliasu.
Do czasu odświeżenia FileES ukrywa **Widoczność…** i operacje lock wymagające
tożsamości strefy.

---

## Otwieranie katalogu

**Otwórz katalog** otwiera lokalny katalog repozytorium w domyślnym menedżerze plików (Linux: `xdg-open`, Windows: Explorer). Opcja jest wyszarzona, jeśli demon nie podał lokalnej ścieżki dla tego repozytorium.

---

## Przeniesiona albo brakująca kopia robocza

Nie zmieniaj nazwy ani nie usuwaj rootu aktywnej kopii poza FileES. Na Windows
działający daemon blokuje taki rename/delete uchwytem systemowym. Po zamknięciu
FileES system nie może już tego wymusić, dlatego następny start sprawdza `.svn`,
URL repozytorium i marker tożsamości.

Jeżeli poprawna kopia została przeniesiona:

1. Repozytorium przejdzie do **Wymaga uwagi** z operacją
   `working_copy_missing`; FileES nie utworzy pustego zamiennika.
2. Otwórz **Zarządzaj serwerem…** i wybierz **Wskaż kopię**.
3. Wskaż istniejący root z `.svn`. FileES sprawdzi URL i tożsamość, a następnie
   trwale przepnie lokalną ścieżkę bez checkoutu.

Lokalne, niecommitowane zmiany są dozwolone i pozostają nietknięte. Nie używaj
**Połącz** do odzyskania przeniesionej WC — ta akcja oznacza pierwszy checkout
repozytorium, które nie ma jeszcze lokalnej kopii na tym kliencie.

---

## Odłączanie folderu

W podmenu opcjonalnie podłączonego repozytorium są dwie różne operacje:

- **Odłącz folder…** — po jednym potwierdzeniu zatrzymuje synchronizację tej
  working copy. FileES usuwa z rootu wyłącznie katalogi `.svn` i `.filees`.
  Wszystkie dokumenty i pozostałe katalogi zostają na dysku; wynik jest
  zwykłym folderem. Operacja działa również wtedy, gdy samo repo jest offline;
  trwały tombstone zapobiega ponownemu podłączeniu starego wpisu z
  `config.json` po restarcie.
- **Odłącz trwale…** — jest dostępne tylko dla repozytorium własnego realmu i
  wymaga dwóch oddzielnych potwierdzeń. Serwer wycofuje dostęp i usuwa FSFS.
  Przy retencji większej od zera najpierw tworzy oraz weryfikuje pełny dump,
  usuwa FSFS natychmiast i zachowuje wyłącznie dump przez skonfigurowaną liczbę
  dni. Retencja `0` to tryb panic: natychmiastowe usunięcie bez dumpa i bez
  serwerowej kopii odzyskowej.

Repozytoriów oznaczonych jako **Wymagane przez serwer** nie można odłączyć ani
usunąć tą ścieżką. Odłączenie trwa do końcowego potwierdzenia daemona; nie
zamykaj FileES w trakcie operacji.

Po odłączeniu zachowany katalog jest zwykłym folderem, a nie kopią roboczą.
Ponowne **Połącz** wykonuje pierwszy checkout do nowego lub pustego folderu;
nie scala automatycznie zachowanych plików z repozytorium. Odrzucenie wybranego
celu albo konflikt lokalnego lifecycle jest pokazywany w modalnym oknie błędu,
nie tylko w powiadomieniu Windows.

---

## Restart i zamykanie

**Uruchom FileES ponownie…** i **Zamknij FileES…** obejmują cały stack
kliencki, nie tylko ikonę. Przed zakończeniem daemon zatrzymuje aktywne
repozytoria i wykonuje końcowy scan/flush.

Pliki zmienione, gdy FileES jest wyłączony, nie przepadają: po następnym
uruchomieniu watcher ładuje trwały manifest, porównuje go z bieżącym
filesystemem i traktuje różnice jako zwykłe dodania, modyfikacje lub usunięcia.

---

## Rozwiązywanie problemów

### Ikona nie pojawia się (GNOME)

GNOME nie obsługuje SNI natywnie. Zainstaluj rozszerzenie **AppIndicator and KStatusNotifierItem Support** (dostępne na extensions.gnome.org). Po instalacji wyloguj się i zaloguj ponownie.

### Brak połączenia — demon działa

Sprawdź, czy `filees-gui` używa właściwego socketu:

```bash
filees-gui --autostart status     # sprawdź, czy wpis jest aktualny (enabled / enabled-stale)
filees status                     # weryfikuje, czy demon odpowiada
filees-gui --socket /ścieżka/do/daemon.sock
```

Polecenie `--autostart status` nie wypisuje argumentu socketu; porównuje jednak cały zapisany wpis z oczekiwaną komendą. Wynik `enabled-stale` oznacza, że ścieżka executable lub argumenty wymagają odświeżenia przez ponowne `--autostart enable`.

Domyślna ścieżka socketu: `$XDG_RUNTIME_DIR/filees.sock` lub `~/.filees/daemon.sock`.

### Selektor plików nie otwiera się (Linux)

GUI wymaga `zenity` lub `kdialog`. Sprawdź dostępność:

```bash
which zenity
which kdialog
```

Zainstaluj brakujące narzędzie pakietem systemowym (`apt install zenity`, `dnf install zenity`, itp.).

### Okno „Zarządzaj serwerem…” nie otwiera się (Linux)

To osobne okno (razem z „Widoczność…” i „Dostęp stref…”) wymaga `yad`, nie
`zenity`. Sprawdź dostępność (`which yad`) i zainstaluj brakujące narzędzie
pakietem systemowym (`apt install yad`, `dnf install yad`, itp.).

### Powiadomienia toast nie pojawiają się (Windows)

Powiadomienia wymagają zarejestrowanego AUMID. Upewnij się, że `filees-gui.exe` był zainstalowany przez MSI (tworzy skrót Start Menu z `System.AppUserModel.ID = ATMProjekt.FileES`). Uruchomienie samego `.exe` bez instalatora nie zarejestruje AUMID i powiadomienia będą pomijane.

### Wiele ikon w trayu

Aktualna wersja blokuje drugą instancję przed utworzeniem ikony. Jeśli mimo to widoczne są dwie ikony, jedna może pochodzić ze starszej wersji GUI albo z innej sesji użytkownika. Zakończ stary proces `filees-gui` z poziomu jego sesji lub menedżera procesów, sprawdź wersję poleceniem `filees-gui --version` i uruchom ponownie bieżącą instalację.

### „Widoczność…” nie jest dostępna albo folder pokazuje „brak aliasu”

Tożsamość istniejącej strefy nie dotarła jeszcze w projekcji. Nie ustawiaj
nowego aliasu. Odczekaj odświeżenie, użyj **Połącz ponownie**, a jeżeli stan
wraca po restarcie — administrator musi zrekoncyliować widok klienta po stronie
serwera. GUI celowo nie otwiera dialogu widoczności bez kanonicznego aliasu.

### Repozytorium pokazuje `working_copy_missing`

Sprawdź, czy kopia nie została przeniesiona lub czy dysk jest podłączony. Jeśli
znasz jej nową lokalizację, użyj **Wskaż kopię**. Nie twórz ręcznie pustego
katalogu pod starą nazwą; nie jest on prawidłową WC i nie zostanie uruchomiony.

---

## Znane ograniczenia wersji 0.1.0

- **Aktywacja powiadomień**: powiadomienia są informacyjne; kliknięcie nie otwiera jeszcze katalogu ani szczegółów błędu. Nie wykonuje też żadnej operacji mutującej.
- **MSI Windows**: przed odinstalowaniem należy wykonać `filees-gui --autostart disable`; automatyczne usunięcie utworzonej przez aplikację wartości `HKCU\...\Run` pozostaje otwartą bramką instalatora.
- **Odbiór natywny**: build MSI oraz instalacja i deinstalacja per-user zostały ręcznie potwierdzone na Windows 11. Każde kolejne wydanie nadal wymaga testów install/upgrade/uninstall na Windows 10/11.
- **Wstrzymanie / Sync now**: funkcje nieobecne w bieżącym kontrakcie daemona;
  pojawią się po dodaniu odpowiednich capabilities.

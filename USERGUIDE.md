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

### Ostatnie błędy

Sekcja widoczna tylko gdy daemon obsługuje `error.list`. Wyświetla błędy od najnowszego:

```
▶ Ostatnie błędy
    [ERROR] LOCK-2001 — Operacja blokady nie powiodła się   ← tooltip: Wymagane działanie użytkownika
    [WARN]  NET-4007 — Brak połączenia z serwerem           ← tooltip: Ponowienie nastąpi później
    Brak błędów                                 ← gdy dziennik pusty
```

Wpisy są wyszarzone (informacyjne). Tooltip każdego wpisu zawiera wskazówkę jak postępować.

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

## Blokowanie i odblokowanie plików

`Zablokuj pliki…` i `Odblokuj pliki…` są dostępne w podmenu repozytorium,
gdy daemon zgłasza odpowiednie capabilities. W zwykłym repozytorium wykonują
blokadę SVN; w repozytorium z `edit_passports` nabywają lub zwalniają
edit-passport. W obu przypadkach operacja zapobiega równoległej edycji, a w
trybie passportu udane zablokowanie przywraca plikowi lokalną zapisywalność.

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

W podmenu serwera wybierz **Rezerwacje plików…**, aby otworzyć natywne okno z
blokadami SVN widocznymi z folderów roboczych podłączonych lokalnie do tego
serwera. Wiersze są uporządkowane według working copy i ścieżki; można je
odświeżyć bez zamykania okna. To nie jest globalny panel administracyjny:
blokady w repozytorium bez lokalnej working copy nie pojawią się na liście.

**Zwolnij** zawsze wymaga potwierdzenia. Gdy folder zawiera lokalne zmiany lub
rezerwacja ma aktywny paszport edycji, pojawia się dodatkowe ostrzeżenie o
możliwych niezapisanych danych. FileES nie sprawdza, czy edytor ma „otwarty
plik”, ponieważ nowoczesne edytory często zapisują przez atomową podmianę i
taki wskaźnik byłby niewiarygodny. Rezerwację aktywną na innym urządzeniu
oznaczono jako niedostępną; nie można jej tu zwolnić. Po zatwierdzeniu daemon
ponownie sprawdza blokadę i jej token, więc odświeżony lub podmieniony wiersz
nie zwolni przypadkowo innej rezerwacji.

---

## Otwieranie katalogu

**Otwórz katalog** otwiera lokalny katalog repozytorium w domyślnym menedżerze plików (Linux: `xdg-open`, Windows: Explorer). Opcja jest wyszarzona, jeśli demon nie podał lokalnej ścieżki dla tego repozytorium.

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

### Powiadomienia toast nie pojawiają się (Windows)

Powiadomienia wymagają zarejestrowanego AUMID. Upewnij się, że `filees-gui.exe` był zainstalowany przez MSI (tworzy skrót Start Menu z `System.AppUserModel.ID = ATMProjekt.FileES`). Uruchomienie samego `.exe` bez instalatora nie zarejestruje AUMID i powiadomienia będą pomijane.

### Wiele ikon w trayu

Aktualna wersja blokuje drugą instancję przed utworzeniem ikony. Jeśli mimo to widoczne są dwie ikony, jedna może pochodzić ze starszej wersji GUI albo z innej sesji użytkownika. Zakończ stary proces `filees-gui` z poziomu jego sesji lub menedżera procesów, sprawdź wersję poleceniem `filees-gui --version` i uruchom ponownie bieżącą instalację.

---

## Znane ograniczenia wersji 0.1.0

- **Aktywacja powiadomień**: powiadomienia są informacyjne; kliknięcie nie otwiera jeszcze katalogu ani szczegółów błędu. Nie wykonuje też żadnej operacji mutującej.
- **MSI Windows**: przed odinstalowaniem należy wykonać `filees-gui --autostart disable`; automatyczne usunięcie utworzonej przez aplikację wartości `HKCU\...\Run` pozostaje otwartą bramką instalatora.
- **Odbiór natywny**: build MSI oraz instalacja i deinstalacja per-user zostały ręcznie potwierdzone na Windows 11. Każde kolejne wydanie nadal wymaga testów install/upgrade/uninstall na Windows 10/11.
- **Wstrzymanie / Sync now**: funkcje nieobecne w bieżącym kontrakcie daemona;
  pojawią się po dodaniu odpowiednich capabilities.

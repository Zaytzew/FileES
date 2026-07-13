# FileES

Daemon synchronizujący lokalne katalogi z repozytorium SVN. Przeznaczony dla zespołów pracujących na plikach binarnych (grafika, modele 3D, zasoby projektowe). SVN jest tu warstwą transportową i magazynem — semantyka kontroli wersji jest drugorzędna.

Docelowy UX: automat w trayu, który niewidocznie utrzymuje pliki zsynchronizowane z serwerem. Użytkownik nie musi wiedzieć, że pod spodem działa SVN.

---

## Wymagania

- Go 1.24+
- Klient SVN (`svn`) dostępny w `PATH`
- Dostęp do repozytorium svnserve

---

## Budowanie

```bash
go build -o filees ./cmd/filees
```

---

## Konfiguracja

Daemon szuka pliku `config.json` w katalogu roboczym. Plik zawiera tablicę repozytoriów:

```json
[
  {
    "id":              "projectA",
    "repo_url":        "svn://server/repo/trunk",
    "local_path":      "/home/user/projectA",
    "username":        "",
    "password":        "",
    "commit_interval": "1m",
    "watch_interval":  "30s",
    "poll_interval":   "30s",
    "max_batch_files": 100,
    "lock_first":      false,
    "shout_patterns":  ["\\.psd$", "\\.blend$", "\\.obj$"],
    "rate_limit_shout":"5m",
    "commit_tiers": [
      {"max_mb": 1,  "interval": "2m"},
      {"max_mb": 10, "interval": "5m"},
      {"max_mb": 50, "interval": "15m"},
      {"max_mb": 0,  "interval": "24h"}
    ]
  }
]
```

| Pole               | Opis |
|--------------------|------|
| `id`               | Unikalny identyfikator repo (używany w logach i ścieżkach stanu) |
| `repo_url`         | URL repozytorium SVN |
| `local_path`       | Bezwzględna ścieżka do kopii roboczej |
| `username`         | Login SVN (opcjonalny) |
| `password`         | Hasło SVN (opcjonalny) |
| `commit_interval`  | Okno commitów (np. `1m`, `30s`) |
| `watch_interval`   | Interwał skanowania systemu plików |
| `poll_interval`    | Jak często sprawdzać HEAD serwera i pobierać zmiany (`svn update`); domyślnie `30s` |
| `max_batch_files`  | Maks. liczba plików w jednym commicie |
| `lock_first`       | Jeśli `true` — próbuje `svn lock` przed commitem |
| `shout_patterns`   | Wzorce regex; pasujące pliki wyzwalają powiadomienie (ticket) |
| `rate_limit_shout` | Minimalny odstęp między powiadomieniami |
| `commit_tiers`     | Size-adaptive interwały (lista rosnąco wg `max_mb`); pominięte = tylko `commit_interval` |

**`commit_tiers`** — każdy wpis to `{"max_mb": N, "interval": "Xm"}`. Daemon sprawdza sumaryczny rozmiar plików w bieżącym batchu i stosuje minimalny odstęp odpowiedniego tieru. `max_mb: 0` to catch-all (ostatni tier). Przykład: batche < 1 MiB co 2 min, 1–10 MiB co 5 min, 10–50 MiB co 15 min, > 50 MiB co 24h.

Czasy podawane w formacie Go: `30s`, `5m`, `1h`.

---

## Uruchamianie

```bash
./filees                        # uruchamia daemon (domyślnie)
./filees daemon                 # jawne uruchomienie daemona
./filees --config ścieżka/do/config.json
```

Poziom logowania przez zmienną środowiskową:

```bash
FILEES_LOG=debug ./filees
FILEES_LOG=trace ./filees   # łącznie z wywołaniami svn
```

Dostępne poziomy: `silent`, `error`, `warn`, `info` (domyślny), `debug`, `trace`.

Opcjonalny prefix w logach:

```bash
FILEES_LOG_PREFIX=myhost ./filees
```

---

## Polecenia CLI

Daemon nasłuchuje na gnieździe Unix (`$XDG_RUNTIME_DIR/filees.sock` lub `~/.filees/daemon.sock`). Wszystkie subkomendy komunikują się z działającym daemonem przez to gniazdo — nie czytają bezpośrednio plików `.filees/` ani nie wywołują `svn`.

```bash
filees status               # stan wszystkich repozytoriów
filees lock   <plik>...     # założenie blokady SVN
filees unlock <plik>...     # zwolnienie blokady SVN
filees log [N]              # ostatnie N wpisów z dziennika błędów (domyślnie 20)
filees help
```

`lock` i `unlock` obsługują wiele plików naraz i automatycznie grupują je według repozytorium. Ścieżki mogą być relatywne — daemon konwertuje je do absolutnych i weryfikuje, że leżą wewnątrz kopii roboczej.

---

## GUI Tray — koncepcja

`filees-gui` będzie osobnym procesem i cienką warstwą UX nad publicznym kontraktem IPC. GUI nie jest częścią daemona, nie zna SVN i nie przejmuje odpowiedzialności za synchronizację. Zamknięcie GUI nie zatrzymuje daemona ani pracy repozytoriów.

### Twarda granica GUI–daemon

GUI może importować wyłącznie:

- `pkg/ipcclient` — transport i typowane operacje IPC,
- `pkg/contract/v1` — publiczne DTO, stany, zdarzenia i capabilities,
- własne pakiety prezentacji oraz integracji z systemowym trayem.

GUI nie może:

- importować `watcher`, `commit`, `client`, `ipcserver`, `errmap` ani innych pakietów silnika,
- uruchamiać `svn` lub modyfikować kopii roboczej w imieniu daemona,
- czytać `config.json`, `.filees/`, cache, manifestów ani logów błędów bezpośrednio,
- rekonstruować stanu na podstawie logów lub szczegółów tekstowych błędów,
- wywoływać komendy, których daemon nie zgłosił w `capabilities`.

Jedynym wyjątkiem poza IPC są lokalne działania należące do UX, np. otwarcie katalogu repozytorium w menedżerze plików. Nie mogą one zmieniać stanu synchronizacji.

### Model uruchomienia

- daemon działa niezależnie, najlepiej jako usługa użytkownika,
- `filees-gui` może startować wraz z sesją graficzną i łączy się z istniejącym socketem,
- brak daemona jest normalnym stanem UX, a nie awarią samego GUI,
- GUI ponawia połączenie z ograniczonym backoffem, np. `1s → 2s → 5s → 10s → 30s`,
- „Zamknij GUI” kończy tylko tray-app; zatrzymanie daemona będzie osobną akcją dopiero po pojawieniu się odpowiedniej capability.

Po połączeniu GUI wykonuje:

1. `system.hello` i zapisuje capabilities,
2. `events.subscribe`, jeśli capability jest dostępna,
3. `system.status`, `repo.list` oraz `repo.status` dla każdego repozytorium,
4. okresowe odświeżenie snapshotów jako mechanizm naprawczy.

Subskrypcja jest zestawiana przed pobraniem snapshotów, żeby zmiana zachodząca podczas inicjalizacji nie pozostała niezauważona; event odebrany w tym oknie może najwyżej spowodować dodatkowe odświeżenie. Snapshot z `repo.status` jest jedynym autorytatywnym źródłem stanu. Zdarzenia są sygnałem do szybkiego odświeżenia odpowiedniego snapshotu — GUI nie buduje trwałego stanu wyłącznie przez nakładanie eventów. Po reconnect, przerwie w `sequence` albo niepoprawnym evencie wykonywany jest pełny resync.

### Model stanu tray

Stan ikony jest agregatem wszystkich repozytoriów. Obowiązuje stały priorytet, dzięki czemu stan poważniejszy nie jest maskowany przez repozytorium zdrowe:

| Priorytet | Stan ikony | Warunek |
|-----------|------------|---------|
| 1 | brak połączenia | daemon nieosiągalny lub niezgodny protokół |
| 2 | wymagana uwaga | `interaction_required`, `degraded`, konflikt albo błąd wymagający działania |
| 3 | offline | co najmniej jedno repozytorium jest offline |
| 4 | praca w toku | istnieje `current_operation` lub stan przejściowy |
| 5 | gotowe | wszystkie repozytoria są aktywne i online |

Nieznany stan lub nieznana wartość enum jest prezentowana jako bezpieczne „stan nieznany” i powoduje odświeżenie, nigdy crash GUI.

Priorytet agregacji to: `disconnected > error > offline > busy > active`. Przy braku połączenia ostatni snapshot może pozostać widoczny w menu wyłącznie jako oznaczony stan nieaktualny.

### Menu MVP

Menu tray powinno zawierać:

- zagregowany stan daemona i czas ostatniego poprawnego odświeżenia,
- listę repozytoriów ze stanem, connectivity, rewizją i liczbą oczekujących zmian,
- „Otwórz katalog” dla każdego repozytorium,
- `Lock…` i `Unlock…` z wyborem plików wewnątrz danego repozytorium,
- ostatnie błędy z `error.list`, mapowane przez `message_key`, `severity` i `hint`,
- „Połącz ponownie” przy niedostępnym daemonie,
- „Zamknij GUI”.

Elementy zależne od komend mutujących są tworzone wyłącznie na podstawie capabilities. W aktualnym kontrakcie r42 GUI może udostępniać `events.subscribe`, `repo.lock`, `repo.unlock` i `error.list`. `Pause`, `Sync now`, publikowanie zmian i decyzje konfliktowe pozostają ukryte do czasu wdrożenia i zareklamowania ich przez daemon.

### Powiadomienia

Systemowe powiadomienia są wtórne wobec stanu w menu. MVP powinno pokazywać je tylko dla zdarzeń wymagających uwagi, utraty/odzyskania łączności oraz zakończenia operacji istotnej dla użytkownika. Powtarzające się zdarzenia muszą być grupowane i ograniczane czasowo. Kliknięcie powiadomienia otwiera szczegóły repozytorium, nie wykonuje automatycznie operacji mutującej.

### Proponowany podział kodu

```text
cmd/filees-gui/          composition root, lifecycle procesu
internal/gui/app/        stan prezentacji, reconnect, resync, capability gating
internal/gui/tray/       adapter fyne.io/systray, menu i mapowanie ikon
internal/gui/platform/   autostart, powiadomienia, otwieranie katalogu
pkg/ipcclient/           jedyna droga komunikacji z daemonem
pkg/contract/v1/         publiczne typy graniczne
```

Wybraną biblioteką jest `fyne.io/systray`, izolowane jako adapter w `internal/gui/tray`. Jej API nie może przenikać do logiki aplikacji ani kontraktu. MVP obejmuje Linux (SNI; GNOME wymaga rozszerzenia AppIndicator/SNI) oraz Windows 10+. Szczegółowe decyzje platformowe znajdują się w `gui-assumptions.md`.

### Etapowanie implementacji

1. **Rdzeń bez tray** — `internal/gui/app`, interfejs `DaemonClient`, pojedyncza pętla stanu, init, reconnect, resync, debounce oraz test architektoniczny i jednostkowy bez GUI.
2. **Adapter tray** — `internal/gui/tray` na `fyne.io/systray`, pięć ikon, menu renderowane z `ViewModel` oraz intencje użytkownika bez bezpośredniego dostępu do IPC.
3. **Integracje platformowe** — Linux i Windows: autostart, powiadomienia, otwieranie katalogów i natywny wybór wielu plików dla lock/unlock.
4. **Integracja i odbiór MVP** — `cmd/filees-gui`, osadzone zasoby, testy app ↔ fake IPC, testy manualne obu platform oraz weryfikacja restartu daemona, wolnego GUI i wielu repozytoriów.

Etap 1 nie dodaje jeszcze `fyne.io/systray`: najpierw utrwala zachowanie aplikacji i granicę architektoniczną. Szczegółowy zakres każdego etapu oraz checklista znajdują się w `gui-assumptions.md`.

### Zakres pierwszego wydania

Pierwszy pionowy przekrój uznajemy za gotowy, gdy:

- GUI startuje i kończy się niezależnie od daemona,
- poprawnie pokazuje brak połączenia, reconnect i snapshot wielu repozytoriów,
- reaguje na eventy, a po luce sekwencji wykonuje pełny resync,
- pokazuje wyłącznie akcje dostępne w capabilities,
- lock/unlock działa tylko przez `ipcclient` i prezentuje ustrukturyzowane błędy,
- wolny lub zamknięty GUI nie blokuje daemona,
- autostart działa na Linuksie i Windowsie,
- test architektoniczny chroni zakaz importowania pakietów silnika,
- testy modelu prezentacji nie wymagają działającego SVN ani środowiska graficznego.

Poza MVP pozostają: edycja konfiguracji, zarządzanie usługą daemona, `pause/resume`, ręczny sync/publish, interaktywne decyzje konfliktowe, pełne okno aplikacji oraz macOS.

---

## Architektura

```
                    ┌─────────────────────────────────────────────┐
                    │                  daemon                      │
                    │                                             │
  SVN server ◄──svn─┤  Scanner ──events──► Commit Service        │
                    │                            │                │
                    │                     IPC Server              │
                    └──────────────────────┬──────────────────────┘
                                           │ Unix socket
                                    ┌──────┴──────┐
                                    │             │
                                 filees CLI    filees-gui (TBD)
```

Dla każdego repozytorium uruchamiany jest niezależny potok:

```
Scanner (watcher) ──events──► Commit Service ──svn──► SVN server
```

### Scanner (`pkg/watcher`)

Cyklicznie przechodzi drzewo kopii roboczej i wykrywa zmiany:

- Sprawdza mtime i rozmiar pliku
- Jeśli rozmiar ≤ 64 MiB i budżet MD5 pozwala — oblicza hash i porównuje z poprzednim; identyczna zawartość nie generuje eventu
- Pliki większe trafiają do backlogu (`md5.backlog.json`); **backlog worker** (osobna goroutine) oblicza MD5 w tle co 5 s, po jednym pliku, wybierając za każdym razem najmniejszy — wynik trafia z powrotem do `s.cur`, co umożliwia wykrywanie renomowań dużych plików
- Usunięcia są debouncowane 10 minut (ochrona przed chwilowymi ruchami plików)
- W czasie commitu (`commit.busy`) przełącza się w tryb lekki — obserwuje tylko `.filees/tickets/`
- Symlinki są pomijane (FS-0201); widoczne w logach przy poziomie `debug`

**Tryby pracy:**

| Tryb | Opis |
|------|------|
| `Baselining` | Pierwsze uruchomienie — buduje manifest bez emitowania eventów |
| `Active` | Normalny tryb — skanuje i emituje eventy do commit service |

Przejście z Baselining do Active jest automatyczne po pierwszym pomyślnym skanie — nie wymaga zewnętrznej flagi.

### Commit Service (`pkg/commit`)

Zbiera eventy ze skanera w mapie staging i co `commit_interval` wykonuje commit:

1. Snapshot pending zmian (z uwzględnieniem minimalnego opóźnienia dla nowych plików — 5 min)
2. Opcjonalnie: sprawdza rozmiar batcha i stosuje `commit_tiers` (size-adaptive interval)
3. Filtruje przez `svn status` (nie dodaje plików już wersjonowanych, nie usuwa unversioned)
4. `svn add` → `svn delete` → opcjonalnie `svn lock` → `svn commit`
5. Zapisuje numer rewizji do `head.rev`
6. Jeśli commitowane pliki pasują do `shout_patterns` — tworzy ticket powiadomień

Równolegle działa **poller HEAD** (co `poll_interval`, domyślnie 30s):
- `svn info --show-item revision <repo_url>` — pobiera HEAD rewizję serwera
- Jeśli HEAD > lokalnej rewizji — wykonuje `svn update`
- Obsługuje offline i backoff identycznie jak commit
- Po update wykrywa konflikty i uruchamia **reconciliation**

**Reconciliation** (polityka: HEAD wygrywa):
1. Wykrywa pliki z konfliktem w outputcie `svn update` (linie `C ...`)
2. Kopiuje lokalną wersję (`<plik>.mine` lub sam plik) do `!kolizje/<timestamp>_lokalne/`
3. Zapisuje plik `.meta` z metadanymi (oryginalna ścieżka, rozmiar, timestamp)
4. Wykonuje `svn resolve --accept theirs-full` — wersja serwera wygrywa
5. Emituje `RECON-3002` do `errors.jsonl`

Katalog `!kolizje/` jest automatycznie ignorowany przez skaner — nie trafi do commita.

Koalescja eventów w staging:
- `Added + Modified → Added`
- `Deleted + Added → Added` (plik wrócił — traktowany jako nowy)
- `Modified + Deleted → Deleted`

### IPC Server (`pkg/ipcserver`)

Daemon wystawia gniazdo Unix i przyjmuje połączenia od CLI i GUI. Protokół: JSON Lines, format `filees.contract/v1`.

- Każde połączenie obsługuje jedno żądanie i się zamyka (request/response), z wyjątkiem `events.subscribe` — które przełącza połączenie w tryb push (serwer wysyła eventy aż do rozłączenia klienta)
- `RepoState` — live snapshot stanu repozytorium, aktualizowany przez daemon, serwowany klientom bez dostępu do silnika
- Wszystkie ścieżki w `repo.lock`/`repo.unlock` są walidowane — muszą być absolutne i leżeć wewnątrz kopii roboczej (`LOCK-2002` przy naruszeniu)

Zaimplementowane komendy: `system.hello`, `system.status`, `repo.list`, `repo.status`, `repo.lock`, `repo.unlock`, `error.list`, `events.subscribe`.

### IPC Client (`pkg/ipcclient`)

Biblioteka używana przez CLI i GUI. Każde zwykłe wywołanie otwiera nowe połączenie do gniazda, wysyła żądanie i czeka na odpowiedź. `Subscribe` utrzymuje osobne, długotrwałe połączenie eventowe. Klient weryfikuje odpowiedzi (protocol, request_id, status), ACK subskrypcji oraz wymagane pola eventów. Deadline połączenia dziedziczy z kontekstu wywołującego — lock/unlock z 30 s kontekstem dostaje 30 s zamiast domyślnych 10 s; anulowanie kontekstu odblokowuje również handshake i oczekiwanie na kolejny event.

### SVN Client (`pkg/client`)

Wrapper na `svn` CLI. Wszystkie wywołania serializowane przez mutex wewnątrz procesu. Timeout per komenda: 30 minut. Wywoływany wyłącznie przez daemon — CLI nigdy nie woła SVN bezpośrednio.

### Runtime Gates (`pkg/runtime`)

Opcjonalne mechanizmy ograniczające równoległość commitów:

- **HostGate** — limit K równoległych commitów w skali hosta (blokada przez `mkdir`)
- **RepoMutex** — maksymalnie 1 commit naraz per repozytorium

Oba mechanizmy zwracają funkcję release, bezpieczną dla wielu goroutine.

### Error Classifier (`pkg/errmap`)

Klasyfikuje błędy Go na ustrukturyzowane wpisy gotowe do logowania i obsługi przez UI.

```
Classify(err) → Entry{Code, Severity, Hint, Msg, Details}
```

| Code | Severity | Hint | Opis |
|------|----------|------|------|
| `NET-4007` | WARN | RETRY_BACKOFF | Brak połączenia z serwerem |
| `AUTH-4102` | ERROR | ADMIN_ONLY | Błąd uwierzytelnienia |
| `LOCK-2001` | ERROR | REQUIRE_ACTION | Plik zablokowany przez innego użytkownika |
| `COMMIT-3101` | WARN | RETRY_LOCAL | WC nieaktualne — wymagany update |
| `COMMIT-3102` | WARN | RETRY_LOCAL | Ścieżka poza kontrolą wersji |
| `COMMIT-3100` | ERROR | RETRY_LOCAL | Ogólny błąd commita |
| `SYNC-0000` | ERROR | RETRY_LOCAL | Niesklasyfikowany błąd |

`Sink` zapisuje wpisy jako JSON Lines do `<wc>/.filees/logs/errors.jsonl` (jeden obiekt JSON na linię). Format:

```json
{"ts":"2026-07-13T10:00:00Z","scope":"commit:projectA","code":"NET-4007","severity":"WARN","hint":"RETRY_BACKOFF","msg":"Network unreachable — retrying with backoff","details":"..."}
```

### Tickets (`pkg/tickets`)

Tworzy pliki powiadomień w `.filees/tickets/NOTICE-<uuid>.req` w formacie INI. Używane do sygnalizacji zdarzeń do zewnętrznych narzędzi (np. UI w trayu).

Format pliku:
```ini
TYPE=NOTICE
CLIENT=<uuid>
TS=<RFC3339>
ID=<uuid>

[payload]
TITLE=Pending commit: 5 paths
<treść>
```

---

## Struktura plików stanu

Daemon tworzy katalog `.filees/` wewnątrz kopii roboczej:

```
<wc>/
├── !kolizje/
│   └── YYYY.MM.DD@HH.MM_lokalne/
│       ├── <relpath>           # lokalna kopia pliku sprzed konfliktu
│       └── <relpath>.meta      # JSON: orig_rel, timestamp, size, type
└── .filees/
    ├── state/
    │   ├── manifest.json       # aktywny manifest (mtime, size, MD5)
    │   ├── manifest.tmp        # manifest budowany w trybie Baselining
    │   ├── commit.busy         # flaga: commit w toku (TTL 10 min)
    │   ├── head.rev            # ostatnia commitowana/zaktualizowana rewizja
    │   ├── client.uuid         # stabilny UUID klienta (generowany raz, trwały)
    │   ├── daemon.pid          # PID daemona (usuwany przy shutdown)
    │   └── md5.backlog.json    # kolejka dużych plików oczekujących na hash MD5
    ├── commit_cache/
    │   └── cache.json          # staging map (przeżywa restart daemona)
    ├── tickets/
    │   └── NOTICE-<uuid>.req   # powiadomienia wychodzące
    ├── logs/
    │   └── errors.jsonl        # structured error log (JSON Lines, append-only)
    └── locks/
        ├── global/             # sloty HostGate
        └── repo/               # blokady RepoMutex

$XDG_RUNTIME_DIR/filees.sock   # gniazdo IPC daemona (lub ~/.filees/daemon.sock)
```

---

## Ignorowanie plików

Daemon zawsze ignoruje: `.svn/`, `.filees/state/`, `.filees/locks/`.

### Wbudowane wzorce (hardcoded, nie można nadpisać)

| Kategoria | Wzorce |
|-----------|--------|
| Pliki tymczasowe Office | `~$*`, `*.tmp`, `*.bak` |
| Metadane OS | `.DS_Store`, `Thumbs.db`, `desktop.ini` |
| Katalogi edytorów | `.vscode/`, `.idea/` |
| Pliki edytorów | `*.swp`, `*.swo` |
| Artefakty buildu | `node_modules/`, `__pycache__/`, `*.o`, `*.pyc` |
| Inne VCS | `.git/` |

Katalogi (`.git/`, `node_modules/` itd.) są pomijane w całości — watcher nie wchodzi do środka.

### Wzorce użytkownika

Plik `.filees/user_ignore.cfg` (hot-reload przy każdym skanie):

```
# komentarz
*.local
!archive/         # twardy ignore — pomija cały podkatalog
assets/**/thumb   # ** dopasowuje dowolną głębokość
```

Wzorce z `!` na początku są "twardymi" ignorami — przy katalogu powodują pominięcie całego poddrzewa.

---

## Sygnały

| Sygnał | Działanie |
|--------|-----------|
| `SIGINT` / `SIGTERM` | Graceful shutdown — czeka na zakończenie wszystkich potoków |

---

## Zależności

| Pakiet | Wersja | Użycie |
|--------|--------|--------|
| `github.com/google/uuid` | v1.6.0 | Generowanie ID ticketów i żądań IPC |

---

## Pakiety wewnętrzne

| Pakiet | Rola |
|--------|------|
| `pkg/watcher` | Skaner systemu plików + backlog MD5 |
| `pkg/commit` | Commit service, HEAD poller, reconciliation |
| `pkg/client` | Wrapper SVN CLI |
| `pkg/config` | Parsowanie `config.json` |
| `pkg/contract/v1` | Typy protokołu IPC (`filees.contract/v1`) |
| `pkg/ipcserver` | Serwer gniazda Unix dla CLI/GUI |
| `pkg/ipcclient` | Klient IPC — używany przez CLI i docelowo GUI |
| `pkg/errmap` | Klasyfikacja błędów + zapis do `errors.jsonl` |
| `pkg/runtime` | HostGate, RepoMutex |
| `pkg/talk` | Logger z poziomami i zmienną `FILEES_LOG` |
| `pkg/tickets` | Zapis plików powiadomień `.filees/tickets/` |

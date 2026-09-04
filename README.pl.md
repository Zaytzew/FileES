<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="branded-assets/filees-space-svg-pack/filees-space-monochrome-white.svg">
    <img src="branded-assets/filees-space-svg-pack/filees-space-monochrome.svg" alt="FileES" width="360">
  </picture>
</p>

# FileES

*[English](README.md) | Polski*

Sync and share system built on top of Apache Subversion, server side targeting OpenBSD.

> ## ⚠️ Wersja robocza — nie do użytku
>
> **To nie jest wydanie. To nie jest beta. To jest robocza wersja roboczego systemu, publikowana wyłącznie po to, żeby kod dało się obejrzeć.**
>
> - Nie ma tu żadnej wersji stabilnej, żadnego wsparcia i żadnej gwarancji — ani działania, ani bezpieczeństwa, ani zachowania danych.
> - Formaty na dysku, protokoły, nazwy poleceń i schematy zmieniają się bez zapowiedzi i bez ścieżki migracji.
> - Nie należy tego wdrażać na maszynie produkcyjnej ani powierzać temu danych, których utrata byłaby problemem.
> - Kod jest ciągle w trakcie audytu i przeglądów; znane defekty bywają otwarte tygodniami, bo priorytet ma projekt, nie polerowanie.
> - Wewnętrzne dokumenty projektowe, koncepcje i raporty z audytu **nie są publikowane**. To repozytorium jest filtrowanym lustrem repozytorium SVN i zawiera wyłącznie kod, instrukcję i pliki readme.
>
> Zgłoszenia i pull requesty nie są oczekiwane i mogą pozostać bez odpowiedzi.
>
> ---
>
> **English:** this is a work-in-progress draft of a work-in-progress system, published only so the code can be looked at. It is not a release, has no stable version, no support and no guarantees of any kind — including security and data integrity. On-disk formats, protocols and schemas change without notice or migration. Do not deploy it and do not trust it with data you care about. Internal design and audit documents are deliberately not published; this repository is a filtered mirror of an SVN repository and contains code, the manual and readme files only. Issues and pull requests are not expected and may go unanswered.

Daemon synchronizujący lokalne katalogi z repozytorium SVN. Przeznaczony dla zespołów pracujących na plikach binarnych (grafika, modele 3D, zasoby projektowe). SVN jest tu warstwą transportową i magazynem — semantyka kontroli wersji jest drugorzędna.

Docelowy UX: automat w trayu, który niewidocznie utrzymuje pliki zsynchronizowane z serwerem. Użytkownik nie musi wiedzieć, że pod spodem działa SVN.

---

## Dokumentacja

- **[manual.filees.space](https://manual.filees.space)** — pełny podręcznik HTML PL/EN na żywo (origin). Strona główna jest przełącznikiem języka.
- **[manual/index.html](manual/index.html)** — ta sama treść jako obraz repozytorium (rozdziały w
  `manual/assets/pl/` i `manual/assets/en/`); origin pod `manual.filees.space` jest wersją autorytatywną.
- **[docs/man/](docs/man/)** — strony `mandoc` narzędzi serwerowych (`man filees`, `man filees-admin`, `man filees.conf`).
- **[USERGUIDE.md](USERGUIDE.md)** — krótszy przewodnik użytkownika.
- **[manual-filees.html](manual-filees.html)** — tylko przekierowanie do `manual/`, żeby stare odnośniki nie umarły.

**GUI desktopowe:** `cmd/filees-gui-wails` (Wails/WebView) jest bieżącym i
jedynym aktywnie rozwijanym klientem desktopowym. Stary `cmd/filees-gui`
(Fyne+zenity/yad na Linuksie, WinForms/PowerShell na Windows) ma status
**deprecated / abandoned** i pozostaje wyłącznie historią wdrożenia do
mechanicznego usunięcia. Nie jest wspieraną alternatywą ani wzorcem parytetu.

Aktualny klient desktopowy obsługuje na Windows pełne dołączenie kolejnej
instalacji do istniejącego realmu, listę wszystkich repozytoriów realmu w
Ustawieniach i selektywny pierwszy checkout. Grant realmu nie oznacza
automatycznego pobrania wszystkich repozytoriów na każde urządzenie. Szczegóły
obsługi lifecycle WC, dziennika i projekcji aliasu opisuje
`reports/WINDOWS_REALM_JOIN_WC_AND_JOURNAL_FIX_BLOCK_2026-08-10.md`.

---

## Jakość

Neutralny gate jakości dla CI to `make verify`: pełne testy Go, wybrane testy race, `go vet` oraz lokalny smoke test recovery SVN. Sam smoke test uruchamia `scripts/svn-recovery-smoke.sh` — tworzy tymczasowe repozytorium SVN i nie wymaga dostępu do sieci.

---

## Wymagania

- Go 1.25+
- Klient SVN (`svn`) dostępny w `PATH`
- Klient OpenSSH (`ssh`) i aktywna tożsamość instalacji FileES
- Dostęp przez systemowy `sshd` do tunelowego `svnserve -t`; nasłuchujący
  `svnserve --daemon` nie jest obsługiwany

### Inspekcja i naprawa stanu po stronie serwera

Na OpenBSD polecenia administracyjne uruchamiaj jako `_filees-state`.
Porzucone próby utworzenia repozytorium można najpierw sprawdzić bez zmian,
potem wykonać dry-run i dopiero jawne zastosowanie:

```sh
doas -u _filees-state filees-admin repo check-state [--realm-id UUID]
doas -u _filees-state filees-admin repo prune [--realm-id UUID] --older-than 1h
doas -u _filees-state filees-admin repo prune [--realm-id UUID] --older-than 1h --apply
```

Prune dopuszcza wyłącznie stare, opublikowane, lecz nigdy nieaktywowane
repozytoria, których FSFS nie istnieje albo nadal ma dokładnie r0. Przed
usunięciem blokuje commity i ponownie sprawdza HEAD. Repozytoria aktywne,
inicjalizowane od r1 wzwyż oraz zwykłe tombstone’y usunięcia są chronione.
Pełny kontrakt opisuje `filees-admin(8)`.

---

## Budowanie

```bash
go build -o filees ./cmd/filees
```

---

## Konfiguracja

Daemon szuka pliku `config.json` w katalogu roboczym. Transport SSH należy do
instalacji klienta, a nie do pojedynczego repozytorium:

```json
{
  "transport": {
    "identity_file": "/home/user/.local/share/filees/identity/id_ed25519",
    "known_hosts": "/home/user/.local/share/filees/known_hosts"
  },
  "update": {
    "enabled": true,
    "repo_url": "https://releases.example/FILEES-BIN",
    "channel": "stable",
    "component": "desktop",
    "platform": "linux-amd64",
    "state_path": "/home/user/.local/state/filees/update.json",
    "stage_root": "/home/user/.local/state/filees/update-stage"
  },
  "repositories": [
    {
      "id":              "projectA",
      "repo_url":        "svn+ssh://_filees-client@server/repo/trunk",
      "local_path":      "/home/user/projectA",
      "commit_interval": "1m",
      "watch_interval":  "30s",
      "poll_interval":   "30s",
      "max_batch_files": 100,
      "max_batch_mib": 512,
      "backlog_flush_mib": 1024,
      "shutdown_commit_timeout": "10m",
      "lock_first":      false,
      "edit_passports":  false,
      "edit_passport_ttl": "15m",
      "edit_passport_heartbeat": "5m",
      "edit_passport_max_session": "24h",
      "edit_passport_close_grace": "5m",
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
}
```

| Pole               | Opis |
|--------------------|------|
| `id`               | Unikalny identyfikator repo (używany w logach i ścieżkach stanu) |
| `transport.identity_file` | Bezwzględna ścieżka do klucza Ed25519 utworzonego podczas aktywacji |
| `transport.known_hosts` | Bezwzględna ścieżka do pinowanego klucza hosta usługi |
| `update.enabled` | Włącza podpisany updater klienta; domyślnie wyłączony |
| `update.repo_url` | URL repozytorium release SVN/HTTPS bez hasła, query i fragmentu |
| `update.channel` | Kanał release, domyślnie `stable` |
| `update.state_path` | Prywatny, bezwzględny plik trwałego high-water mark |
| `update.stage_root` | Prywatny, bezwzględny katalog zweryfikowanego stagingu |
| `repo_url`         | URL `svn+ssh://_filees-client@host/...`; inne transporty są odrzucane |
| `local_path`       | Bezwzględna ścieżka do kopii roboczej |
| `commit_interval`  | Okno commitów (np. `1m`, `30s`) |
| `watch_interval`   | Interwał skanowania systemu plików |
| `poll_interval`    | Jak często sprawdzać HEAD serwera i pobierać zmiany (`svn update`); domyślnie `30s` |
| `max_batch_files`  | Maks. liczba plików w jednym commicie |
| `max_batch_mib`    | Docelowy maks. rozmiar jednego commita w MiB; większy pojedynczy plik tworzy własny batch |
| `backlog_flush_mib` | Próg zaległości w MiB wymuszający commit bez czekania na zwykły interwał |
| `shutdown_commit_timeout` | Maks. czas pełnego drainu stagingu podczas kontrolowanego zamknięcia |
| `lock_first`       | Jeśli `true` — próbuje `svn lock` przed commitem |
| `edit_passports`   | Tylko dla ręcznie skonfigurowanych repozytoriów legacy/deweloperskich: włącza passporty edycji. Dla repozytoriów projektowanych przez serwer pole jest ignorowane na rzecz kanonicznego `editing_policy` |
| `edit_passport_ttl` | Ważność pojedynczego odnowienia passportu; domyślnie `15m` |
| `edit_passport_heartbeat` | Interwał odnowienia, krótszy od TTL; domyślnie `5m` |
| `edit_passport_max_session` | Nieprzedłużalny limit sesji; domyślnie `24h` |
| `edit_passport_close_grace` | Wymagany okres ciszy po potwierdzonym commicie; domyślnie `5m` |
| `shout_patterns`   | Wzorce regex; pasujące pliki wyzwalają powiadomienie (ticket) |
| `rate_limit_shout` | Minimalny odstęp między powiadomieniami |
| `commit_tiers`     | Size-adaptive interwały (lista rosnąco wg `max_mb`); pominięte = tylko `commit_interval` |

**`commit_tiers`** — każdy wpis to `{"max_mb": N, "interval": "Xm"}`. Daemon sprawdza sumaryczny rozmiar plików w bieżącym batchu i stosuje minimalny odstęp odpowiedniego tieru. `max_mb: 0` to catch-all (ostatni tier). Przykład: batche < 1 MiB co 2 min, 1–10 MiB co 5 min, 10–50 MiB co 15 min, > 50 MiB co 24h.

Czasy podawane w formacie Go: `30s`, `5m`, `1h`.

W normalnym przepływie produktu właściciel zmienia repozytoryjną politykę w
Ustawieniach przez akcję **Zasady edycji**. Serwer zapisuje ją raz w rekordzie
kanonicznym i projektuje identycznie do wszystkich klientów; jedyną wartością
wysyłaną na drucie jest `lock_required`, a brak pola oznacza zwykłą edycję.
Pełny lifecycle i gwarancje wieloklientowe opisuje rozdział
[2.5 Edit Passports](manual/assets/en/user-guide.html#ch2-passports).

Każdy `local_path` musi być ścieżką bezwzględną. Identyfikatory repozytoriów muszą być unikalne, a lokalne korzenie nie mogą być identyczne ani zagnieżdżone w żadną stronę. Walidacja rozwiązuje symlinki istniejących katalogów i kończy start daemona twardym błędem przed utworzeniem stanu `.filees`.

Hasło SVN podane w konfiguracji jest usuwane z logów `trace`. Klient SVN 1.14 nie udostępnia bezpiecznego wejścia hasła przez stdin, dlatego do czasu przejścia na docelowe klucze SSH hasło nadal jest przekazywane procesowi `svn` jako argument. Na współdzielonych hostach należy preferować transport oparty na kluczach albo konto systemowe z ograniczonym dostępem do listy procesów.

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

## GUI Tray

`filees-gui` jest osobnym procesem i cienką warstwą UX nad publicznym kontraktem IPC. GUI nie jest częścią daemona, nie zna SVN i nie przejmuje odpowiedzialności za synchronizację. Awaria samego procesu GUI nie zabija daemona, natomiast jawna akcja użytkownika **Zamknij FileES** kontrolowanie zatrzymuje daemon i GUI jako jeden stack kliencki.

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
- **Uruchom FileES ponownie…** i **Zamknij FileES…** są dostępne wyłącznie po
  zareklamowaniu `system.restart`/`system.shutdown`; obie operacje obejmują
  daemon i GUI.

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

Menu tray zawiera:

- zagregowany stan daemona i czas ostatniego poprawnego odświeżenia,
- listę repozytoriów ze stanem, connectivity, rewizją i liczbą oczekujących zmian,
- „Dodaj folder do FileES…” przy serwerze, który pozwala temu klientowi tworzyć repozytoria,
- globalną pozycję nagłówkową „Lista rezerwacji plikowych…”, aktywną tylko gdy
  lokalnie widoczna jest co najmniej jedna rezerwacja; otwiera natywną,
  wieloserwerową listę blokad z lokalnie podłączonych WC,
- „Otwórz katalog” dla każdego repozytorium,
- `Lock…` i `Unlock…` z wyborem plików wewnątrz danego repozytorium,
- bezpośrednio w menu serwera (przed rozwijanym folderem) **Odłącz folder
  „&lt;nazwa&gt;”…** dla opcjonalnej WC oraz osobne, podwójnie potwierdzane
  **Odłącz trwale „&lt;nazwa&gt;”…** dla repozytorium własnego realmu,
- w oknie „Ustawienia FileES”, gdy daemon zgłasza capability: **Widoczność…**
  (przełącza widoczność własnej strefy w prywatnym katalogu odbiorców),
  **Uprawnienia gości** per repozytorium (aktualny stan oraz nadanie/cofnięcie
  `r`/`rw` widocznej strefie), **Udostępnienia publiczne** per własne
  repozytorium (lista, utworzenie, edycja, revoke i delete kanału) oraz
  **Odtwórz z archiwum…** dla wybranego wiersza repozytorium
  (ładuje uprzednio wyeksportowany dump SVN jako nową generację tego
  repozytorium, przez ten sam mechanizm co `filees-rotate`),
- jedno globalne podmenu **Dziennik**, łączące aktywność i błędy newest-first;
  tray pokazuje maksymalnie 12 zagregowanych wpisów, a **Otwórz log…** otwiera
  pełny dostępny snapshot w natywnym oknie; podgląd tłumaczy czas na
  „przed chwilą”, „N minut temu”, godzinę, „wczoraj” lub „N dni temu”, a
  pełny widok używa `dd:mm:yy hh:mm`; błędy są jawnie wyróżnione,
- „Połącz ponownie” przy niedostępnym daemonie,
- placeholder „Aktualizacja klienta — w przygotowaniu”, gdy nie ma
  zareklamowanego wydania,
- „Uruchom FileES ponownie…” i „Zamknij FileES…”.

Elementy zależne od komend mutujących są tworzone wyłącznie na podstawie capabilities i świeżego snapshotu. GUI obsługuje obecnie m.in. `events.subscribe`, `repo.create_request`, `repo.attach_intent`, `repo.attach_approve`, `repo.locate`, `repo.detach`, `repo.delete`, `repo.load_dump`, `repo.lifecycle_status`, `repo.activity`, `repo.grant_access`, `repo.revoke_access`, `repo.public_share_list`, `repo.public_share_create`, `repo.public_share_update`, `repo.public_share_revoke`, `repo.public_share_delete`, `repo.lock`, `repo.unlock`, `repo.reservation_list`, `repo.reservation_release`, `realm.alias_claim`, `realm.grant_recipients`, `realm.set_visibility`, `system.restart`, `system.shutdown`, `error.list` oraz dynamiczne `update.status`, `update.plan` i `update.apply`. Capability aktualizacji pojawiają się wyłącznie przy kompletnej, podpisanej usłudze update. `Pause`, `Sync now`, publikowanie zmian i decyzje konfliktowe pozostają ukryte do czasu wdrożenia i zareklamowania ich przez daemon.

Dołączenie kolejnej instalacji do istniejącego realmu jest autoryzowane przez
administratora przy tworzeniu ticketu (`--join-realm-alias`). Po aktywacji
Ustawienia pokazują pełną projekcję repozytoriów realmu jako lokalnie
`attached` lub `unattached`. Windows pozwala zaznaczyć wiele niepodłączonych
wierszy i wybrać **Połącz**; dla każdego repo pojawia się osobny picker
lokalnego katalogu. Po akceptacji wiersz natychmiast pokazuje wybrany path i
`łączenie…`, aż daemon potwierdzi pierwszy checkout. Linux korzysta z tego
samego lifecycle dla pojedynczego wybranego wiersza.

Tworzenie repozytorium jest zwykłą operacją użytkownika, bez kontaktu z konsolą:

1. W podmenu właściwego serwera wybierz „Dodaj folder do FileES…”. Akcja jest ukryta dla klienta tylko do odczytu, serwera bez zezwolenia lub nieaktualnego połączenia.
2. Wskaż istniejący lokalny folder w natywnym pickerze Linux/Windows.
3. Zaakceptuj nazwę wyprowadzoną z nazwy folderu albo wpisz własną.
4. Sprawdź serwer, folder oraz dostęp `rw` w podsumowaniu i wybierz „Utwórz”.

GUI ponownie sprawdza świeżość i uprawnienia bezpośrednio przed żądaniem IPC. Daemon kanonizuje ścieżkę, odrzuca nakładające się korzenie i trwale zapisuje operację przed odpowiedzią. Dalsze tworzenie na serwerze, import zawartości, pierwszy commit i dołączenie repozytorium odbywają się asynchronicznie; przyjęcie operacji daje pierwsze powiadomienie ("Tworzenie repozytorium rozpoczęte"). Gdy repo istnieje już na serwerze, ale `INITIAL_COMMIT` nadal trwa, projekcja zachowuje wskazaną lokalną ścieżkę i pokazuje **import początkowy w toku** zamiast fałszywego **nieprzypięte lokalnie**; runtime i akcje mutujące pozostają zablokowane do sukcesu importu. GUI odpytuje następnie `repo.lifecycle_status` po `operation_id` (domyślnie co 3 s, do 15 min) i pokazuje drugie powiadomienie z realnym wynikiem: "Repozytorium utworzone" albo, w razie niepowodzenia dowolnego etapu (np. `STORAGE_INSUFFICIENT` przy braku miejsca na serwerze), komunikat błędu z dokładną treścią przyczyny. Repozytorium, które nie przejdzie tego pipeline'u, nigdy nie dostaje zarejestrowanego `RepoID`, więc nie pojawi się w stanie repozytoriów ani w `error.list` — drugie powiadomienie jest jedynym miejscem, gdzie taka porażka jest widoczna.

Odłączenie ma dwa rozłączne kontrakty:

- **Odłącz folder…** zatrzymuje runtime, usuwa wyłącznie `.svn` i `.filees`
  z lokalnego rootu i zostawia wszystkie dane użytkownika jako zwykły folder;
  działa także dla repo offline, a trwały tombstone blokuje ponowne podłączenie
  starego wpisu `config.json`. Końcowa synchronizacja katalogu korzysta z
  platformowego `durable.SyncDirectory`: zachowuje `fsync` na POSIX i nie
  wpada w `ERROR_ACCESS_DENIED` na Windows. Ponowne **Połącz** wymaga nowego
  albo pustego celu; odrzucenie attach jest pokazywane modalnie oraz jako
  powiadomienie;
- **Odłącz trwale…** wymaga dwóch osobnych potwierdzeń, server-side ownership
  oraz capability administracji repo. Dla retencji `X>0` serwer tworzy i
  weryfikuje pełny dump, natychmiast usuwa FSFS i trzyma wyłącznie dump przez
  `X` dni. Domyślne `X=30` zapisuje dump i manifest z SHA-256 pod
  `results_root/deleted-repositories`; wynik workera zawiera dokładny
  `retain_until`. Dla `X=0` usuwa FSFS natychmiast bez tworzenia dumpa.

  Sukces serwera, wydanie capability archiwum oraz lokalne usunięcie metadanych
  WC są trzema niezależnymi, trwałymi granicami. Po sukcesie
  `DELETE_REPOSITORY` daemon zapisuje `retain_until`, a następnie niezależnie
  próbuje wydać kluczowany pakiet `.fkr` i usunąć `.svn`/`.filees`. Starszy
  worker bez ticketu recovery nie blokuje lokalnego cleanupu, a blokada
  `wc.db` nie jest raportowana jako porażka serwera. Repo pozostaje w projekcji
  jako usunięte, z osobnym stanem oczekiwania obu skutków; tylko lokalny cleanup
  jest automatycznie ponawiany bez ruchu sieciowego. Przy dodatniej retencji
  panel pokazuje osobną grupę archiwów, odliczanie do zera i akcję pobrania;
  przy retencji zero nie pokazuje martwej akcji.

Repozytorium `attachment_policy=required` nie udostępnia żadnej z tych akcji.
Lifecycle jest trwały i wznawialny po restarcie.

Root aktywnej WC jest artefaktem śledzonym. Przed uruchomieniem pipeline daemon
sprawdza `.svn`, dokładny URL oraz marker tożsamości FileES. Brakujący albo
podmieniony root daje `interaction_required / working_copy_missing`; FileES nie
odtwarza pustego katalogu i nie raportuje go jako zdrowej rewizji 0. Ustawienia
pokazują wtedy **Wskaż kopię**, które przez `repo.locate` przyjmuje istniejącą,
przeniesioną WC bez checkoutu i bez kasowania lokalnych zmian. Na Windows
aktywny root jest dodatkowo chroniony uchwytem blokującym zewnętrzny
rename/delete; kontrolowana operacja FileES najpierw zwalnia ten uchwyt.

**Widoczność strefy i granty** działają przez dwie osobne akcje w oknie
„Ustawienia FileES” (Linux: `yad` radiolist; Windows: PowerShell
`DataGridView`). **Widoczność…** przełącza wpis własnej strefy w prywatnym
katalogu odbiorców między ukrytym a widocznym — strefa musi być widoczna,
zanim inna strefa będzie mogła wybrać ją jako odbiorcę grantu; przełączenie
nigdy nie ujawnia repozytoriów ani istniejących dostępów. **Uprawnienia gości**,
dostępne per wiersz repozytorium, otwiera sumę aktualnie widocznych stref oraz
ukrytych stref z aktywnym grantem. Tabela pokazuje bieżące `r`/`rw` albo brak
dostępu i pozwala nadać dostęp tylko do odczytu, do odczytu i zapisu albo go
cofnąć; ukrycie strefy nie czyni istniejącego grantu niemożliwym do zarządzania.
Każda zmiana wymaga potwierdzenia i natychmiast regeneruje `data-authz` oraz
`view.json` wszystkich instalacji, których dotyczy.

**Udostępnienia publiczne** są akcją wyłącznie właściciela repozytorium. Okno
pokazuje aktywne i cofnięte kanały oraz ich adres, źródło, odbiorców, ochronę
hasłem i politykę rewizji. Kanał można utworzyć, edytować, cofnąć lub usunąć.
Przy create/update użytkownik wybiera root albo podfolder lokalnej kopii
roboczej; GUI rekurencyjnie buduje mapę maksymalnie 4096 zwykłych plików wraz
z ich rozmiarami, pomija `.svn` i `.filees`, odrzuca symlinki i zachowuje
stabilne `public_id` dla ścieżek już obecnych w edytowanym kanale. Pusty folder
jest legalnym placeholderem; późniejsze pliki wymagają jawnej aktualizacji
kanału. Publiczna strona buduje wyłącznie z bezpiecznej projekcji mapy
domyślnie zwinięte drzewo w stylu widoku „Szczegóły”: nazwa, ikona i opis typu,
rozmiar oraz pobranie pojedynczego pliku. Zaznaczenie, folder i cały udział
można pobrać również jako ograniczony ZIP. Widok używa identyfikacji
`filees:space`; nie używa JavaScriptu ani miniaturek. Kanał zamknięty używa
osobnych tokenów dla adresów e-mail; kanał otwarty może być bez hasła albo z
hasłem Argon2id.
Plaintext jest hashowany przed IPC control-plane i nigdy nie trafia do ticketu
SVN. Edycja może zachować istniejący verifier po stronie serwera bez odsyłania
go do klienta. Pusta rewizja śledzi HEAD, dodatni numer ustawia
`do-not-follow`.

**Odtwórz z archiwum…** (`load_dump`, IPC `repo.load_dump`, ticket
`LOAD_REPOSITORY_DUMP`) jest dostępne dla wybranego wiersza repozytorium
obok Odłącz/Odłącz trwale. Ładuje wcześniej wyeksportowany dump SVN jako
nową generację repozytorium przez ten sam mechanizm staging/weryfikacja/
atomowy swap, którego wewnętrznie używa `filees-rotate` (`manual/assets/en/administration.html`
§4.7) — worker sam odnajduje przesłany dump, klient
nie wysyła ścieżki ani zakresu rewizji. Pierwsze wydanie działa bez okna
opcji: filtrowanie zawsze stosuje bieżącą politykę ignorowania serwera, a
pełna historia źródłowa jest zachowana.

### Rezerwacje plików

Nagłówkowa pozycja **Lista rezerwacji plikowych…** otwiera natywne okno
Linux/Windows z aktywnymi blokadami SVN znalezionymi we wszystkich working copy
podłączonych lokalnie do aktywnych serwerów. Wiersz zawiera serwer, nazwę kopii
roboczej, ścieżkę względną, alias właściciela oraz lokalny czas utworzenia w
formacie `HH:MM DD-MM-RRRR`. Lista jest uporządkowana według katalogu roboczego,
a następnie ścieżki. Nie jest to administracyjny spis wszystkich blokad całego
serwera — repozytorium bez lokalnej WC nie jest w tym widoku obserwowalne.

Po wybraniu wiersza **Zwolnij** GUI zawsze pyta o potwierdzenie. Gdy w tej WC
są lokalne zmiany lub blokada odpowiada aktywnemu paszportowi edycji, dialog
wyraźnie ostrzega o niezapisanych danych i wymaga świadomego potwierdzenia.
GUI nie próbuje wykrywać uchwytów edytora: wiele programów zapisuje atomową
podmianą pliku, więc taki test byłby pozornym zabezpieczeniem. Rezerwacja
związana z paszportem aktywnym na innym urządzeniu ma akcję **Poproś o
zwolnienie**. Prośba jest przypięta do tokenu tej blokady; requester widzi jej
stan, a holder dostaje `[OK] [Zwolnij]`. Przycisk **Zwolnij wszystko** obejmuje
wyłącznie rezerwacje możliwe do zwolnienia przez ten klient, wymaga jednego
potwierdzenia i wykonuje każdą operację z jej tokenem. Żądanie pojedynczego
zwolnienia również zawiera token z listy; daemon odczytuje stan ponownie i
odrzuca zmieniony lub nieaktualny wiersz, zanim wywoła SVN.

Alias właściciela jest stałą tożsamością realmu, nie adresem e-mail ani UID.
**Ustaw stały alias…** jest oferowane tylko świeżemu, pustemu realmowi. Klient
dołączający do istniejącego realmu dziedziczy jego kanoniczny alias w pierwszej
projekcji i nie może go ponownie nazwać. Brak aliasu przy już projektowanych
repozytoriach oznacza niepełną projekcję, a nie zadanie dla użytkownika;
**Widoczność…** i locki są wtedy blokowane z jawnym komunikatem do czasu
rekoncyliacji serwera.

### Podpisane aktualizacje klienta desktopowego

Updater jest opt-in i fail-closed. Produkcyjny build musi zawierać osadzony
publiczny klucz release oraz jego `key_id`; konfiguracja nie może podmienić
klucza ani wyłączyć podpisów. Daemon reklamuje `update.status`, `update.plan`
i `update.apply` wyłącznie po zarejestrowaniu kompletnej usługi aktualizacji.

Zweryfikowany envelope v2 wiąże `release_id`, monotoniczne `sequence` i
`security_epoch`, termin ważności, komponent, platformę oraz manifest artefaktu.
Klient sprawdza format OpenBSD signify wewnętrznie przez Ed25519, następnie
dokładny rozmiar i SHA-256 bundla. Trwały high-water mark blokuje downgrade,
obniżenie epoki bezpieczeństwa i fork tego samego sequence.

GUI pokazuje badge „Dostępna aktualizacja”. „Pokaż, co ulegnie zmianie…” jest
dry runem, a „Zaktualizuj i uruchom ponownie…” ponownie rozwiązuje podpisane
wydanie, wyświetla natywne potwierdzenie i uruchamia właściwy instalator.
Linux zachowuje konfigurację i wyłącza restart/autostart wewnątrz skryptu.
Windows (r819/r821) instaluje daemon, Wails GUI i dwa launchery do katalogu
uruchomionego `filees.exe`; działające obrazy odsuwa pod nieuruchamialną nazwę,
a `config.json` pozostawia nietknięty. Po sukcesie GUI żąda restartu całego
stacku, kończy pracę, zwalnia blokadę single-instance i dopiero wtedy uruchamia
nową binarkę. Procedura publikacji: `tools/RELEASE_PUBLISHING.md`.

Windowsowy kandydat powstaje przez `tools/prepare-client-release-windows.sh`.
Manifest ma kanoniczną ścieżkę
`releases/<id>/desktop/windows-amd64/manifest.json`, a neutralny wobec kanału
envelope — `releases/<id>/channel.v2.json`. Wpis innej platformy można dołączyć
wyłącznie z kandydata o tej samej pełnej tożsamości release'u; kopiowanie wpisu
ze starego kanału jest odrzucane, ponieważ jego manifest nie przeszedłby
walidacji envelope–manifest po stronie klienta.

### Powiadomienia

Systemowe powiadomienia są wtórne wobec stanu w menu. MVP pokazuje je dla nowych błędów, przejścia repozytorium w stan wymagający uwagi, utraty/odzyskania łączności oraz zakończenia operacji istotnej dla użytkownika. Powtarzające się zdarzenia są grupowane i ograniczane czasowo. Powiadomienia pozostają informacyjne; bezpieczna aktywacja po kliknięciu wymaga osobnego odbioru natywnego i nie może wykonywać operacji mutującej.

### Mapa modułów

```text
cmd/filees/              daemon, CLI i composition root klienta
cmd/filees-gui/          composition root i lifecycle warstwy prezentacyjnej
cmd/filees-gui-wails/    docelowy klient Wails/WebView tej samej projekcji IPC
android/                 klient mobilny Kotlin, most gomobile przez pkg/mobileclient
internal/gui/            model, akcje, tray, platforma i powiadomienia
pkg/contract/v1/         kontrakt IPC GUI/CLI <-> daemon
pkg/ipcclient,ipcserver/ transport lokalnego control plane
contracttests/           wspólna bramka zgodności kopert, capability i IPC E2E

pkg/clientview/          ścisła projekcja stanu instalacji z service repo
pkg/localrepo/           trwały lifecycle lokalnych przypięć WC
pkg/provisioning/        create/attach/initial-commit state machine
pkg/reposupervisor/      uruchamianie i rekoncyliacja wielu repozytoriów
pkg/watcher,commit/      skanowanie, batching, update i publikacja SVN
pkg/passport/            needs-lock, lease, fencing i migracje polityki edycji
pkg/shout/               marker wydania w svn:log i lokalny inbox komunikatów
pkg/errcat,errmap/       wspólny język błędów i klasyfikacja diagnostyki

pkg/control/v1/          podpisywane żądania klient -> worker
pkg/whale/v1/            kanon generacji i framing windowed Whale
pkg/whaleclient/         trwały aktor/spool oraz pinowany transport SSH PUT/GET
pkg/repoworker/          autorytatywne repozytoria, granty i projekcje
internal/servertool/     forced-command entrypoints, lease/revoke supervisor i operacje serwera
internal/whaleworker/    PUT, świadomy GET, seekowalny cache i svnmucc file://
pkg/onboarding,activation/ aktywacja, tożsamość i service repo
cmd/filees-service-wc-corrector/ korekta owner/group usługowej WC przed ticketem

internal/mobileworker/   mobilny odczyt i capture pod mobile-uploads, w tym UPLOAD_TREE
cmd/filees-public-authority/ publiczne udziały i ich osobna granica zaufania
public-shares/           kanał, brama, projekcja web, OTP wnoszącego, poczekalnia intake
internal/uploadworker/   poczekalnia i reaper AV dla kwarantanny wpłat publicznych
pkg/avscan/              klasyfikacja AV (clamscan/clamdscan) i sygnatura testowa EICAR
internal/clientupdate/   podpisany updater desktopu
internal/serverinstall/  rdzeń manifestowego instalatora serwera
internal/release*/       koperty, podpisy i publikacja artefaktów
```

Historyczny renderer traya używał `fyne.io/systray`, izolowanego jako adapter
w `internal/gui/tray`. `cmd/filees-gui` i jego powierzchnia
Fyne+zenity/yad/WinForms są **deprecated / abandoned**. Kod pozostaje
tymczasowo do migracji i sprzątnięcia packagingu; nie trafiają tam nowe
funkcje produktu.

**Bieżącym GUI jest `cmd/filees-gui-wails`**, przypięty do Wails
`v3.0.0-beta.6`. Decyzja zapadła 2026-08-26 (r603): to, co zaczęło się jako
eksperyment WebView, dało na tyle bezprecedensową poprawę UX — pełne, trwałe
okno zamiast serii natywnych dialogów, spójny layout i motyw na Windows i
Linux, żywa projekcja bez ręcznego odświeżania — że inna decyzja nie była
realna. Od tego czasu przeszedł żywy odbiór na Linuksie i przejął panel, tray,
single-instance, lokalny PIN, powiadomienia, lifecycle repozytoriów, udziały
publiczne, półki przyjęcia, kwarantannę, aktualizacje, parowanie mobilne i
ogłoszenia. Dotąd nie natrafiliśmy na żadną blokującą słabość samego Wails w
becie; błędy zamknięte po drodze (np. bindings promptów niosące
identyfikatory z `go test` zamiast z buildu) były naszymi własnymi pomyłkami,
nie ograniczeniem frameworka. Interaktywny pion prośby o zwolnienie locka jest
już obecny; do domknięcia pozostają automatyczne `svn up` + lock u requestera,
rekonsyliacja/GC i żywy test dwóch klientów opisane w
`concepts/LOCK_RELEASE_REQUEST_CONCEPT_V2.md`. Architektura i status starego
renderera: `concepts/WAILS_GUI_FORK.md` §0, §4 i §7.

Wails nie wnosi drugiego modelu klienta: uruchamia ten sam `internal/gui/app`,
komunikuje się wyłącznie przez `pkg/ipcclient`, a WebView renderuje otrzymaną
projekcję i zwraca intencje. Ma osobny EXE, statyczny frontend bez Node/Vite
oraz `Snapshot`, `Refresh` i `Reconnect`. Akcje `Otwórz`, `Zablokuj` i
`Zwolnij` idą przez ten sam `internal/gui/actions` co Fyne; JavaScript nie
woła IPC. Okno Windows jest bezramkowe, a ukrycie scrollbara WebView nie
wyłącza przewijania. Aktywne locki są częścią projekcji; inline zwolnienie
przekazuje tylko opaque ID, a token fencingowy pozostaje w Go. Tray Wails
utrzymuje proces po ukryciu okna i pokazuje stan oraz liczbę repozytoriów i
blokad. Podmenu `FileES` przekazuje restart i zakończenie całej pary daemon +
GUI do wspólnego kontrolera; nie istnieje już lokalna akcja kończąca
wyłącznie renderer.

### Historyczne etapy implementacji `cmd/filees-gui`

Poniższe etapy dokumentują, jak porzucony renderer ustanowił współdzielone
granice `internal/gui/app`, `internal/gui/actions` i adapterów platformowych.
To zapis historyczny, nie bieżąca mapa rozwoju desktopu.

1. **Rdzeń bez tray** — `internal/gui/app`, interfejs `DaemonClient`, pojedyncza pętla stanu, init, reconnect, resync, debounce oraz test architektoniczny i jednostkowy bez GUI.
2. **Adapter tray** — `internal/gui/tray` na `fyne.io/systray`, pięć ikon, menu renderowane z `ViewModel` oraz intencje użytkownika bez bezpośredniego dostępu do IPC.
3. **Integracje platformowe** — 3A: czyste interfejsy i fake backend; 3B: Linux (otwieranie katalogów, picker, powiadomienia, autostart XDG); 3C: odpowiedniki Windows; 3D: nieblokujący kontroler `tray.Intent`, który koordynuje platformę i granicę `DaemonClient` bez importowania implementacji IPC.
4. **Integracja i odbiór MVP** — `cmd/filees-gui`, metadane i pakietowanie istniejących zasobów, testy app ↔ fake IPC, testy manualne obu platform oraz weryfikacja restartu daemona, wolnego GUI i wielu repozytoriów.

Etapy 1 i 2 są ukończone. Adapter `fyne.io/systray` jest odseparowany od IPC i kontraktu przez `ViewModel`, ma pięć osadzonych ikon (PNG/ICO), deterministyczny model menu, intencje użytkownika oraz testy renderera i granicy importów. Szczegółowy zakres kolejnych etapów oraz checklista znajdują się w `gui-assumptions.md`.

Etap 3A jest ukończony: `internal/gui/platform` definiuje czyste interfejsy systemowe, klasyfikację niedostępności i błędów operacyjnych oraz współbieżnie bezpieczny fake backend. Pakiet nie zależy od traya, aplikacji, kontraktu IPC ani silnika; granicę sprawdza test architektoniczny.

Etap 3B jest ukończony: adapter Linux zapewnia `xdg-open`, wybór plików i katalogów przez Zenity/KDialog, grupowane i limitowane powiadomienia `notify-send` oraz atomowy autostart XDG z obsługą `Hidden=true`. Wywołania desktopowe są wstrzyknięte i testowane bez otwierania rzeczywistych okien.

Etap 3C jest ukończony implementacyjnie: adapter Windows obejmuje Explorer, pickery plików i katalogów PowerShell/WinForms, `ToastGeneric` i autostart HKCU. Procesy oraz rejestr są wstrzyknięte; quoting korzysta z reguł Windows, a powiadomienia wymagają własnego AUMID FileES zarejestrowanego przez pakiet Etapu 4. Picker, prompt i powiadomienia uruchamiają PowerShell bez widocznego okna konsoli i ustawiają per-monitor DPI awareness przed utworzeniem okna. Natywny odbiór pozostaje częścią checklisty każdego wydania Windows.

Etap 3D jest ukończony: `internal/gui/actions` nieblokująco obsługuje intencje traya, ponownie sprawdza świeżość modelu i repozytorium po interakcji z pickerem, waliduje ścieżki przed IPC oraz serializuje lock/unlock w obrębie jednego repozytorium. Kontroler posiada lifecycle swoich zadań i single-flight dla otwierania katalogu. Granicę importów chroni test architektoniczny.

Etap 4 jest ukończony implementacyjnie. `cmd/filees-gui` stanowi composition root dla `ipcclient`, modelu `app`, renderera `tray`, polityki `notifications`, kontrolera `actions` i adaptera platformowego. Lifecycle ma wspólne anulowanie dla sygnałów systemowych, restartu/zamknięcia całego FileES i zamknięcia traya, czeka na zadania kontrolera oraz listenery renderera, a ręczny reconnect przechodzi przez pętlę zdarzeń `app`. Per-user lock blokuje drugą instancję przed inicjalizacją traya. Test pionowy z rzeczywistym transportem IPC obejmuje wiele repozytoriów oraz zamknięcie i restart daemona; shutdown serwera zamyka aktywne streamy, więc reconnect nie zależy od śmierci procesu. Skrypt `packaging/build-gui.sh` tworzy pure-Go bundle Linux/Windows w świeżych katalogach i przekazuje wersję z `VERSION` do GUI oraz WiX. Linux ma instalację per-user, a źródło MSI WiX tworzy na Windows skrót Start Menu z AUMID. Odbiór w prawdziwych sesjach obu systemów opisuje `packaging/ACCEPTANCE.md` i pozostaje bramką wydania.

Autostart procesu GUI jest zarządzany bez uruchamiania traya przez `filees-gui --autostart status|enable|disable`. Status rozróżnia poprawny wpis `enabled` od `enabled-stale`, który wskazuje inną komendę. Wpis zachowuje absolutną ścieżkę executable i parametr `--socket`; `enable` należy zatem wykonać dopiero dla pliku umieszczonego w docelowej lokalizacji instalacyjnej. Operacja jest per-user: XDG na Linuksie i HKCU na Windowsie.

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

```text
filees-gui / filees CLI
          │ contract/v1 przez lokalny Unix-domain socket
          ▼
daemon: projekcja + supervisor + provisioning + potoki repozytoriów
          │                    │
          │ svn+ssh            └── control/v1 przez SSH
          ▼                                      ▼
repozytoria danych                    forced entry -> worker OpenBSD
                                                  │
                                      rekordy kanoniczne + service repo
                                                  │
                                      view.json wraca jako projekcja
```

Serwer nie ma rezydentnego „daemona FileES”. Systemowy `sshd` uruchamia
ograniczone entrypointy i workery dla pojedynczych operacji. Autoryzacja,
granty, polityka edycji i widoki klientów są własnością serwera; GUI jest
wyłącznie prezentacją, a daemon pozostaje właścicielem stanu lokalnego i SVN.

Dla każdego repozytorium uruchamiany jest niezależny potok:

```
Scanner (watcher) ──events──► Commit Service ──svn──► SVN server
```

### Scanner (`pkg/watcher`)

Cyklicznie przechodzi drzewo kopii roboczej i wykrywa zmiany:

- Sprawdza mtime i rozmiar pliku
- Jeśli rozmiar ≤ 64 MiB i budżet MD5 pozwala — oblicza hash i porównuje z poprzednim; identyczna zawartość nie generuje eventu
- Pliki większe trafiają do backlogu (`md5.backlog.json`); **backlog worker** (osobna goroutine) oblicza MD5 w tle co 5 s, po jednym pliku, wybierając za każdym razem najmniejszy — wynik trafia z powrotem do `s.cur`, co umożliwia wykrywanie renomowań dużych plików
- Usunięcia są debouncowane nie dłużej niż opóźnienie publikacji nowych plików (obecnie 5 min)
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
2. Planuje batch według rzeczywistej liczby plików i sumy bajtów; pojedynczy plik większy od limitu dostaje własny batch
3. Po przekroczeniu `backlog_flush_mib` wymusza publikację bez czekania na zwykły interwał
4. Filtruje przez jawny `svn status --verbose` (rozróżnia `unversioned`, `added` i `normal`)
5. Wykonuje nierekurencyjny `svn add --parents --depth empty`, aby katalog nie omijał limitów, następnie delete/lock/commit
6. Zapisuje numer rewizji do `head.rev`
7. Jeśli commitowane pliki pasują do `shout_patterns` — tworzy ticket powiadomień

Podczas `SIGINT`/`SIGTERM` serwis przestaje przyjmować nowe zmiany, odbiera końcówkę eventów watchera i opróżnia cały staging w ograniczonych batchach. Drain ma osobny `shutdown_commit_timeout`; niewysłana reszta jest atomowo zachowywana w commit cache i wznawiana przy następnym starcie. `SIGKILL`, OOM kill i utrata zasilania nie pozwalają wykonać kodu shutdownu, dlatego trwały cache pozostaje obowiązkową ścieżką recovery. Restart pomija ścieżki już przyjęte przez serwer i publikuje tylko pozostałą część cache.

Przy starcie oraz równolegle w **pollerze HEAD** (co `poll_interval`, domyślnie 30s):
- daemon wykonuje `svn cleanup` i, o ile working copy nie zawiera lokalnie brakujących ścieżek, `svn update`; wpis `missing` odracza update, aby nie odtworzyć lokalnego delete ani źródła rename
- konflikty startowe przechodzą przez tę samą bezstratną reconciliation co konflikty pollera
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
- `repo.reservation_list` agreguje tylko live locki widoczne z lokalnie
  podłączonych WC danego serwera; `repo.reservation_release` ponownie odczytuje
  lock i wymaga jego tokenu, repo, serwera oraz bezpiecznej ścieżki względnej

Zaimplementowane komendy są capability-gated i obejmują system lifecycle,
podpisane aktualizacje, aktywację/pairing, lifecycle repozytoriów (w tym
`repo.load_dump`), granty i widoczność strefy (`repo.grant_access`,
`repo.revoke_access`, `realm.grant_recipients`, `realm.set_visibility`),
aktywność, `repo.lock`, `repo.unlock`, `repo.reservation_list`,
`repo.reservation_release`, `error.list` i `events.subscribe`.
Po wpięciu trwałego aktora daemon reklamuje także `whale.list`, `whale.get`,
`whale.put_begin`, `whale.get_begin`, `whale.get_confirm`, `whale.retry` i
`whale.cancel`. Są to intencje neutralne wobec edytora: GUI, helper CAD i inna
wtyczka 3rd party obserwują ten sam zapisany stan, a event `whale.changed` jest
jedynie sygnałem do ponownego pobrania projekcji.

`whale.get_begin` może przyjąć pełną tożsamość generacji albo tylko repo,
logiczną ścieżkę i rewizję snapshotu. W drugim wariancie serwer wykonuje
metadata-only `GET_DISCOVER`, wycenia rozmiar i SHA, ale nie rezerwuje miejsca
i nie tworzy cache. Bajty mogą ruszyć dopiero po `whale.get_confirm`.

### IPC Client (`pkg/ipcclient`)

Biblioteka używana przez CLI i GUI. Każde zwykłe wywołanie otwiera nowe połączenie do gniazda, wysyła żądanie i czeka na odpowiedź. `Subscribe` utrzymuje osobne, długotrwałe połączenie eventowe. Klient weryfikuje odpowiedzi (protocol, request_id, status), ACK subskrypcji oraz wymagane pola eventów. Deadline połączenia dziedziczy z kontekstu wywołującego — lock/unlock z 30 s kontekstem dostaje 30 s zamiast domyślnych 10 s; anulowanie kontekstu odblokowuje również handshake i oczekiwanie na kolejny event.

### SVN Client (`pkg/client`)

Wrapper na `svn` CLI. Wszystkie wywołania serializowane przez mutex wewnątrz procesu. Timeout per komenda: 30 minut. Wywoływany wyłącznie przez daemon — CLI nigdy nie woła SVN bezpośrednio.

### Runtime Gates (`pkg/runtime`)

Opcjonalne mechanizmy ograniczające równoległość commitów:

- **HostGate** — limit K równoległych commitów w skali hosta (blokada przez `mkdir`)
- **RepoMutex** — maksymalnie 1 commit naraz per repozytorium

Katalog blokady zawiera PID i unikalny token właściciela. Po awarii następny proces atomowo przejmuje blokadę martwego PID; token zapobiega usunięciu nowszej blokady przez spóźnione `release`. Stare katalogi bez metadanych są przejmowane dopiero po okresie ochronnym. Oba mechanizmy zwracają funkcję release, bezpieczną dla wielu goroutine.

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
$XDG_DATA_HOME/filees/whales/ # operacje aktora, PUT payload.ready i stan resume
```

---

## Ignorowanie plików

Daemon zawsze ignoruje: `.svn/`, `.filees/state/`, `.filees/locks/`.

### Wbudowane wzorce (hardcoded, nie można nadpisać)

| Kategoria | Wzorce |
|-----------|--------|
| Pliki tymczasowe Office | `~$*` (MS Office), `.~lock.*#` (LibreOffice/OpenOffice), `*.tmp`, `*.bak` |
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
| `pkg/control/v1` | Wersjonowane koperty ticket/result control plane (`filees.control/v1`) |
| `pkg/whale/v1` | Kanon ścieżki/generacji, stany i framing transportu Whale |
| `pkg/whaleclient` | Trwały aktor PUT/GET, spool/partial, dokładne offsety i pinowany SSH |
| `pkg/provisioning` | Trwała maszyna stanów tworzenia repo i initial commit |
| `pkg/clientview` | Ścisłe dekodowanie projekcji instalacji z service repo |
| `pkg/localrepo` | Trwały lifecycle lokalnych przypięć i ścieżek WC |
| `pkg/reposupervisor` | Dynamiczne uruchamianie, zatrzymywanie i rekoncyliacja repozytoriów |
| `pkg/passport` | Passporty edycji, fencing i migracja `svn:needs-lock` |
| `pkg/repoworker` | Kanoniczne rekordy repozytoriów, granty, polityki i projekcje |
| `internal/whaleworker` | Serwerowy PUT/commit, metadata-only discovery/quote, seekowalny GET cache i jego retencja; sesje nadzoruje `internal/servertool` |
| `pkg/ipcserver` | Serwer gniazda Unix dla CLI/GUI |
| `pkg/ipcclient` | Klient IPC używany przez CLI i GUI |
| `contracttests` | Przekrojowa bramka zgodności kopert, capability i round-trip IPC |
| `pkg/errmap` | Klasyfikacja błędów + zapis do `errors.jsonl` |
| `pkg/runtime` | HostGate, RepoMutex |
| `pkg/talk` | Logger z poziomami i zmienną `FILEES_LOG` |
| `pkg/tickets` | Zapis plików powiadomień `.filees/tickets/` |

---

## Licencja

BSD 2-Clause — zob. [LICENSE](LICENSE).

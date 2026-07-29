# FileES

> ## ⚠️ Wersja robocza — nie do użytku
>
> **To nie jest wydanie. To nie jest beta. To jest robocza wersja roboczego systemu, publikowana wyłącznie po to, żeby kod dało się obejrzeć.**
>
> - Nie ma tu żadnej wersji stabilnej, żadnego wsparcia i żadnej gwarancji — ani działania, ani bezpieczeństwa, ani zachowania danych.
> - Formaty na dysku, protokoły, nazwy poleceń i schematy zmieniają się bez zapowiedzi i bez ścieżki migracji.
> - Nie należy tego wdrażać na maszynie produkcyjnej ani powierzać temu danych, których utrata byłaby problemem.
> - Kod jest ciągle w trakcie audytu i przeglądów; znane defekty bywają otwarte tygodniami, bo priorytet ma projekt, nie polerowanie.
> - Wewnętrzne dokumenty projektowe, raporty z audytu i notatki robocze **nie są publikowane** — repozytorium git jest filtrowanym lustrem repozytorium SVN i zawiera wyłącznie kod, instrukcję i pliki readme. Odnośniki do dokumentów wewnętrznych poniżej nie zadziałają.
>
> Zgłoszenia i pull requesty nie są oczekiwane i mogą pozostać bez odpowiedzi.
>
> ---
>
> **English:** this is a work-in-progress draft of a work-in-progress system, published only so the code can be looked at. It is not a release, has no stable version, no support and no guarantees of any kind — including security and data integrity. On-disk formats, protocols and schemas change without notice or migration. Do not deploy it and do not trust it with data you care about. Internal design and audit documents are deliberately not published; this git repository is a filtered mirror of an SVN repository and contains code, the manual and readme files only. Issues and pull requests are not expected and may go unanswered.

Szczegółowy opis przepływu klienta, invariants, zachowania po awarii, zweryfikowanych edge cases i roadmapy testów znajduje się w [CLIENT_MECHANICS.md](CLIENT_MECHANICS.md).

Raport końcowy audytu i wnioski z testów chaosowych znajduje się w [CLIENT_AUDIT_REPORT.md](CLIENT_AUDIT_REPORT.md). Dokumentację edit-passport uzupełniają [raport implementacji](EDIT_PASSPORT_IMPLEMENTATION_REPORT.md) i [raport testów](EDIT_PASSPORT_TEST_REPORT.md).

Poprawki wynikające z przeglądu r61–r84 opisują [raport implementacji](CODE_REVIEW_FIX_IMPLEMENTATION_REPORT.md) i [raport testów](CODE_REVIEW_FIX_TEST_REPORT.md).

Docelowy model dodawania katalogów, repozytorium technicznego, udostępnień oraz push-deployu tożsamości instalacji opisuje [PROVISIONING_AND_IDENTITY.md](PROVISIONING_AND_IDENTITY.md).

Stan klientowej warstwy push deploy, granice obu połączeń SSH i blokery produkcyjne opisuje [PUSH_DEPLOY_CLIENT_READINESS.md](PUSH_DEPLOY_CLIENT_READINESS.md); wykonane testy są w [PUSH_DEPLOY_CLIENT_TEST_REPORT.md](PUSH_DEPLOY_CLIENT_TEST_REPORT.md).

Natywny projekt narzędzi serwerowych OpenBSD, semantykę jednorazowego ticketu i OTP, podział procesów oraz plan etapów opisuje [SERVER_OPENBSD_WORKER_DESIGN.md](SERVER_OPENBSD_WORKER_DESIGN.md).

Funkcjonalny układ repo serwisowego, projekcję read-only klienta, model administracyjny, ACL oraz recovery opisuje [SERVICE_REPOSITORY_DESIGN.md](SERVICE_REPOSITORY_DESIGN.md).

Lokalny smoke test recovery można uruchomić przez `scripts/svn-recovery-smoke.sh`; tworzy tymczasowe repozytorium SVN i nie wymaga dostępu do sieci.

Neutralny gate jakości dla CI to `make verify`. Obejmuje pełne testy Go, wybrane testy race, `go vet` oraz lokalny smoke test recovery SVN.

Daemon synchronizujący lokalne katalogi z repozytorium SVN. Przeznaczony dla zespołów pracujących na plikach binarnych (grafika, modele 3D, zasoby projektowe). SVN jest tu warstwą transportową i magazynem — semantyka kontroli wersji jest drugorzędna.

Docelowy UX: automat w trayu, który niewidocznie utrzymuje pliki zsynchronizowane z serwerem. Użytkownik nie musi wiedzieć, że pod spodem działa SVN.

---

## Wymagania

- Go 1.24+
- Klient SVN (`svn`) dostępny w `PATH`
- Klient OpenSSH (`ssh`) i aktywna tożsamość instalacji FileES
- Dostęp przez systemowy `sshd` do tunelowego `svnserve -t`; nasłuchujący
  `svnserve --daemon` nie jest obsługiwany

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
    "repo_url": "https://releases.example/FILESS-BIN",
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
| `edit_passports`   | Włącza jawne passporty edycji, migrację `svn:needs-lock`, fencing, heartbeat i kontrolowany autounlock |
| `edit_passport_ttl` | Ważność pojedynczego odnowienia passportu; domyślnie `15m` |
| `edit_passport_heartbeat` | Interwał odnowienia, krótszy od TTL; domyślnie `5m` |
| `edit_passport_max_session` | Nieprzedłużalny limit sesji; domyślnie `24h` |
| `edit_passport_close_grace` | Wymagany okres ciszy po potwierdzonym commicie; domyślnie `5m` |
| `shout_patterns`   | Wzorce regex; pasujące pliki wyzwalają powiadomienie (ticket) |
| `rate_limit_shout` | Minimalny odstęp między powiadomieniami |
| `commit_tiers`     | Size-adaptive interwały (lista rosnąco wg `max_mb`); pominięte = tylko `commit_interval` |

**`commit_tiers`** — każdy wpis to `{"max_mb": N, "interval": "Xm"}`. Daemon sprawdza sumaryczny rozmiar plików w bieżącym batchu i stosuje minimalny odstęp odpowiedniego tieru. `max_mb: 0` to catch-all (ostatni tier). Przykład: batche < 1 MiB co 2 min, 1–10 MiB co 5 min, 10–50 MiB co 15 min, > 50 MiB co 24h.

Czasy podawane w formacie Go: `30s`, `5m`, `1h`.

Pełny lifecycle, inwarianty i granica gwarancji wieloklientowej są opisane w [EDIT_PASSPORTS.md](EDIT_PASSPORTS.md).

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

## GUI Tray — koncepcja

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
- globalne podmenu „Ostatnia aktywność”, równorzędne z „Ostatnimi błędami”,
  pokazujące repozytorium, plik i potwierdzony etap synchronizacji,
- ostatnie błędy z `error.list`, mapowane przez `message_key`, `severity` i `hint`,
- „Połącz ponownie” przy niedostępnym daemonie,
- placeholder „Aktualizacja klienta — w przygotowaniu”, gdy nie ma
  zareklamowanego wydania,
- „Uruchom FileES ponownie…” i „Zamknij FileES…”.

Elementy zależne od komend mutujących są tworzone wyłącznie na podstawie capabilities i świeżego snapshotu. GUI obsługuje obecnie m.in. `events.subscribe`, `repo.create_request`, `repo.detach`, `repo.delete`, `repo.lock`, `repo.unlock`, `repo.reservation_list`, `repo.reservation_release`, `realm.alias_claim`, `system.restart`, `system.shutdown`, `error.list` oraz dynamiczne `update.status`, `update.plan` i `update.apply`. Capability aktualizacji pojawiają się wyłącznie przy kompletnej, podpisanej usłudze update. `Pause`, `Sync now`, publikowanie zmian i decyzje konfliktowe pozostają ukryte do czasu wdrożenia i zareklamowania ich przez daemon.

Tworzenie repozytorium jest zwykłą operacją użytkownika, bez kontaktu z konsolą:

1. W podmenu właściwego serwera wybierz „Dodaj folder do FileES…”. Akcja jest ukryta dla klienta tylko do odczytu, serwera bez zezwolenia lub nieaktualnego połączenia.
2. Wskaż istniejący lokalny folder w natywnym pickerze Linux/Windows.
3. Zaakceptuj nazwę wyprowadzoną z nazwy folderu albo wpisz własną.
4. Sprawdź serwer, folder oraz dostęp `rw` w podsumowaniu i wybierz „Utwórz”.

GUI ponownie sprawdza świeżość i uprawnienia bezpośrednio przed żądaniem IPC. Daemon kanonizuje ścieżkę, odrzuca nakładające się korzenie i trwale zapisuje operację przed odpowiedzią. Dalsze tworzenie na serwerze, import zawartości, pierwszy commit i dołączenie repozytorium odbywają się asynchronicznie; przyjęcie operacji daje pierwsze powiadomienie ("Tworzenie repozytorium rozpoczęte"). GUI odpytuje następnie `repo.lifecycle_status` po `operation_id` (domyślnie co 3 s, do 15 min) i pokazuje drugie powiadomienie z realnym wynikiem: "Repozytorium utworzone" albo, w razie niepowodzenia dowolnego etapu (np. `STORAGE_INSUFFICIENT` przy braku miejsca na serwerze), komunikat błędu z dokładną treścią przyczyny. Repozytorium, które nie przejdzie tego pipeline'u, nigdy nie dostaje zarejestrowanego `RepoID`, więc nie pojawi się w stanie repozytoriów ani w `error.list` — drugie powiadomienie jest jedynym miejscem, gdzie taka porażka jest widoczna.

Odłączenie ma dwa rozłączne kontrakty:

- **Odłącz folder…** zatrzymuje runtime, usuwa wyłącznie `.svn` i `.filees`
  z lokalnego rootu i zostawia wszystkie dane użytkownika jako zwykły folder;
  działa także dla repo offline, a trwały tombstone blokuje ponowne podłączenie
  starego wpisu `config.json`;
- **Odłącz trwale…** wymaga dwóch osobnych potwierdzeń, server-side ownership
  oraz capability administracji repo. Dla retencji `X>0` serwer tworzy i
  weryfikuje pełny dump, natychmiast usuwa FSFS i trzyma wyłącznie dump przez
  `X` dni. Domyślne `X=30` zapisuje dump i manifest z SHA-256 pod
  `results_root/deleted-repositories`; wynik workera zawiera dokładny
  `retain_until`. Dla `X=0` usuwa FSFS natychmiast bez tworzenia dumpa.

Repozytorium `attachment_policy=required` nie udostępnia żadnej z tych akcji.
Lifecycle jest trwały i wznawialny po restarcie.

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
związana z paszportem aktywnym na innym urządzeniu jest tylko informacyjna: w
kolumnie działania ma placeholder **Poproś o zwolnienie (wkrótce)** i nie może
zostać zwolniona z tego klienta. Przycisk **Zwolnij wszystko** obejmuje
wyłącznie rezerwacje możliwe do zwolnienia przez ten klient, wymaga jednego
potwierdzenia i wykonuje każdą operację z jej tokenem. Żądanie pojedynczego
zwolnienia również zawiera token z listy; daemon odczytuje stan ponownie i
odrzuca zmieniony lub nieaktualny wiersz, zanim wywoła SVN.

Alias właściciela jest stałą tożsamością realmu, nie adresem e-mail ani UID.
Po aktywacji istniejącego klienta można go raz ustawić z podmenu serwera przez
**Ustaw stały alias…**. Worker serwerowy waliduje format i unikatowość podczas
zapisu; aliasu nie da się później zmienić.

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
wydanie, wyświetla natywne potwierdzenie i uruchamia istniejący instalator.
Linux zachowuje konfigurację i wyłącza restart/autostart wewnątrz skryptu;
po sukcesie GUI żąda restartu całego stacku, kończy pracę, zwalnia blokadę
single-instance i dopiero wtedy uruchamia nową binarkę. Procedura publikacji:
`tools/RELEASE_PUBLISHING.md`.

### Powiadomienia

Systemowe powiadomienia są wtórne wobec stanu w menu. MVP pokazuje je dla nowych błędów, przejścia repozytorium w stan wymagający uwagi, utraty/odzyskania łączności oraz zakończenia operacji istotnej dla użytkownika. Powtarzające się zdarzenia są grupowane i ograniczane czasowo. Powiadomienia pozostają informacyjne; bezpieczna aktywacja po kliknięciu wymaga osobnego odbioru natywnego i nie może wykonywać operacji mutującej.

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
podpisane aktualizacje, aktywację/pairing, lifecycle repozytoriów, aktywność,
`repo.lock`, `repo.unlock`, `repo.reservation_list`,
`repo.reservation_release`, `error.list` i `events.subscribe`.

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
| `pkg/provisioning` | Trwała maszyna stanów tworzenia repo i initial commit |
| `pkg/ipcserver` | Serwer gniazda Unix dla CLI/GUI |
| `pkg/ipcclient` | Klient IPC — używany przez CLI i docelowo GUI |
| `pkg/errmap` | Klasyfikacja błędów + zapis do `errors.jsonl` |
| `pkg/runtime` | HostGate, RepoMutex |
| `pkg/talk` | Logger z poziomami i zmienną `FILEES_LOG` |
| `pkg/tickets` | Zapis plików powiadomień `.filees/tickets/` |

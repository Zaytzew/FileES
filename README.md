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
./filees
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

## Architektura

Dla każdego repozytorium uruchamiany jest niezależny potok:

```
Scanner (watcher) ──events──► Commit Service ──svn──► SVN server
```

### Scanner (`pkg/watcher`)

Cyklicznie przechodzi drzewo kopii roboczej i wykrywa zmiany:

- Sprawdza mtime i rozmiar pliku
- Jeśli rozmiar ≤ 64 MiB i budżet MD5 pozwala — oblicza hash i porównuje z poprzednim; identyczna zawartość nie generuje eventu
- Pliki większe trafiają do backlogu (`md5.backlog.json`) — hash obliczany przy kolejnej okazji
- Usunięcia są debouncowane 10 minut (ochrona przed chwilowymi ruchami plików)
- W czasie commitu (`commit.busy`) przełącza się w tryb lekki — obserwuje tylko `.filees/tickets/`
- Symlinki są pomijane (FS-0201); widoczne w logach przy poziomie `debug`

**Tryby pracy:**

| Tryb | Opis |
|------|------|
| `Baselining` | Pierwsze uruchomienie — buduje manifest bez emitowania eventów |
| `Active` | Normalny tryb — skanuje i emituje eventy do commit service |

Przejście z Baselining do Active przez plik-flagę `baseline.ok`.

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

### SVN Client (`pkg/client`)

Wrapper na `svn` CLI. Wszystkie wywołania serializowane przez mutex wewnątrz procesu. Timeout per komenda: 30 minut.

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
    │   ├── baseline.ok         # flaga: promuj tmp → aktywny
    │   ├── commit.busy         # flaga: commit w toku (TTL 10 min)
    │   ├── head.rev            # ostatnia commitowana/zaktualizowana rewizja
    │   ├── client.uuid         # stabilny UUID klienta (generowany raz, trwały)
    │   └── md5.backlog.json    # kolejka dużych plików oczekujących na hash
    ├── commit_cache/
    │   └── cache.json          # staging map (przeżywa restart daemona)
    ├── tickets/
    │   └── NOTICE-<uuid>.req   # powiadomienia wychodzące
    ├── logs/
    │   └── errors.jsonl        # structured error log (JSON Lines, append-only)
    └── locks/
        ├── global/             # sloty HostGate
        └── repo/               # blokady RepoMutex
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
| `github.com/google/uuid` | v1.6.0 | Generowanie ID ticketów |

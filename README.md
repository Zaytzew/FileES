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
    "max_batch_files": 100,
    "lock_first":      false,
    "shout_patterns":  ["\\.psd$", "\\.blend$", "\\.obj$"],
    "rate_limit_shout":"5m"
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
| `commit_interval`  | Okno czasowe między commitami (np. `1m`, `30s`) |
| `watch_interval`   | Interwał skanowania (parsowany, rezerwa) |
| `max_batch_files`  | Maks. liczba plików w jednym commicie |
| `lock_first`       | Jeśli `true` — próbuje `svn lock` przed commitem |
| `shout_patterns`   | Wzorce regex; pasujące pliki wyzwalają powiadomienie (ticket) |
| `rate_limit_shout` | Minimalny odstęp między powiadomieniami |

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

**Tryby pracy:**

| Tryb | Opis |
|------|------|
| `Baselining` | Pierwsze uruchomienie — buduje manifest bez emitowania eventów |
| `Active` | Normalny tryb — skanuje i emituje eventy do commit service |

Przejście z Baselining do Active przez plik-flagę `baseline.ok`.

### Commit Service (`pkg/commit`)

Zbiera eventy ze skanera w mapie staging i co `commit_interval` wykonuje commit:

1. Snapshot pending zmian (z uwzględnieniem minimalnego opóźnienia dla nowych plików — 5 min)
2. Filtruje przez `svn status` (nie dodaje plików już wersjonowanych, nie usuwa unversioned)
3. `svn add` → `svn delete` → opcjonalnie `svn lock` → `svn commit`
4. Zapisuje numer rewizji do `head.rev`
5. Jeśli commitowane pliki pasują do `shout_patterns` — tworzy ticket powiadomień

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
└── .filees/
    ├── state/
    │   ├── manifest.json       # aktywny manifest (mtime, size, MD5)
    │   ├── manifest.tmp        # manifest budowany w trybie Baselining
    │   ├── baseline.ok         # flaga: promuj tmp → aktywny
    │   ├── commit.busy         # flaga: commit w toku (TTL 10 min)
    │   ├── head.rev            # ostatnia commitowana rewizja
    │   └── md5.backlog.json    # kolejka dużych plików oczekujących na hash
    ├── tickets/
    │   └── NOTICE-<uuid>.req   # powiadomienia wychodzące
    └── locks/
        ├── global/             # sloty HostGate
        └── repo/               # blokady RepoMutex
```

---

## Ignorowanie plików

Daemon zawsze ignoruje: `.svn/`, `.filees/state/`, `.filees/locks/`.

Dodatkowe wzorce w `.filees/user_ignore.cfg` (hot-reload przy każdym skanie):

```
# komentarz
*.tmp
!*.blend          # twardy ignore (nie skanuje podkatalogów)
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

# public-shares

Publiczny pion FileES: wydawanie danych osobom spoza systemu i przyjmowanie
plików od znanych wnoszących.

Koncepcje, których to jest realizacją:

- `concepts/PUBLIC_SHARE_CONCEPT.md` — kierunek wydawania;
- `concepts/UPLOAD_CHANNEL_CONCEPT.md` — kierunek przyjmowania.

Dokumenty są nadrzędne. Jeżeli kod i koncepcja się rozjeżdżają, to nie znaczy
automatycznie, że kod ma rację — patrz `COMPONENT_MAP.md` §2.

---

## Dwie zasady wiążące dla całego pionu

### 1. Toolchain, nie demon trzymający stan

To jest łańcuch narzędzi, a nie usługa z modelem świata w pamięci.

- **Prawdą jest system plików**, nie proces. Narzędzie czyta stan, działa,
  zapisuje i kończy się. Ten sam wzorzec, co w kolejce DROPPERA i w
  `code-putter`.
- **Brak stanu sesyjnego.** Żądanie wejściowe zamraża rewizję i niesie ją dalej
  w linkach — nie ma ciasteczka, nie ma sesji, nie ma nic, co trzeba pamiętać
  między żądaniami po stronie przeglądarki. Pięciominutowa epoka OTP jest
  trwałym stanem credentialu w systemie plików authority, nie sesją procesu.
- **Restart nie jest zdarzeniem.** Skoro nic nie mieszka w pamięci, ubicie
  procesu w dowolnym momencie nie gubi niczego poza bieżącym żądaniem.

### 2. Ostre granice kontraktu

Każdy pakiet ma zamknięty kontrakt i nie przecieka.

- `manifest` i `slug` są **czyste**: żadnego we/wy, żadnego zegara, żadnego
  stanu. Dają się testować bez infrastruktury i tak mają zostać.
- Warstwa publiczna **konsumuje deklarację**. Nie odkrywa polityki, nie zna
  projektu, nie zgaduje intencji właściciela.
- Rozwiązanie `public_id → ścieżka repozytorium` należy do strony
  autorytatywnej. Ten pakiet nie ma prawa go wykonać i nie dostanie do tego
  danych.

---

## Układ

Najpierw zakres, potem funkcje wewnątrz niego.

```
public-shares/
    slug/         przestrzeń nazw adresu: /<alias-realmu>/<slug>
    manifest/     deklaracje oraz ich czyste niezmienniki
    channel/      kanoniczny lifecycle, ACL i tombstone'y
    gate/         ograniczony Argon2id kanałów otwartych
    recipientotp/ pięciominutowy OTP odbiorców, próby i trwałe epoki
    authority/    frost, bieżąca autoryzacja i dokładny odczyt przez svnlook
    backchannel/  wersjonowany protokół granicy stref
    cache/        prywatny, opcjonalny cache liści z TTL
    storage/      pojemność filesystemu i klasyfikacja awarii, bez autoryzacji
    web/          bezstanowy listing, attachmenty i formularz przyjęcia
    intake/       kwarantanna publicznej maszyny (losowy upload_id, bez SVN)
    linkservice/  konfiguracja i socket procesu FastCGI
```

Kolejne pakiety dochodzą tą samą regułą — nazwa mówi, co robi, a nie z czego
się składa.

## Stan

M47 (checkpoint 2026-09-08): regresja storage naprawiona w r922 i odebrana
na cloud: podpisany kanał alpha FILEES-BIN r59, migracja /home/_filees,
restart authority/links, rzeczywiste file/Range/ZIP PASS. To nie odbiór
pozostałych hostów ani test całego dużego udziału.
Staging (`server.json`: `public_shares.authority_staging_root`) i cache
(`public-links.json`: `cache.root`) są niezależnie konfigurowalne. Katalogi
muszą przetrwać systemowe sprzątanie, być prywatne i wykluczone z backupu.
Nowy domyślny układ: `/var/filees-downloads/{authority,cache}`; nie wymusza
tego wolumenu na operatorze. Instalator ma `install.public_downloads_dir`
dla migracji starych domyślnych ścieżek; niestandardowych nie nadpisuje.
Zmiana konfiguracji wymaga restartu i nowego unveil. Awaria miejsca lub
lokalnego stagingu/cache po autoryzacji daje 503/Retry-After, bez prywatnych
szczegółów. Odmowa nadal jest 404. ZIP strumieniuje wyjście, ale wcześniej
materializuje liście w cache; limity logiczne nie zastępują kontroli dysku.
Migracja i bramka wdrożenia: `packaging/server/public-storage-migration.md`.

M48 na bazie r924: zaimplementowany GC wewnątrz obu usług, bez crona.
Start + timer domyślnie 5m; `cache.cleanup_interval` w public-links.json
oraz `public_shares.cleanup_interval` w server.json (1s–1h).
TTL od zapisu odmawia nowych trafień; Reader i cały przygotowany ZIP
pozostają przypięte do zakończenia/Close. Ponowny autoryzowany fetch może
odnowić TTL; samo trafienie nie. Staging jest chroniony od utworzenia.
Wyłączność katalogu i wspólny tracker pozwalają zbierać rozpoznane orphany
po crashu. GC nie usuwa korzeni, blokady, obcych plików ani symlinków.
Błędy usuwania i liczniki trafiają do prywatnego `.maintenance-status.json`;
obie binarki mają odczytowy `-check-maintenance` (JSON + kod wyjścia).
Fizyczne usunięcie następuje w najbliższym udanym przebiegu po końcu użycia;
postój i błędy odraczają GC. To nie ścisły deadline ani dowód erasure.
Linux -race/vet i izolowane OpenBSD PASS. Cloud: podpisane r927/alpha
(FILEES-BIN r61) wdrożone 2026-09-08; GC start/timer, oba health oraz
file/Range/ZIP PASS. Pierwszy upgrade cloud wykonano po stop obu legacy.
Pierwszy upgrade musi zatrzymać stare procesy przed nowymi; stare r922
nie respektuje blokady. Wyłączony cache nie jest sprzątany automatycznie.
Dowód: [odbiór M48](../reports/PUBLIC_SHARES_MAINTENANCE_2026-09-08.md).
Instrukcja operatora: [storage](../packaging/server/public-storage-migration.md).

Kanał dystrybucji jest zaimplementowany pionowo: owner tworzy, aktualizuje,
odwołuje i usuwa kanał przez control-plane; tożsamość ownera pochodzi z sesji,
a grant `rw` nie wystarcza do publikacji. Publiczna projekcja nie potrafi
reprezentować `repo_id`, `source_root` ani ścieżki repozytorium. Wejście zamraża
konkretną rewizję, każde użycie cache'u jest poprzedzone ponowną autoryzacją,
a revoke daje niejawne `404` także dla ciepłego wpisu.
Wszystkie odpowiedzi kończące się 404 mają ten sam obrandowany HTML i nie
odbijają aliasu, slugu, żądanej ścieżki ani przyczyny niedostępności. Wygasła
wizyta recipienta z nadal aktywnym zaproszeniem wraca do bramki OTP, nie 404.

Desktop ownera jest również kompletny: dynamiczne IPC udostępnia
list/create/update/revoke/delete, a natywne Ustawienia Windows/Linux budują
deklarację z wybranego podfolderu WC. Plaintext hasła jest hashowany przed
ticketem; lista pokazuje tylko flagę ochrony, a update może zachować istniejący
verifier bez przesyłania go przez control ani IPC. Ta powierzchnia nie zmienia
granicy zaufania: daemon wykonuje ergonomiczną bramkę ownership, worker
autoryzuje ponownie na podstawie uwierzytelnionej sesji.

Wygląd listingów jest dziedziczony z jednego kanonicznego rekordu realmu, a
nie kopiowany do kanałów. Owner ustawia dokładnie dwie wartości: kolor wiodący
`#RRGGBB` i opcjonalne logo PNG/JPEG. GUI przyjmuje źródło do 16 MiB i
proporcjonalnie przygotowuje wariant webowy do 32 KiB; wynik jest dekodowany i
sprawdzany przed zapisem, a na stronie używa `object-fit: contain`.
Nie ma powierzchni dla własnego CSS, fontów, URL-i ani SVG. Zmiana obowiązuje
od razu we wszystkich istniejących i przyszłych udziałach realmu.

Proces `filees-public-authority` pozostaje po stronie FileES i jako jedyny zna
FSFS oraz mapowanie `public_id` na ścieżkę. `filees-links` ma wyłącznie prywatny
cache, klucz krótkiej capability wizyty oraz połączenie backchannel; nie ma
poświadczeń SVN. TLS i publiczne HTTP kończy zewnętrzny serwer, na OpenBSD
`httpd(8)`, który przekazuje żądania do FastCGI.

Kanał odbiorców wysyła link będący wyłącznie niejawnym identyfikatorem
zaproszenia. Publiczna projekcja nie zawiera adresów e-mail, a sam link nie
autoryzuje. Jawne „Wyślij kod” aktywuje pięciominutową epokę OTP po stronie
authority; pięć błędnych prób zamyka epokę, a resend nie rotuje kodu ani TTL.
Po poprawnej weryfikacji `filees-links` wydaje podpisany URL ważny dokładnie do
końca tej epoki. Nie używa cookies, localStorage ani JavaScriptu, poza formularzem przyjęcia:
jeden inline skrypt zahashowany w CSP obsługuje pole drag-and-drop, pokazuje
oczekiwanie od „Wyślij” do potwierdzenia i blokuje drugie submit. Listing i OTP
zostają bez skryptu.
`filees-mail
public-loop` jest odizolowanym dzieckiem authority i jako jedyny z tej pary
ładuje sekret SMTP; trwały outbox przeżywa restart obu procesów. Authority
przechwytuje sygnał zatrzymania usługi i czeka na zakończenie dziecka, więc
restart nie zostawia równoległego, osieroconego pollera.

Zasoby mają twarde granice w kodzie: pojedynczy liść nie przekracza polityki
`public_shares.max_size`, dwie operacje `svnlook` mogą trwać równolegle,
jednoczesne chybienia tego samego liścia są scalane, a kosztowna weryfikacja
Argon2id jest serializowana. Limit połączeń i tempo żądań ustawia frontujący
serwer HTTP. Host może ponadto ograniczyć liczbę aktywnych lub odwołanych
kanałów realmu i wymagać hasła dla kanałów bez listy odbiorców.

Manifest kanału przyjęcia pozostaje tylko rozpoczętym kontraktem. Receiver,
kwarantanna i AV dla Upload Channel są osobnym etapem i nie współdzielą procesu
ani poświadczeń z dystrybucją.

## Testy

```sh
go test ./public-shares/...
```

Pakiety tego pionu nie wymagają sieci, SVN-a ani uprawnień. Jeżeli któryś
kiedykolwiek będzie wymagał, to znaczy, że złamał zasadę 2.

Pełny test z prawdziwym repozytorium FSFS i `svnlook` znajduje się w
`internal/servertool/public_shares_e2e_test.go`; wymaga dostępnych binariów SVN.

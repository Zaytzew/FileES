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
  między żądaniami.
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
    gate/         tokeny odbiorców i ograniczony Argon2id
    authority/    frost, bieżąca autoryzacja i dokładny odczyt przez svnlook
    backchannel/  wersjonowany protokół granicy stref
    cache/        prywatny, opcjonalny cache liści z TTL
    web/          bezstanowy listing i attachmenty
    linkservice/  konfiguracja i socket procesu FastCGI
```

Kolejne pakiety dochodzą tą samą regułą — nazwa mówi, co robi, a nie z czego
się składa.

## Stan

Kanał dystrybucji jest zaimplementowany pionowo: owner tworzy, aktualizuje,
odwołuje i usuwa kanał przez control-plane; tożsamość ownera pochodzi z sesji,
a grant `rw` nie wystarcza do publikacji. Publiczna projekcja nie potrafi
reprezentować `repo_id`, `source_root` ani ścieżki repozytorium. Wejście zamraża
konkretną rewizję, każde użycie cache'u jest poprzedzone ponowną autoryzacją,
a revoke daje niejawne `404` także dla ciepłego wpisu.

Desktop ownera jest również kompletny: dynamiczne IPC udostępnia
list/create/update/revoke/delete, a natywne Ustawienia Windows/Linux budują
deklarację z wybranego podfolderu WC. Plaintext hasła jest hashowany przed
ticketem; lista pokazuje tylko flagę ochrony, a update może zachować istniejący
verifier bez przesyłania go przez control ani IPC. Ta powierzchnia nie zmienia
granicy zaufania: daemon wykonuje ergonomiczną bramkę ownership, worker
autoryzuje ponownie na podstawie uwierzytelnionej sesji.

Wygląd listingów jest dziedziczony z jednego kanonicznego rekordu realmu, a
nie kopiowany do kanałów. Owner ustawia dokładnie dwie wartości: kolor wiodący
`#RRGGBB` i opcjonalne logo PNG/JPEG. Logo ma limit 32 KiB i 2048 px, jest
dekodowane i sprawdzane przed zapisem, a na stronie używa `object-fit: contain`.
Nie ma powierzchni dla własnego CSS, fontów, URL-i ani SVG. Zmiana obowiązuje
od razu we wszystkich istniejących i przyszłych udziałach realmu.

Proces `filees-public-authority` pozostaje po stronie FileES i jako jedyny zna
FSFS oraz mapowanie `public_id` na ścieżkę. `filees-links` ma wyłącznie prywatny
cache, klucz krótkiej capability wizyty oraz połączenie backchannel; nie ma
poświadczeń SVN. TLS i publiczne HTTP kończy zewnętrzny serwer, na OpenBSD
`httpd(8)`, który przekazuje żądania do FastCGI.

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

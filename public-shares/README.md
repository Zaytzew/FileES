# public-shares

Publiczny pion FileES: wydawanie danych osobom spoza systemu i przyjmowanie
plików od znanych wnoszących.

Koncepcje, których to jest realizacją:

- `concepts/PUBLIC_SHARE_CONCEPT.md` — kierunek wydawania;
- `concepts/UPLOAD_CHANNEL_CONCEPT.md` — kierunek przyjmowania.

Dokumenty są nadrzędne. Bieżący wyjątek jest jawny: manifest uploadu z r376
używa jeszcze `trash_repo_id`, podczas gdy robocza koncepcja przeszła na
per-channel `upload_repo_id` i jeden realm-wide `trash_repo`. Do czasu
migracji nie wolno traktować schematu uploadu z tego pakietu jako wydanego
kontraktu.

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
    slug/        przestrzeń nazw adresu: /<alias-realmu>/<slug>
    manifest/    oba manifesty, typy i niezmienniki
```

Kolejne pakiety dochodzą tą samą regułą — nazwa mówi, co robi, a nie z czego
się składa.

## Stan

Zaczęte. Zaimplementowane:

- `slug` — walidacja i składanie adresu publicznego, z przedrostkiem aliasu
  realmu (`PUBLIC_SHARE_CONCEPT.md` §5.1);
- `manifest` — `Share` oraz pierwszy, **nieaktualny kontraktowo** wariant
  `Upload`; część share może być rozwijana, część upload wymaga najpierw
  migracji pól i testów do aktualnej koncepcji.

Do zrobienia, w kolejności zależności: bramka wejścia (token, OTP, hasło kanału
otwartego), generowanie listingu, cache kluczowany rewizją, backchannel v1,
odbiornik uploadu, binarek publiczny.

## Testy

```sh
go test ./public-shares/...
```

Pakiety tego pionu nie wymagają sieci, SVN-a ani uprawnień. Jeżeli któryś
kiedykolwiek będzie wymagał, to znaczy, że złamał zasadę 2.

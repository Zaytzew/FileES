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
    slug/        przestrzeń nazw adresu: /<alias-realmu>/<slug>
    manifest/    oba manifesty, typy i niezmienniki
```

Kolejne pakiety dochodzą tą samą regułą — nazwa mówi, co robi, a nie z czego
się składa.

## Stan

Zaczęte. Zaimplementowane:

- `slug` — walidacja i składanie adresu publicznego, z przedrostkiem aliasu
  realmu (`PUBLIC_SHARE_CONCEPT.md` §5.1);
- `manifest` — `Share` i `Upload` z walidacją niezmienników, w tym rozdziału
  `authority_repo_id` od per-channel `upload_repo_id` (`UPLOAD_CHANNEL_CONCEPT.md`
  §2, §3) oraz obowiązkowej listy odbiorców przy kanale przyjęcia (§3.1).
  Realm-wide `trash_repo` świadomie nie jest polem tego manifestu (§3, §6).

Do zrobienia, w kolejności zależności: bramka wejścia (token, OTP, hasło kanału
otwartego), generowanie listingu, cache kluczowany rewizją, backchannel v1,
odbiornik uploadu, binarek publiczny.

## Testy

```sh
go test ./public-shares/...
```

Pakiety tego pionu nie wymagają sieci, SVN-a ani uprawnień. Jeżeli któryś
kiedykolwiek będzie wymagał, to znaczy, że złamał zasadę 2.

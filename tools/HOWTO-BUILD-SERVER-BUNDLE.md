# Jak zbudować bundel serwerowy

Przepis operacyjny. **Dlaczego** tak — w `RELEASE_PUBLISHING.md`; tutaj są
komendy i te rzeczy, na których kolejne sesje traciły czas, odkrywając je od
nowa.

Kończysz na **niepodpisanym** bundlu zacommitowanym do `FILEES-BIN`. Podpis
składa właściciel na osobnej maszynie. Klucz prywatny nie ma prawa pojawić się
na hoście budującym ani w kopii roboczej.

## Klucz publiczny leży w `FILEES-BIN`, nie w `~/.signify`

```
FILEES-BIN/FILEESrelease.pub
```

Jest wersjonowany, więc masz go w każdej kopii roboczej `FILEES-BIN`.
`RELEASE_PUBLISHING.md` pokazuje w przykładach `$HOME/.signify/filees-release.pub`
i to jest ścieżka z maszyny podpisującej — na hoście budującym zwykle nie
istnieje.

**Nie używaj kluczy z drzewa źródeł.** `cmd/filees/assets/release.pub` i
`cmd/filees-install/assets/release.pub` to **zaślepki** (`RWTxxxx…`).
`build-server.sh` je odrzuca, ale najpierw je znajdziesz i stracisz na to
kwadrans. Prawdziwy klucz jest wstrzykiwany do binariów przez `ldflags` w
czasie budowania, nie leży w źródłach.

## Host budujący nie ma znaczenia

`build-server.sh` cross-kompiluje przez `GOOS`/`GOARCH` i **sprawdza**, czy Go
faktycznie zaraportowało żądaną platformę. Buduj z Windows, z Linuksa, z VM
OpenBSD albo ze `spot` — wychodzi ten sam ELF. Weryfikacja:

```bash
file FILEES-BIN/releases/<ID>/openbsd-amd64/bin/filees-admin
# ELF 64-bit LSB executable, x86-64, ... for OpenBSD
```

Potrzebne lokalnie: `go`, `svn`, POSIX `sh` (na Windows wystarczy Git Bash).

## Konwencja identyfikatorów

`RELEASE_ID` to `r<rewizja źródeł>`, a `SEQUENCE` to ta sama liczba bez `r`.
`SECURITY_EPOCH` zostaje `1`, dopóki nie unieważniamy starszych wydań.

```
r688 → RELEASE_ID=r688  SEQUENCE=688  SECURITY_EPOCH=1
```

Wydanie jest **niezmienne**: skrypt odmówi, jeśli `releases/<ID>` już istnieje.
Poprawka wychodzi jako nowe wydanie z wyższym numerem, nigdy jako nadpisanie.

## Warunki wstępne, które skrypt egzekwuje

Obie kopie robocze — źródła **i** `FILEES-BIN` — muszą być czyste i na HEAD.
„Czyste" znaczy w `FILEES-BIN` również **bez plików nieversjonowanych**, więc
posprzątaj śmieci, zanim zaczniesz.

## Przebieg

```bash
cd <źródła>
svn up -q
REV=$(svn info --show-item revision | tr -d '\r\n')

cd <FILEES-BIN>
svn up -q

cd <źródła>
FILEES_BIN_WC="<FILEES-BIN>" \
FILEES_RELEASE_PUBKEY="<FILEES-BIN>/FILEESrelease.pub" \
RELEASE_ID="r$REV" SEQUENCE="$REV" SECURITY_EPOCH=1 \
sh tools/prepare-server-release.sh
```

Powstaje `releases/r$REV/` z `openbsd-amd64/bin/` (17 binariów serwerowych),
`examples/install.example.conf`, `manifest.json` i `channel.json`.

## Przegląd przed commitem

```bash
cd <FILEES-BIN>
ls releases/r$REV/openbsd-amd64/bin/ | wc -l     # 17
grep -c '"target"' releases/r$REV/openbsd-amd64/manifest.json
svn status releases/r$REV
```

Sprawdź, czy w `bin/` jest **każde** binarium, którego oczekujesz. Binarka
obecna w payloadzie, ale nieobecna w `packaging/server/openbsd-binary-policy.json`,
**znika z manifestu bez ostrzeżenia** — tak `filees-serving-state` nie
zainstalował się mimo obecności w wydaniu, aż do r688. Liczba wpisów manifestu
i liczba plików muszą się zgadzać.

## Commit

**Tylko `releases/<ID>`.** Nie dotykaj `channels/` na hoście budującym:
promocja kanału to część podpisywania i należy do maszyny podpisującej.

```bash
svn add releases/r$REV --force
svn commit releases/r$REV -m "Add unsigned server release r$REV (openbsd-amd64)"
```

Skrypt **niczego nie commituje sam** — kończy się wypisaniem, co przejrzeć.

## Potem

Właściciel podpisuje i promuje kanał (`release-sign-and-publish.sh`,
§2 `RELEASE_PUBLISHING.md`), a na serwerze wchodzi `filees-install --apply`.
Dopóki to nie nastąpi, **kod w źródłach nikogo nie chroni** — wydanie
niepodpisane nie jest wdrożeniem.

## Znany brak

`build-server.sh` wstrzykuje klucz tylko wtedy, gdy `FILEES_RELEASE_PUBKEY` jest
ustawione, a przy braku zmiennej buduje dalej **bez** klucza. Ścieżka wydania
jest bezpieczna, bo `prepare-server-release.sh` wymaga zmiennej i odrzuca
zaślepkę — ale gołe `build-server.sh` wyprodukuje binaria, które nie zweryfikują
niczego. Niezmiennik do dopisania; zapisany też w macierzy jako M15.

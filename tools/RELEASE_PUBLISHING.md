# FileES release publishing

Publikacja rozdziela maszynę budującą od maszyny podpisującej. Prywatny klucz
release nie może znajdować się na build hoście, VM testowej ani w repozytorium.

## 1. Deterministyczny manifest

Produkcyjny build klienta przyjmuje wyłącznie publiczny trust root:

```sh
FILEES_RELEASE_PUBKEY="$HOME/.signify/filees-release.pub" \
FILEES_RELEASE_KEY_ID="release-2026-a" \
./packaging/build-gui.sh linux-amd64
```

Obie zmienne muszą wystąpić razem. Build odrzuca placeholder i nie zna żadnej
ścieżki ani zmiennej private key. Publiczny klucz jest kodowany base64 i
wstrzykiwany do binarki daemona przez linker; źródłowy placeholder i WC nie są
modyfikowane. `build-gui.sh` tworzy katalog bundla oraz deterministyczny
`filees-client-linux-amd64.tar.gz` przez `filees-release-bundle`: sortowane
ścieżki, uid/gid i czas równe zero, tryby 0755/0644, bez symlinków i plików
specjalnych.

Na maszynie budującej przygotuj katalog payloadu oraz recenzowalną specyfikację:

```json
{
  "release_id": "r178",
  "platform": "openbsd-amd64",
  "svn_revision": "178",
  "files": [
    {
      "source": "bin/filees-admin",
      "target": "{sbin_dir}/filees-admin",
      "kind": "binary",
      "mode": "0755"
    }
  ]
}
```

Generator nie przyjmuje digestów od operatora. Czyta każdy regularny plik z
payloadu, odrzuca traversal, wyjście przez symlink, powtórzone źródła i targety
oraz nieznane pola specyfikacji. Wpisy sortuje po `source`; nie dodaje bieżącej
daty, dlatego identyczne wejście daje identyczny manifest.

```sh
go run ./cmd/filees-release-manifest \
  -spec release-openbsd-amd64.json \
  -payload dist/filees-server-openbsd-amd64 \
  -output FILESS-BIN/releases/r178/openbsd-amd64/manifest.json
```

Specyfikacja jest polityką instalacji: targety, typy i tryby (w tym set-id)
muszą być jawnie recenzowane. Generator nie zgaduje ich na podstawie bundla.
Po wygenerowaniu należy sprawdzić diff i commitować payload, manifesty wszystkich
wymaganych komponentów/platform oraz plik kanału jako kompletne, jeszcze
niepodpisane wydanie.

Manifest v1 jest konsumowany przez serwerowy `filees-install`. Klientowe
instalatory Linux/MSI/Android pozostają własnymi wykonawcami lifecycle'u; ich
artefakty są związane wspólnym wielokomponentowym release envelope v2, zamiast
otrzymywać serwerowe pola `target`.

Envelope klienta ma ścisły schemat i wskazuje manifest każdego
`component/platform`. Przykład kanału:

```json
{
  "schema_version": 2,
  "release_id": "r188",
  "sequence": 188,
  "security_epoch": 1,
  "key_id": "release-2026-a",
  "expires_at": "2026-08-22T00:00:00Z",
  "components": [
    {
      "name": "desktop",
      "platform": "linux-amd64",
      "manifest": "releases/r188/desktop/linux-amd64/manifest.json"
    }
  ]
}
```

Manifest v2 musi powtórzyć `release_id`, `sequence`, `security_epoch`, `key_id`,
`component` i `platform`, podać wersję oraz dokładny rozmiar i SHA-256 bundla.
Kanał i manifest są podpisywane oddzielnie tym samym zaufanym kluczem. Nie wolno
ponownie użyć sequence dla innego release ani go obniżyć; zainstalowany klient
utrzymuje trwały high-water mark.

## 2. Podpisanie offline

Na dedykowanej maszynie podpisującej, w czystej i aktualnej WC FILESS-BIN:

```sh
FILESS_BIN_WC="$HOME/FILESS-BIN" \
SIGNIFY_SEC_KEY="$HOME/.signify/filees-release.sec" \
SIGNIFY_PUB_KEY="$HOME/.signify/filees-release.pub" \
CHANNEL=stable ./tools/release-sign-and-publish.sh
```

Skrypt preferuje `channels/stable.v2.json` (z fallbackiem do v1), podpisuje
każdy manifest `component/platform` i kanał, natychmiast weryfikuje
nowe podpisy kluczem publicznym i commituje wyłącznie odłączone pliki `.sig`.
Jeśli cały release jest już poprawnie podpisany, wykonanie jest no-op.

## 3. Odbiór klienta i test regresyjny

Po publikacji produkcyjny klient z odpowiednim embedded keyringiem powinien:

1. pokazać badge nowej wersji dopiero po weryfikacji envelope i manifestu;
2. zwrócić dry run bez modyfikacji filesystemu;
3. przed apply ponownie rozwiązać wydanie i pokazać natywne potwierdzenie;
4. sprawdzić rozmiar i SHA-256 bundla przed ekstrakcją;
5. uruchomić istniejący instalator platformowy i dopiero po sukcesie zapisać
   high-water mark;
6. zwolnić blokadę GUI przed uruchomieniem nowej binarki.

Reprodukowalne E2E jest częścią `go test ./internal/clientupdate`. Tworzy lokalne
repo SVN, podpisuje release testowym kluczem w formacie OpenBSD signify, wykonuje
rzeczywisty `packaging/linux/install-user.sh` w izolowanym HOME i potwierdza
odmowę rollbacku, złego podpisu oraz artefaktu o niezgodnym SHA-256. Testowy
klucz powstaje wyłącznie w katalogu tymczasowym i nie jest trust rootem wydania.

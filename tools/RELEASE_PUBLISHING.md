# FileES release publishing

Publikacja rozdziela maszynę budującą od maszyny podpisującej. Prywatny klucz
release nie może znajdować się na build hoście, VM testowej ani w repozytorium.

## 1. Deterministyczny manifest

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
artefakty będą związane wspólnym wielokomponentowym release envelope, zamiast
otrzymywać serwerowe pola `target`.

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

# FILEES-BIN

Repozytorium zawiera wyłącznie publiczne artefakty wydań FileES. Prywatny klucz
signify nigdy nie może trafić do tej kopii roboczej ani na host budujący.

Kanoniczny układ:

```text
FILEESrelease.pub
channels/
  alpha.json
  alpha.json.sig
  beta.json
  beta.json.sig
  stable.json
  stable.json.sig
releases/
  <release-id>/
    channel.json
    openbsd-amd64/
      manifest.json
      manifest.json.sig
      bin/
      examples/
tools/
  release-sign-and-publish.sh
```

Release jest publikowany w dwóch commitach SVN:

1. host budujący dodaje tylko niezmienny `releases/<release-id>`;
2. maszyna podpisująca dodaje podpisy manifestów i promuje podpisany kanał w
   jednym commicie.

Kanał jest obowiązkową, jawną decyzją operatora podpisującego:

- `alpha` — wczesne testy wewnętrzne;
- `beta` — wydanie funkcjonalnie kompletne do szerszych testów;
- `stable` — wydanie produkcyjne po pełnym odbiorze.

Kandydat `channel.json` nie zawiera nazwy kanału. Ten sam podpisany release można
później promować do kolejnego kanału bez przebudowy payloadu. Release, na który
nie wskazuje żaden podpisany plik w `channels/`, nie jest opublikowany klientom.

Nigdy nie zmieniaj już opublikowanego payloadu ani manifestu. Poprawka jest
nowym release ID i nową, nieużywaną wartością `sequence`.

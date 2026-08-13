# FILEES-BIN

Repozytorium zawiera wyłącznie publiczne artefakty wydań FileES. Prywatny klucz
signify nigdy nie może trafić do tej kopii roboczej ani na host budujący.

Kanoniczny układ:

```text
FILEESrelease.pub
channels/
  stable.json
  stable.json.sig
releases/
  <release-id>/
    channel-stable.json
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

Nigdy nie zmieniaj już opublikowanego payloadu ani manifestu. Poprawka jest
nowym release ID i nową, nieużywaną wartością `sequence`.

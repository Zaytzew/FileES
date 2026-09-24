# demo.filees.space: strona startowa

`www/index.html` to strona pod `https://demo.filees.space/`. Wcześniej był
tam wyłącznie ręcznie wgrany plik bez źródła w repozytorium (2026-09-17).

## Ograniczenia, które wymusza serwer

httpd serwuje statycznie tylko `/` i `/index.html`, a każdą inną ścieżkę
przekazuje do linków publicznych FileES (FastCGI, `/<alias>/<slug>`). Dlatego
strona jest jednym plikiem: CSS, logo (SVG z `branded-assets`, przekolorowane
na białe) i skrypt przełącznika języka są w środku. Nie ładuje niczego z
zewnątrz: żadnych czcionek, analityki ani obrazów spoza pliku.

## Wdrożenie

Strona zmienia tylko plik statyczny. Usługi FileES się nie restartują, a
aktywacje demo nie są przerywane.

```sh
scp -i <klucz demo> packaging/demo-server/www/index.html gpt_ai@202.61.192.51:/tmp/demo-index.html
ssh -i <klucz demo> gpt_ai@202.61.192.51 \
  'doas install -o root -g daemon -m 0644 /tmp/demo-index.html /var/www/htdocs/demo.filees.space/index.html && rm /tmp/demo-index.html'
```

Przed wdrożeniem trzeba zajrzeć do pliku na serwerze: jeśli różni się od
wersji z repozytorium, najpierw ustalić, kto i dlaczego go zmienił.

## Treść

Fakty na stronie muszą zgadzać się z polityką demo:
- 120 minut i 1 GB;
- jedna aktywacja na instalację;
- linki publiczne i półki bez przesyłania plików;
- bez grantów i bez parowania telefonu;
- zapis aktywacji usuwany po 14 dniach (r1491).

Przy zmianie polityki trzeba zaktualizować także tę stronę.

## Historia wdrożeń

- **2026-09-24, r1525.** Wdrożone za zgodą właściciela w trakcie
  certyfikacji Sklepu. Plik jest statyczny i nie dotyka usług FileES.
  Poprzednia wersja (657 B) leży w `/root/demo-index.html.before-20260924`.
  SHA-256 `00c7a009…e2` jest zgodny z repozytorium, a linki publiczne nadal
  trafiają do FastCGI.

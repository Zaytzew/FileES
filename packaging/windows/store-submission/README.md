# FileES Desktop — zgłoszenie MSIX do Microsoft Store

Stan: lokalne narzędzie przygotowane 2026-10-03; nie wykonano jeszcze odczytu
produkcyjnego Partner Center ani żadnego zapisu. Źródłem prawdy o zgłoszeniach
jest Partner Center. Przed pierwszym żywym użyciem sprawdź, czy r1882 nie został
już wysłany ręcznie.

## Granice i decyzje

- `preview` sprawdza paczkę i notatki lokalnie; z dostępem API odczytuje produkt,
  ostatnią publikację i bieżący szkic. Nie tworzy szkicu.
- `prepare` wymaga osobnej zgody właściciela na utworzenie szkicu i upload
  konkretnej paczki. Kopiuje ostatnie opublikowane zgłoszenie. Zmienia tylko
  status starej paczki, dodaje nową paczkę, aktualizuje `releaseNotes` w
  istniejących listingach `pl-pl` i `en-us` oraz ustawia `targetPublishMode`
  na `Manual`. Pozostałe metadane zachowuje. Podgląd ścieżek różnic jest w
  `preview`; po utworzeniu narzędzie porównuje kopię z publikacją i odmawia,
  jeśli Microsoft zmienił inne pola. Nie usuwa szkiców.
- `submit` to osobna decyzja dla dokładnego ID zgłoszenia, wersji i SHA-256
  podsumowania wyświetlonego po `prepare`. Wywołuje `commit`, czyli wysyła do
  certyfikacji. Nie jest to upload paczki ani publikacja po certyfikacji.
- Publikacja po certyfikacji pozostaje ręczna w Partner Center. Ustawienie
  `Manual` jest weryfikowane ponownie przed `commit`. Oglądanie w portalu jest
  dopuszczalne; nie edytuj w portalu szkicu utworzonego przez API, bo według
  Microsoft może to uniemożliwić dalsze użycie API dla zgłoszenia.
- Jeżeli istnieje obcy szkic lub trwa certyfikacja, `prepare` odmawia. Przy
  niepewnym wyniku `POST create` zapisany wcześniej `create-intent` wymusza
  ręczne uzgodnienie stanu. Nie tworzy drugiego szkicu. Po przerwanym uploadzie
  ponowienie używa zapisanego ID i pełnej kopii metadanych; odmówi, jeśli
  żywy szkic został zmieniony. Stan `commit-intent` nie ponawia `commit`.

Nie używaj `msstore publish`: bieżąca dokumentacja Microsoft mówi, że może
usunąć istniejący szkic i odtworzyć go z ostatniej publikacji. API dla
pakietów MSIX ma osobne `create`, `PUT`, upload ZIP i `commit`. Nie stosuj API
dla niepakowanych MSI/EXE do tego produktu.

## Przygotowanie dostępu — wymaga osobnej zgody właściciela

1. W Partner Center sprawdź stan produktu `9P0J1BRL5K77`, w tym wersję
   opublikowaną, ewentualny szkic i certyfikację. Jeśli r1882 jest już
   zgłoszone, nie twórz kolejnego zgłoszenia.
2. Właściciel potwierdził typ konta **Company**. Logowanie osobistym kontem
   Microsoft nie zastępuje tożsamości aplikacji wymaganej przez API. Według
   Microsoft właściciel konta utworzonego przez MSA ma pełny dostęp w Partner
   Center; nie potrzebuje osobno przypisanej roli Manager. Brak dostępu tego
   MSA do portalu Entra bez powiązanej dzierżawy jest oczekiwany.
3. W Partner Center sprawdź **Settings → Account settings → Organization
   profile → Tenants → Developer**. Tu Microsoft pokazuje powiązane dzierżawy
   i umożliwia powiązanie istniejącej lub utworzenie nowej. Następnie sprawdź
   **Account settings → User management → Microsoft Entra applications**.
   Zarządzanie aplikacją wymaga konta Owner lub Manager w Partner Center oraz
   uprawnień Global administrator w dzierżawie. Jedną aplikację z rolą
   Manager można wykorzystywać do kolejnych zgłoszeń produktów na tym koncie.
   Jeśli sekcja Tenants nie jest widoczna pod Organization profile na koncie
   Owner, nie zakładaj, że utworzenie dzierżawy poza Partner Center samo ją
   powiąże.
   Microsoft opisuje alternatywę: rejestracja bezpłatnego konta Azure tworzy
   nową dzierżawę i nadaje rejestrującemu uprawnienia Global administrator.
   Nadal trzeba powiązać tę dzierżawę z Partner Center. Bez widocznej funkcji
   powiązania albo jej udokumentowanego odpowiednika konfiguracja API pozostaje
   zablokowana. Utworzenie lub powiązanie dzierżawy, aplikacji, roli i sekretu
   wymaga osobnej decyzji właściciela. Po konfiguracji kolejne aktualizacje
   wysyłaj przez API.
4. Dopiero po zatwierdzeniu konfiguracji zapisz identyfikator dzierżawy w
   `%LOCALAPPDATA%\FileES\StoreSubmission\account.json`, np.
   `{"tenantId":"<GUID>"}`. Klient ID i sekret przechowuj w lokalnym,
   związanym z użytkownikiem i komputerem magazynie DPAPI:

   ```powershell
   $privateDir = Join-Path $env:LOCALAPPDATA 'FileES\StoreSubmission'
   New-Item -ItemType Directory -Force -Path $privateDir | Out-Null
   $credential = Get-Credential -Message 'Client ID jako nazwa użytkownika; sekret jako hasło'
   $credential | Export-Clixml -LiteralPath (Join-Path $privateDir 'credential.clixml')
   ```

   Nie wklejaj sekretu do czatu, argumentów polecenia ani plików SVN.
   `invoke-store-submission.ps1` pobiera krótko żyjący token do procesu
   potomnego i nie zapisuje go. Dla rotacji utwórz nowy sekret w Partner
   Center po zgodzie właściciela, zastąp lokalny `credential.clixml` nowym
   eksportem, sprawdź odczyt `status`, a potem unieważnij stary sekret.
   Kopie `state.json` i `credential.clixml` oraz odpowiedzi API pozostają
   wyłącznie poza repo; nie dodawaj ich do SVN ani publicznego lustra.

## Operacje

W przykładach `$python` wskazuje lokalny Python 3.10+; skrypt używa tylko
biblioteki standardowej. `$tool` wskazuje
`packaging/windows/store-submission/store_submission.py`; `$launcher` wskazuje
`invoke-store-submission.ps1` w tym samym katalogu. Dla r1882:

```powershell
$python = 'C:\sciezka\do\python.exe'
$tool = 'packaging/windows/store-submission/store_submission.py'
$launcher = 'packaging/windows/store-submission/invoke-store-submission.ps1'
$msix = 'dist/store-r1882/FileES-Desktop-0.1.18.1882-unsigned.msix'
$notes = 'dist/store-r1882/whats-new-pl-en.txt'
$hash = '320d55a19780d2e5f080d5589a67e8c5e88a7d42e40fa88ac0e5996638f2d2d3'

# Bez konfiguracji konta: tylko walidacja lokalna.
& $python $tool preview --msix $msix --sha256 $hash --notes $notes

# Po konfiguracji konta: odczyt bez zapisów Microsoft.
& $launcher -Python $python status
& $launcher -Python $python preview --msix $msix --sha256 $hash --notes $notes

# Dopiero po odrębnej zgodzie na ten konkretny szkic i upload:
& $launcher -Python $python prepare --msix $msix --sha256 $hash --notes $notes --approve-create-upload

# Po przeglądzie podsumowania, osobnej zgodzie i podaniu dokładnych wartości:
& $launcher -Python $python submit --submission-id '<ID>' --version '0.1.1882.0' --summary-sha256 '<SHA-256 podsumowania>' --approve-submit
```

`status` można powtarzać ręcznie. Narzędzie nie tworzy automatycznego monitora.
Nie odczytuje kluczy podpisujących FileES i nie modyfikuje kanałów wydań.
Do następnego kandydata zachowaj poprzedni plik stanu jako prywatne archiwum,
po potwierdzeniu zakończenia zgłoszenia; nie nadpisuj stanu aktywnego szkicu.

## Bramki pierwszego odbioru

Potrzebny jest zatwierdzony dostęp API. Najpierw wykonać tylko `status` i
`preview`: porównać ID produktu, identity, wersję opublikowaną, ewentualny
szkic, listingi PL/EN i tryb publikacji. Sprawdzić w Partner Center, czy
r1882 nie wysłano ręcznie. Dopiero potem właściciel podejmie decyzję o
`prepare`; po uploadzie należy obejrzeć szkic i różnice bez edycji portalu.
Następna osobna decyzja dotyczy `submit` dla dokładnego ID i podsumowania.
Po certyfikacji właściciel publikuje ręcznie i wykonuje odbiór aktualizacji.

Oficjalna dokumentacja: [API procesu](https://learn.microsoft.com/en-us/windows/uwp/monetize/manage-app-submissions),
[warunki i uwierzytelnienie](https://learn.microsoft.com/en-us/windows/uwp/monetize/create-and-manage-submissions-using-windows-store-services),
[zasób produktu](https://learn.microsoft.com/en-us/windows/uwp/monetize/get-an-app),
[ścieżka ustawień i typ konta](https://learn.microsoft.com/en-us/partner-center/account-settings/manage-account),
[powiązanie dzierżawy](https://learn.microsoft.com/en-us/partner-center/account-settings/manage-tenants),
[zarządzanie aplikacjami Entra](https://learn.microsoft.com/en-us/windows/apps/publish/partner-center/manage-azure-ad-applications-in-partner-center),
[uprawnienia właściciela](https://learn.microsoft.com/en-us/windows/apps/publish/partner-center/assign-roles-to-account-users),
[logowanie MSA do Entra](https://learn.microsoft.com/en-us/troubleshoot/entra/entra-id/app-integration/error-code-aadsts50020-user-account-identity-provider-does-not-exist),
[cennik Entra ID Free](https://www.microsoft.com/pl-pl/security/microsoft-entra-pricing),
[ostrzeżenie o `msstore publish`](https://learn.microsoft.com/en-us/windows/apps/publish/msstore-dev-cli/commands).

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"filees/internal/gui/actions"
	guiapp "filees/internal/gui/app"
	"filees/internal/gui/platform"
	"filees/internal/gui/tray"
	contract "filees/pkg/contract/v1"
	"filees/pkg/localpin"
	"filees/pkg/realmbranding"
	"filees/public-shares/gate"
	"github.com/google/uuid"
	qrcode "github.com/skip2/go-qrcode"
)

type actionRunner interface {
	Run(context.Context)
}

// configureActions deliberately wires only the actions exposed by the first
// Wails UX slice.  The controller remains the authority on eligibility; the
// WebView projection merely avoids offering an obviously unavailable button.
func configureActions(service *GUIService, locker actions.LockUnlocker, reservations actions.ReservationManager, stack actions.StackLifecycle, updater actions.Updater, activator actions.Activator, pinStore *localpin.Store, mobilePairer actions.MobilePairingLauncher, shouts actions.ShoutPublisher, notices actions.NoticeAcker, realmAliases actions.RealmAliasManager, realmGrants actions.RealmGrantManager, realmGrantBrowser platform.RealmGrantBrowser, realmBranding actions.RealmBrandingManager, settings platform.SettingsBrowser, sessionTimeouts actions.SessionTimeoutManager, publicShareBrowser platform.PublicShareBrowser, publicShares actions.PublicShareManager, uploadChannelBrowser platform.UploadChannelBrowser, uploadChannels actions.UploadChannelManager, repositoryCreator actions.RepositoryCreator, repositoryAttacher actions.RepositoryAttacher, repositoryLocator actions.RepositoryLocator, repositoryDetacher actions.RepositoryDetacher, repositoryDumpLoader actions.RepositoryDumpLoader, serverDetacher actions.ServerDetacher, realmRemover actions.RealmRemover, recoveryDownloader actions.RecoveryDownloader, consentPrompter platform.ConsentPrompter, backend platform.Backend, filePicker platform.FilePicker, folderPicker platform.FolderPicker, prompter platform.Prompter, restart, shutdown func()) actionRunner {
	if backend == nil {
		return nil
	}
	if folderPicker == nil {
		folderPicker = backend
	}
	if filePicker == nil {
		filePicker = backend
	}
	intents := make(chan tray.Intent, 32)
	service.attachActions(intents)
	return actions.New(actions.Config{
		Intents:              intents,
		ViewModel:            service.viewModel,
		Opener:               backend,
		Picker:               filePicker,
		FolderPicker:         folderPicker,
		Prompter:             prompter,
		Notifier:             actionNotifier{service: service},
		Updater:              updater,
		Activator:            activator,
		PinStore:             pinStore,
		MobilePairer:         mobilePairer,
		Locker:               locker,
		Reservations:         reservations,
		Shouts:               shouts,
		Notices:              notices,
		RealmAliases:         realmAliases,
		RealmGrants:          realmGrants,
		RealmGrantBrowser:    realmGrantBrowser,
		RealmBranding:        realmBranding,
		SettingsBrowser:      settings,
		SessionTimeouts:      sessionTimeouts,
		PublicShareBrowser:   publicShareBrowser,
		PublicShares:         publicShares,
		UploadChannelBrowser: uploadChannelBrowser,
		UploadChannels:       uploadChannels,
		RepositoryAttacher:   repositoryAttacher,
		RepositoryCreator:    repositoryCreator,
		RepositoryLocator:    repositoryLocator,
		RepositoryDetacher:   repositoryDetacher,
		RepositoryDumpLoader: repositoryDumpLoader,
		ServerDetacher:       serverDetacher,
		RealmRemover:         realmRemover,
		RecoveryDownloader:   recoveryDownloader,
		ConsentPrompter:      consentPrompter,
		ActionLifecycle:      service.runner,
		Stack:                stack,
		Reconnect:            service.runner.Reconnect,
		Refresh:              service.runner.Refresh,
		Restart:              restart,
		Shutdown:             shutdown,
	})
}

// consentPromptAdapter keeps the irreversible realm-removal consent inside
// the Wails prompt cascade. The required retention acknowledgement and the
// optional erasure request are deliberately separate decisions.
type consentPromptAdapter struct{ prompter platform.Prompter }

func (adapter consentPromptAdapter) ConfirmConsent(ctx context.Context, request platform.ConsentRequest) (platform.ConsentResult, error) {
	if adapter.prompter == nil {
		return platform.ConsentResult{Cancelled: true}, nil
	}
	required, err := adapter.prompter.Confirm(ctx, platform.ConfirmRequest{
		Title: request.Title, Text: request.Text + "\n\n" + request.RequiredText,
		ConfirmText: "Rozumiem retencję", CancelText: "Anuluj",
	})
	if err != nil || !required {
		return platform.ConsentResult{Cancelled: true}, err
	}
	optional, err := adapter.prompter.Confirm(ctx, platform.ConfirmRequest{
		Title: "Dodatkowe żądanie usunięcia", Text: request.OptionalText,
		ConfirmText: "Składam żądanie", CancelText: "Bez dodatkowego żądania",
	})
	if err != nil {
		return platform.ConsentResult{Cancelled: true}, err
	}
	return platform.ConsentResult{Required: true, Optional: optional}, nil
}

type updateClient interface {
	UpdatePlan(context.Context) (*contract.UpdatePlanResult, error)
	UpdateApply(context.Context) (*contract.UpdateApplyResult, error)
}

type mobilePairingClient interface {
	MobilePairingBegin(context.Context, string) (*contract.MobilePairingBeginResult, error)
}

const mobilePairingQRSize = 240

type mobilePairingAdapter struct {
	client    mobilePairingClient
	pinStore  *localpin.Store
	prompter  platform.Prompter
	presenter pairingPresenter
	servers   func() []PromptOption
}

func (adapter mobilePairingAdapter) Launch(ctx context.Context, serverID string) error {
	if adapter.client == nil || adapter.pinStore == nil || adapter.prompter == nil || adapter.presenter == nil {
		return errors.New("natywne parowanie mobilne nie jest dostępne")
	}
	authorized, err := adapter.authorize(ctx)
	if err != nil || !authorized {
		return err
	}
	serverID, selected, err := adapter.selectServer(ctx, serverID)
	if err != nil || !selected {
		return err
	}
	result, err := adapter.client.MobilePairingBegin(ctx, serverID)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty mobile pairing result")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, result.ExpiresAt)
	if err != nil {
		return fmt.Errorf("nieprawidłowy termin ważności kodu parowania: %w", err)
	}
	payload, err := mobilePairingPayloadJSON(result)
	if err != nil {
		return err
	}
	defer clear(payload)
	png, err := qrcode.Encode(string(payload), qrcode.Medium, mobilePairingQRSize)
	if err != nil {
		return fmt.Errorf("wygeneruj kod QR parowania: %w", err)
	}
	defer clear(png)
	return adapter.presenter.Present(ctx, pairingPresentation{
		Address: result.Address, ExpiresAt: expiresAt,
		QRDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
	})
}

type pairingServerSelector interface {
	SelectOne(context.Context, PromptSelectRequest) (PromptSelectResult, error)
}

func (adapter mobilePairingAdapter) selectServer(ctx context.Context, defaultServerID string) (string, bool, error) {
	selector, ok := adapter.prompter.(pairingServerSelector)
	if !ok {
		return "", false, errors.New("wybór serwera parowania nie jest dostępny")
	}
	var options []PromptOption
	if adapter.servers != nil {
		options = adapter.servers()
	}
	if len(cleanPromptOptions(options)) == 0 && strings.TrimSpace(defaultServerID) != "" {
		options = []PromptOption{{Value: defaultServerID, Label: defaultServerID}}
	}
	choice, err := selector.SelectOne(ctx, PromptSelectRequest{
		Title: "Sparuj urządzenie mobilne",
		Text:  "Wybierz serwer, dla którego ma zostać utworzony tymczasowy kod QR:",
		Label: "Serwer", Default: defaultServerID, Options: options,
	})
	if err != nil || choice.Cancelled {
		return "", false, err
	}
	serverID := strings.TrimSpace(choice.Value)
	if !promptOptionExists(cleanPromptOptions(options), serverID) {
		return "", false, errors.New("wybrany serwer parowania nie jest już dostępny")
	}
	return serverID, true, nil
}

func pairingServerOptions(snapshot Snapshot) []PromptOption {
	options := make([]PromptOption, 0, len(snapshot.Servers))
	for _, server := range snapshot.Servers {
		serverID := strings.TrimSpace(server.ID)
		if serverID == "" {
			continue
		}
		label := strings.TrimSpace(server.DisplayName)
		if label == "" {
			label = strings.TrimSpace(server.Address)
		}
		if label == "" {
			label = serverID
		}
		options = append(options, PromptOption{Value: serverID, Label: label, Detail: strings.TrimSpace(server.Address)})
	}
	return cleanPromptOptions(options)
}

func (adapter mobilePairingAdapter) authorize(ctx context.Context) (bool, error) {
	configured, err := adapter.pinStore.IsConfigured()
	if err != nil {
		return false, fmt.Errorf("odczytaj lokalny PIN: %w", err)
	}
	if !configured {
		prompted, promptErr := adapter.prompter.PromptText(ctx, platform.PromptTextRequest{
			Title: "Zabezpiecz parowanie", Text: "Ustaw lokalny PIN chroniący wyświetlanie kodów parowania:", Label: "PIN", Secret: true,
		})
		if promptErr != nil || prompted.Cancelled {
			return false, promptErr
		}
		pin := []byte(prompted.Value)
		defer clear(pin)
		if len(pin) == 0 {
			return false, errors.New("PIN nie może być pusty")
		}
		if err := adapter.pinStore.Setup(pin); err != nil {
			return false, fmt.Errorf("zapisz lokalny PIN: %w", err)
		}
		return true, nil
	}

	text := "Podaj lokalny PIN, aby wyświetlić kod parowania:"
	for {
		prompted, promptErr := adapter.prompter.PromptText(ctx, platform.PromptTextRequest{
			Title: "Sparuj urządzenie mobilne", Text: text, Label: "PIN", Secret: true,
		})
		if promptErr != nil || prompted.Cancelled {
			return false, promptErr
		}
		pin := []byte(prompted.Value)
		ok, locked, verifyErr := adapter.pinStore.Verify(pin)
		clear(pin)
		if verifyErr != nil {
			return false, fmt.Errorf("zweryfikuj lokalny PIN: %w", verifyErr)
		}
		if locked {
			return false, errors.New("PIN został zablokowany po zbyt wielu błędnych próbach")
		}
		if ok {
			return true, nil
		}
		text = "Nieprawidłowy PIN. Spróbuj ponownie:"
	}
}

func mobilePairingPayloadJSON(result *contract.MobilePairingBeginResult) ([]byte, error) {
	if result == nil || strings.TrimSpace(result.Address) == "" || strings.TrimSpace(result.HostPublicKey) == "" || strings.TrimSpace(result.Token) == "" {
		return nil, errors.New("daemon returned an incomplete mobile pairing result")
	}
	return json.Marshal(struct {
		Address       string `json:"address"`
		HostPublicKey string `json:"host_public_key"`
		Token         string `json:"token"`
	}{Address: result.Address, HostPublicKey: result.HostPublicKey, Token: result.Token})
}

type updateAdapter struct{ client updateClient }

func (adapter updateAdapter) UpdatePlan(ctx context.Context) (*actions.UpdatePlan, error) {
	result, err := adapter.client.UpdatePlan(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty update plan")
	}
	plan := &actions.UpdatePlan{
		CurrentVersion: result.CurrentVersion, AvailableVersion: result.AvailableVersion,
		ReleaseID: result.ReleaseID, RestartRequired: result.RestartRequired,
		Changes: make([]actions.UpdateChange, len(result.Changes)),
	}
	for index, change := range result.Changes {
		plan.Changes[index] = actions.UpdateChange{Action: change.Action, Path: change.Path, Detail: change.Detail}
	}
	return plan, nil
}

func (adapter updateAdapter) UpdateApply(ctx context.Context) (*actions.UpdateResult, error) {
	result, err := adapter.client.UpdateApply(ctx)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty update result")
	}
	return &actions.UpdateResult{InstalledVersion: result.InstalledVersion, RestartRequired: result.RestartRequired}, nil
}

type realmGrantClient interface {
	RealmGrantRecipients(context.Context, string, string) (*contract.RealmGrantRecipientsResult, error)
	RealmSetVisibility(context.Context, string, string) (*contract.RealmSetVisibilityResult, error)
	RepoGrantAccess(context.Context, contract.RepoGrantAccessPayload) (*contract.RealmGrantResult, error)
	RepoRevokeAccess(context.Context, contract.RepoRevokeAccessPayload) (*contract.RealmGrantResult, error)
	RepoSetEditingPolicy(context.Context, contract.RepoSetEditingPolicyPayload) (*contract.RepoSetEditingPolicyResult, error)
}

type realmAliasClient interface {
	RealmAliasClaim(context.Context, string, string) (*contract.RealmAliasClaimResult, error)
}

type realmAliasAdapter struct{ client realmAliasClient }

func (adapter realmAliasAdapter) ClaimAlias(ctx context.Context, serverID, alias string) error {
	result, err := adapter.client.RealmAliasClaim(ctx, serverID, alias)
	if err != nil {
		return err
	}
	if result == nil || result.Alias == "" {
		return errors.New("daemon returned an empty realm alias")
	}
	return nil
}

type realmGrantAdapter struct{ client realmGrantClient }

type realmBrandingClient interface {
	RealmPublicBranding(context.Context, string) (*contract.RealmPublicBrandingResult, error)
	RealmSetPublicBranding(context.Context, string, realmbranding.Branding) (*contract.RealmPublicBrandingResult, error)
}

type realmBrandingAdapter struct{ client realmBrandingClient }

func (adapter realmBrandingAdapter) PublicBranding(ctx context.Context, serverID string) (realmbranding.Branding, error) {
	result, err := adapter.client.RealmPublicBranding(ctx, serverID)
	if err != nil {
		return realmbranding.Branding{}, err
	}
	if result == nil {
		return realmbranding.Branding{}, errors.New("daemon returned empty realm branding")
	}
	return realmbranding.Normalize(result.Branding)
}

func (adapter realmBrandingAdapter) SetPublicBranding(ctx context.Context, serverID string, branding realmbranding.Branding) (realmbranding.Branding, error) {
	result, err := adapter.client.RealmSetPublicBranding(ctx, serverID, branding)
	if err != nil {
		return realmbranding.Branding{}, err
	}
	if result == nil {
		return realmbranding.Branding{}, errors.New("daemon returned empty realm branding")
	}
	return realmbranding.Normalize(result.Branding)
}

func (adapter realmGrantAdapter) ListRecipients(ctx context.Context, serverID, repoID string) ([]actions.RealmGrantRecipient, error) {
	result, err := adapter.client.RealmGrantRecipients(ctx, serverID, repoID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty realm directory")
	}
	recipients := make([]actions.RealmGrantRecipient, 0, len(result.Recipients))
	for _, recipient := range result.Recipients {
		recipients = append(recipients, actions.RealmGrantRecipient{RealmID: recipient.RealmID, Alias: recipient.Alias, Access: recipient.Access, State: recipient.State})
	}
	return recipients, nil
}

func (adapter realmGrantAdapter) SetVisibility(ctx context.Context, serverID, visibility string) error {
	result, err := adapter.client.RealmSetVisibility(ctx, serverID, visibility)
	if err != nil {
		return err
	}
	if result == nil || result.Visibility != visibility {
		return errors.New("daemon returned an invalid realm visibility result")
	}
	return nil
}

func (adapter realmGrantAdapter) Grant(ctx context.Context, serverID, repoID, recipientRealmID, access string) error {
	result, err := adapter.client.RepoGrantAccess(ctx, contract.RepoGrantAccessPayload{ServerID: serverID, RepoID: repoID, RecipientRealmID: recipientRealmID, Access: access})
	if err != nil {
		return err
	}
	if result == nil || result.RepoID != repoID || result.RecipientRealmID != recipientRealmID || result.Access != access || result.State != "active" {
		return errors.New("daemon returned an invalid realm grant result")
	}
	return nil
}

func (adapter realmGrantAdapter) Revoke(ctx context.Context, serverID, repoID, recipientRealmID string) error {
	result, err := adapter.client.RepoRevokeAccess(ctx, contract.RepoRevokeAccessPayload{ServerID: serverID, RepoID: repoID, RecipientRealmID: recipientRealmID})
	if err != nil {
		return err
	}
	if result == nil || result.RepoID != repoID || result.RecipientRealmID != recipientRealmID || result.State != "revoked" {
		return errors.New("daemon returned an invalid realm grant revoke result")
	}
	return nil
}

func (adapter realmGrantAdapter) SetEditingPolicy(ctx context.Context, serverID, repoID string, lockRequired bool) (bool, error) {
	policy := contract.EditingFree
	if lockRequired {
		policy = contract.EditingLockRequired
	}
	result, err := adapter.client.RepoSetEditingPolicy(ctx, contract.RepoSetEditingPolicyPayload{ServerID: serverID, RepoID: repoID, Policy: policy})
	if err != nil {
		return false, err
	}
	if result == nil || result.RepoID != repoID {
		return false, errors.New("daemon returned an invalid editing policy result")
	}
	return result.Policy == contract.EditingLockRequired, nil
}

type shoutClient interface {
	RepoPublish(context.Context, string, string) (*contract.RepoPublishResult, error)
	NoticeAck(context.Context, string) error
}

type shoutAdapter struct{ client shoutClient }

func (adapter shoutAdapter) Publish(ctx context.Context, repoID, comment string) (int64, error) {
	if adapter.client == nil {
		return 0, errors.New("shout publish is unavailable")
	}
	result, err := adapter.client.RepoPublish(ctx, repoID, comment)
	if err != nil {
		return 0, err
	}
	if result == nil {
		return 0, errors.New("daemon returned an empty publish result")
	}
	return result.Revision, nil
}

func (adapter shoutAdapter) AckNotice(ctx context.Context, noticeID string) error {
	if adapter.client == nil {
		return errors.New("shout ack is unavailable")
	}
	return adapter.client.NoticeAck(ctx, noticeID)
}

type recoveryDownloadClient interface {
	RecoveryDownload(context.Context, contract.RecoveryDownloadPayload) (*contract.RecoveryDownloadResult, error)
}

type recoveryDownloadAdapter struct{ client recoveryDownloadClient }

func (adapter recoveryDownloadAdapter) DownloadRecovery(ctx context.Context, operationID, outputRoot string) ([]string, error) {
	result, err := adapter.client.RecoveryDownload(ctx, contract.RecoveryDownloadPayload{OperationID: operationID, OutputRoot: outputRoot})
	if err != nil {
		return nil, err
	}
	if result == nil || result.OperationID != operationID {
		return nil, errors.New("daemon returned an invalid recovery download result")
	}
	return result.Paths, nil
}

type sessionTimeoutClient interface {
	ServerSetSessionTimeout(context.Context, contract.ServerSetSessionTimeoutPayload) (*contract.ServerSetSessionTimeoutResult, error)
}

type sessionTimeoutAdapter struct{ client sessionTimeoutClient }

func (adapter sessionTimeoutAdapter) SetSessionTimeout(ctx context.Context, serverID string, minutes int) (int, error) {
	result, err := adapter.client.ServerSetSessionTimeout(ctx, contract.ServerSetSessionTimeoutPayload{ServerID: serverID, Minutes: minutes})
	if err != nil {
		return 0, err
	}
	if result == nil || result.Minutes != minutes {
		return 0, errors.New("daemon returned an invalid session timeout")
	}
	return result.Minutes, nil
}

type publicShareClient interface {
	PublicShareList(context.Context, string, string) (*contract.PublicShareListResult, error)
	PublicShareCreate(context.Context, contract.PublicShareCreatePayload) (*contract.PublicShareResult, error)
	PublicShareUpdate(context.Context, contract.PublicShareUpdatePayload) (*contract.PublicShareResult, error)
	PublicShareRevoke(context.Context, contract.PublicShareChannelPayload) (*contract.PublicShareResult, error)
	PublicShareDelete(context.Context, contract.PublicShareChannelPayload) (*contract.PublicShareResult, error)
}

type publicShareAdapter struct{ client publicShareClient }

func (adapter publicShareAdapter) ListPublicShares(ctx context.Context, serverID, repoID string) ([]actions.PublicShareSummary, error) {
	result, err := adapter.client.PublicShareList(ctx, serverID, repoID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty public share list")
	}
	shares := make([]actions.PublicShareSummary, 0, len(result.Shares))
	for _, share := range result.Shares {
		row := actions.PublicShareSummary{ChannelID: share.ChannelID, Alias: share.Alias, Slug: share.Slug, State: share.State, SourceRoot: share.SourceRoot, UpdatedAt: share.UpdatedAt, Recipients: append([]string(nil), share.Recipients...), PasswordProtected: share.PasswordProtected, DoNotFollow: share.DoNotFollow}
		for _, object := range share.Objects {
			row.Objects = append(row.Objects, actions.PublicShareObject{PublicID: object.PublicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
		}
		shares = append(shares, row)
	}
	return shares, nil
}

func (adapter publicShareAdapter) CreatePublicShare(ctx context.Context, serverID string, declaration actions.PublicShareDeclaration) error {
	remote, err := publicShareDeclarationToContract(declaration)
	if err != nil {
		return err
	}
	result, err := adapter.client.PublicShareCreate(ctx, contract.PublicShareCreatePayload{ServerID: serverID, PublicShareDeclaration: remote})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID == "" || result.State != "active" {
		return errors.New("daemon returned an invalid public share result")
	}
	return nil
}

func (adapter publicShareAdapter) UpdatePublicShare(ctx context.Context, serverID, channelID string, declaration actions.PublicShareDeclaration) error {
	remote, err := publicShareDeclarationToContract(declaration)
	if err != nil {
		return err
	}
	result, err := adapter.client.PublicShareUpdate(ctx, contract.PublicShareUpdatePayload{ServerID: serverID, ChannelID: channelID, KeepPassword: declaration.KeepPassword, PublicShareDeclaration: remote})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "active" {
		return errors.New("daemon returned an invalid public share update result")
	}
	return nil
}

func (adapter publicShareAdapter) RevokePublicShare(ctx context.Context, serverID, repoID, channelID string) error {
	result, err := adapter.client.PublicShareRevoke(ctx, contract.PublicShareChannelPayload{ServerID: serverID, RepoID: repoID, ChannelID: channelID})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "revoked" {
		return errors.New("daemon returned an invalid public share revoke result")
	}
	return nil
}

func (adapter publicShareAdapter) DeletePublicShare(ctx context.Context, serverID, repoID, channelID string) error {
	result, err := adapter.client.PublicShareDelete(ctx, contract.PublicShareChannelPayload{ServerID: serverID, RepoID: repoID, ChannelID: channelID})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "deleted" {
		return errors.New("daemon returned an invalid public share delete result")
	}
	return nil
}

type uploadChannelClient interface {
	UploadChannelList(context.Context, string, string) (*contract.UploadChannelListResult, error)
	UploadChannelCreate(context.Context, contract.UploadChannelCreatePayload) (*contract.UploadChannelResult, error)
	UploadChannelUpdate(context.Context, contract.UploadChannelUpdatePayload) (*contract.UploadChannelResult, error)
	UploadChannelRevoke(context.Context, contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error)
	UploadChannelDelete(context.Context, contract.UploadChannelChannelPayload) (*contract.UploadChannelResult, error)
}

type uploadChannelAdapter struct{ client uploadChannelClient }

func (adapter uploadChannelAdapter) ListUploadChannels(ctx context.Context, serverID, repoID string) ([]actions.UploadChannelSummary, error) {
	result, err := adapter.client.UploadChannelList(ctx, serverID, repoID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty upload channel list")
	}
	channels := make([]actions.UploadChannelSummary, 0, len(result.Channels))
	for _, channel := range result.Channels {
		channels = append(channels, actions.UploadChannelSummary{
			ChannelID: channel.ChannelID, Alias: channel.Alias, Slug: channel.Slug, State: channel.State,
			UploadRepoID: channel.UploadRepoID, UpdatedAt: channel.UpdatedAt, Recipients: append([]string(nil), channel.Recipients...),
		})
	}
	return channels, nil
}

func (adapter uploadChannelAdapter) CreateUploadChannel(ctx context.Context, serverID string, declaration actions.UploadChannelDeclaration) error {
	result, err := adapter.client.UploadChannelCreate(ctx, contract.UploadChannelCreatePayload{ServerID: serverID, UploadChannelDeclaration: uploadChannelDeclarationToContract(declaration)})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID == "" || result.State != "active" {
		return errors.New("daemon returned an invalid upload channel result")
	}
	return nil
}

func (adapter uploadChannelAdapter) UpdateUploadChannel(ctx context.Context, serverID, channelID string, declaration actions.UploadChannelDeclaration) error {
	result, err := adapter.client.UploadChannelUpdate(ctx, contract.UploadChannelUpdatePayload{ServerID: serverID, ChannelID: channelID, UploadChannelDeclaration: uploadChannelDeclarationToContract(declaration)})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "active" {
		return errors.New("daemon returned an invalid upload channel update result")
	}
	return nil
}

func (adapter uploadChannelAdapter) RevokeUploadChannel(ctx context.Context, serverID, repoID, channelID string) error {
	result, err := adapter.client.UploadChannelRevoke(ctx, contract.UploadChannelChannelPayload{ServerID: serverID, RepoID: repoID, ChannelID: channelID})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "revoked" {
		return errors.New("daemon returned an invalid upload channel revoke result")
	}
	return nil
}

func (adapter uploadChannelAdapter) DeleteUploadChannel(ctx context.Context, serverID, repoID, channelID string) error {
	result, err := adapter.client.UploadChannelDelete(ctx, contract.UploadChannelChannelPayload{ServerID: serverID, RepoID: repoID, ChannelID: channelID})
	if err != nil {
		return err
	}
	if result == nil || result.ChannelID != channelID || result.State != "deleted" {
		return errors.New("daemon returned an invalid upload channel delete result")
	}
	return nil
}

func uploadChannelDeclarationToContract(declaration actions.UploadChannelDeclaration) contract.UploadChannelDeclaration {
	return contract.UploadChannelDeclaration{AuthorityRepoID: declaration.AuthorityRepoID, Slug: declaration.Slug, Kind: declaration.Kind, Recipients: append([]string(nil), declaration.Recipients...)}
}

func publicShareDeclarationToContract(declaration actions.PublicShareDeclaration) (contract.PublicShareDeclaration, error) {
	passwordHash := ""
	if len(declaration.Password) > 0 {
		var err error
		passwordHash, err = gate.HashPassword(string(declaration.Password), nil)
		if err != nil {
			return contract.PublicShareDeclaration{}, err
		}
	}
	objects := make([]contract.PublicShareObject, 0, len(declaration.Objects))
	for _, object := range declaration.Objects {
		publicID := object.PublicID
		if publicID == "" {
			publicID = strings.ReplaceAll(uuid.NewString(), "-", "")
		}
		objects = append(objects, contract.PublicShareObject{PublicID: publicID, RepoPath: object.RepoPath, DisplayName: object.DisplayName, Size: object.Size})
	}
	return contract.PublicShareDeclaration{RepoID: declaration.RepoID, SourceRoot: declaration.SourceRoot, Slug: declaration.Slug, Recipients: append([]string(nil), declaration.Recipients...), PasswordHash: passwordHash, DoNotFollow: declaration.DoNotFollow, Objects: objects}, nil
}

type repositoryDetachClient interface {
	RepoDetach(context.Context, string, string) (*contract.RepoLifecycleResult, error)
	RepoDelete(context.Context, string, string) (*contract.RepoLifecycleResult, error)
}

type repositoryCreateClient interface {
	RepoCreateRequest(context.Context, contract.RepoCreateRequestPayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryCreateAdapter struct{ client repositoryCreateClient }

func (adapter repositoryCreateAdapter) CreateRepository(ctx context.Context, serverID, displayName, localPath string) (string, error) {
	result, err := adapter.client.RepoCreateRequest(ctx, contract.RepoCreateRequestPayload{ServerID: serverID, DisplayName: displayName, LocalPath: localPath})
	if err != nil {
		return "", err
	}
	if result == nil || result.OperationID == "" {
		return "", errors.New("daemon returned an empty repository operation")
	}
	return result.OperationID, nil
}

func (adapter repositoryCreateAdapter) CreationStatus(ctx context.Context, operationID string) (state, lastError string, err error) {
	result, err := adapter.client.RepoLifecycleStatus(ctx, operationID)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("daemon returned an empty repository operation")
	}
	return result.State, result.LastError, nil
}

type repositoryLocateClient interface {
	RepoLocate(context.Context, contract.RepoLocatePayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryLocateAdapter struct{ client repositoryLocateClient }

func (adapter repositoryLocateAdapter) LocateRepository(ctx context.Context, serverID, repoID, existingLocalPath string) (string, error) {
	result, err := adapter.client.RepoLocate(ctx, contract.RepoLocatePayload{ServerID: serverID, RepoID: repoID, ExistingLocalPath: existingLocalPath})
	if err != nil {
		return "", err
	}
	if result == nil || result.OperationID == "" {
		return "", errors.New("daemon returned an empty repository locate operation")
	}
	return result.OperationID, nil
}

func (adapter repositoryLocateAdapter) LocateStatus(ctx context.Context, operationID string) (string, string, error) {
	result, err := adapter.client.RepoLifecycleStatus(ctx, operationID)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("daemon returned an empty repository operation")
	}
	return result.State, result.LastError, nil
}

type repositoryDumpLoadClient interface {
	RepoLoadDump(context.Context, string, string, bool, *int) (*contract.RepoLifecycleResult, error)
}

type repositoryDumpLoadAdapter struct{ client repositoryDumpLoadClient }

func (adapter repositoryDumpLoadAdapter) LoadDump(ctx context.Context, serverID, repoID string) error {
	operationCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	result, err := adapter.client.RepoLoadDump(operationCtx, serverID, repoID, true, nil)
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty repository dump-load result")
	}
	return nil
}

type serverDetachClient interface {
	ServerDetach(context.Context, string) (*contract.ServerDetachResult, error)
}

type serverDetachAdapter struct{ client serverDetachClient }

func (adapter serverDetachAdapter) DetachServer(ctx context.Context, serverID string) error {
	result, err := adapter.client.ServerDetach(ctx, serverID)
	if err != nil {
		return err
	}
	if result == nil || result.ServerID != serverID {
		return errors.New("daemon did not complete server detach")
	}
	return nil
}

type realmRemovalClient interface {
	RealmRemoveBegin(context.Context, contract.RealmRemoveBeginPayload) (*contract.RealmRemoveBeginResult, error)
	RealmRemoveConfirm(context.Context, contract.RealmRemoveConfirmPayload) (*contract.RealmRemoveConfirmResult, error)
}

type realmRemovalAdapter struct{ client realmRemovalClient }

func (adapter realmRemovalAdapter) BeginRealmRemoval(ctx context.Context, request actions.RealmRemovalBeginRequest) (actions.RealmRemovalBeginResult, error) {
	result, err := adapter.client.RealmRemoveBegin(ctx, contract.RealmRemoveBeginPayload{
		ServerID: request.ServerID, NotificationEmail: request.NotificationEmail,
		RecoveryDirectory: request.RecoveryDirectory, ErasureRequested: request.ErasureRequested,
	})
	if err != nil {
		return actions.RealmRemovalBeginResult{}, err
	}
	if result == nil {
		return actions.RealmRemovalBeginResult{}, errors.New("daemon returned an empty realm removal request")
	}
	return actions.RealmRemovalBeginResult{
		OperationID: result.OperationID, RecoveryKitPath: result.RecoveryKitPath, ExpiresAt: result.ExpiresAt,
		ActiveClientCount: result.ActiveClientCount, OwnedRepositoryCount: result.OwnedRepositoryCount, ForeignGrantCount: result.ForeignGrantCount,
	}, nil
}

func (adapter realmRemovalAdapter) ConfirmRealmRemoval(ctx context.Context, serverID, operationID string, otp []byte, kitPath string) (actions.RealmRemovalConfirmResult, error) {
	result, err := adapter.client.RealmRemoveConfirm(ctx, contract.RealmRemoveConfirmPayload{
		ServerID: serverID, OperationID: operationID, RecoveryKitPath: kitPath, OTP: contract.Secret(otp),
	})
	if err != nil {
		return actions.RealmRemovalConfirmResult{}, err
	}
	if result == nil {
		return actions.RealmRemovalConfirmResult{}, errors.New("daemon returned an empty realm removal confirmation")
	}
	return actions.RealmRemovalConfirmResult{
		RecoveryKitPath: result.RecoveryKitPath, ArchiveCount: result.ArchiveCount,
		DownloadUntil: result.DownloadUntil, AdminGraceUntil: result.AdminGraceUntil,
		ErasureRequested: result.ErasureRequested, ErasureMaxDays: result.ErasureMaxDays,
	}, nil
}

type repositoryAttachClient interface {
	RepoAttachIntent(context.Context, contract.RepoAttachIntentPayload) (*contract.RepoLifecycleResult, error)
	RepoAttachApprove(context.Context, contract.RepoAttachApprovePayload) (*contract.RepoLifecycleResult, error)
	RepoLifecycleStatus(context.Context, string) (*contract.RepoLifecycleResult, error)
}

type repositoryAttachAdapter struct{ client repositoryAttachClient }

func (adapter repositoryAttachAdapter) AttachRepository(ctx context.Context, serverID, repoID, localPath string) (string, error) {
	intent, err := adapter.client.RepoAttachIntent(ctx, contract.RepoAttachIntentPayload{ServerID: serverID, RepoID: repoID, LocalPath: localPath})
	if err != nil {
		return "", err
	}
	if intent == nil || intent.OperationID == "" {
		return "", errors.New("daemon returned an empty repository attachment intent")
	}
	approved, err := adapter.client.RepoAttachApprove(ctx, contract.RepoAttachApprovePayload{OperationID: intent.OperationID, ServerID: serverID, RepoID: repoID})
	if err != nil {
		return "", err
	}
	if approved == nil || approved.OperationID != intent.OperationID {
		return "", errors.New("daemon returned an invalid repository attachment approval")
	}
	return approved.OperationID, nil
}

func (adapter repositoryAttachAdapter) AttachmentStatus(ctx context.Context, operationID string) (state, lastError string, err error) {
	result, err := adapter.client.RepoLifecycleStatus(ctx, operationID)
	if err != nil {
		return "", "", err
	}
	if result == nil {
		return "", "", errors.New("daemon returned an empty repository attachment operation")
	}
	return result.State, result.LastError, nil
}

type repositoryDetachAdapter struct{ client repositoryDetachClient }

func (adapter repositoryDetachAdapter) DetachRepository(ctx context.Context, serverID, repoID string, deleteRepository bool) error {
	operationCtx, cancel := context.WithTimeout(ctx, 45*time.Minute)
	defer cancel()
	var (
		result *contract.RepoLifecycleResult
		err    error
	)
	if deleteRepository {
		result, err = adapter.client.RepoDelete(operationCtx, serverID, repoID)
	} else {
		result, err = adapter.client.RepoDetach(operationCtx, serverID, repoID)
	}
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty repository detach result")
	}
	if deleteRepository {
		if result.ServerDeleteCompleted || result.State == "deleted" {
			return nil
		}
		if result.LastError != "" {
			return errors.New(result.LastError)
		}
		return errors.New("daemon did not confirm server repository deletion")
	}
	if result.State != "detached" {
		if result.LastError != "" {
			return errors.New(result.LastError)
		}
		return errors.New("daemon did not complete repository detach")
	}
	return nil
}

type systemLifecycleClient interface {
	SystemRestart(context.Context) (*contract.SystemLifecycleResult, error)
	SystemShutdown(context.Context) (*contract.SystemLifecycleResult, error)
}

type stackLifecycleAdapter struct {
	client systemLifecycleClient
}

func (adapter stackLifecycleAdapter) RestartFileES(ctx context.Context) error {
	result, err := adapter.client.SystemRestart(ctx)
	return validateSystemLifecycleResult(result, "restart", err)
}

func (adapter stackLifecycleAdapter) ShutdownFileES(ctx context.Context) error {
	result, err := adapter.client.SystemShutdown(ctx)
	return validateSystemLifecycleResult(result, "shutdown", err)
}

func validateSystemLifecycleResult(result *contract.SystemLifecycleResult, expected string, err error) error {
	if err != nil {
		return err
	}
	if result == nil {
		return errors.New("daemon returned an empty lifecycle result")
	}
	if result.Action != expected {
		return errors.New("daemon returned an unexpected lifecycle action")
	}
	return nil
}

type reservationClient interface {
	RepoReservationList(context.Context, string) (*contract.RepoReservationListResult, error)
	RepoReservationRelease(context.Context, contract.RepoReservationReleasePayload) error
}

type reservationAdapter struct {
	client reservationClient
}

func (adapter reservationAdapter) ListReservations(ctx context.Context, serverID string) ([]guiapp.Reservation, error) {
	result, err := adapter.client.RepoReservationList(ctx, serverID)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("daemon returned an empty reservation list")
	}
	reservations := make([]guiapp.Reservation, len(result.Reservations))
	for i, reservation := range result.Reservations {
		reservations[i] = guiapp.Reservation{
			ServerID: serverID, RepoID: reservation.RepoID, WorkingCopy: reservation.WorkingCopy,
			Path: reservation.Path, Token: reservation.Token, OwnerLabel: reservation.OwnerLabel,
			CreatedAt: reservation.CreatedAt, CanRelease: reservation.CanRelease,
			LocalChanges: reservation.LocalChanges, ActivePassport: reservation.ActivePassport,
		}
	}
	return reservations, nil
}

func (adapter reservationAdapter) ReleaseReservation(ctx context.Context, request guiapp.ReservationReleaseRequest) error {
	return adapter.client.RepoReservationRelease(ctx, contract.RepoReservationReleasePayload{
		ServerID: request.ServerID, RepoID: request.RepoID, Path: request.Path,
		ExpectedToken: request.ExpectedToken, ConfirmRisk: request.ConfirmRisk,
	})
}

// actionNotifier keeps action feedback in the same Wails surface. It carries
// information only: no callback can turn a toast into a privileged action.
type actionNotifier struct {
	service *GUIService
}

func (notifier actionNotifier) Notify(_ context.Context, notification platform.Notification) error {
	notifier.service.emitActionFeedback(ActionFeedback{
		Level:   string(notification.Urgency),
		Title:   notification.Title,
		Message: notification.Body,
	})
	return nil
}

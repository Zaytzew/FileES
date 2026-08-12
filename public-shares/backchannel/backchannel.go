// Package backchannel carries the versioned Public Shares authority protocol
// over ordinary HTTP. In the shared topology HTTP travels over a Unix socket;
// in the split topology it travels through the server-established reverse SSH
// forwarding. The public side owns no credential in either case.
package backchannel

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"filees/public-shares/authority"
	"filees/public-shares/channel"
	"filees/public-shares/recipientotp"
)

const Protocol = "filees.public-share-backchannel/v1"

type Authority interface {
	Enter(context.Context, string, string) (authority.Entry, error)
	Inspect(string, string) (channel.Projection, error)
	Check(context.Context, authority.ObjectRequest) (authority.ObjectPermit, error)
	Fetch(context.Context, authority.ObjectRequest) (authority.FetchedLeaf, error)
	RequestRecipientOTP(context.Context, recipientotp.Request) error
	VerifyRecipientOTP(context.Context, recipientotp.VerifyRequest) (recipientotp.Grant, error)
}

type addressRequest struct {
	Protocol string `json:"protocol"`
	Alias    string `json:"alias"`
	Slug     string `json:"slug"`
}

type objectRequest struct {
	Protocol string                  `json:"protocol"`
	Object   authority.ObjectRequest `json:"object"`
}

type recipientRequest struct {
	Protocol string               `json:"protocol"`
	Request  recipientotp.Request `json:"request"`
}

type recipientVerifyRequest struct {
	Protocol string                     `json:"protocol"`
	Request  recipientotp.VerifyRequest `json:"request"`
}

type Server struct {
	Authority  Authority
	FetchSlots chan struct{}
}

func (s Server) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if request.Method != http.MethodPost || s.Authority == nil {
		notFound(w)
		return
	}
	switch request.URL.Path {
	case "/v1/entry":
		var input addressRequest
		if decode(request, &input) != nil || input.Protocol != Protocol {
			notFound(w)
			return
		}
		result, err := s.Authority.Enter(request.Context(), input.Alias, input.Slug)
		if err != nil {
			notFound(w)
			return
		}
		writeJSON(w, result)
	case "/v1/inspect":
		var input addressRequest
		if decode(request, &input) != nil || input.Protocol != Protocol {
			notFound(w)
			return
		}
		result, err := s.Authority.Inspect(input.Alias, input.Slug)
		if err != nil {
			notFound(w)
			return
		}
		writeJSON(w, result)
	case "/v1/check":
		var input objectRequest
		if decode(request, &input) != nil || input.Protocol != Protocol {
			notFound(w)
			return
		}
		result, err := s.Authority.Check(request.Context(), input.Object)
		if err != nil {
			notFound(w)
			return
		}
		writeJSON(w, result)
	case "/v1/recipient/request":
		var input recipientRequest
		if decode(request, &input) != nil || input.Protocol != Protocol || s.Authority.RequestRecipientOTP(request.Context(), input.Request) != nil {
			notFound(w)
			return
		}
		writeJSON(w, map[string]string{"status": "accepted"})
	case "/v1/recipient/verify":
		var input recipientVerifyRequest
		if decode(request, &input) != nil || input.Protocol != Protocol {
			notFound(w)
			return
		}
		result, err := s.Authority.VerifyRecipientOTP(request.Context(), input.Request)
		if err != nil {
			notFound(w)
			return
		}
		writeJSON(w, result)
	case "/v1/fetch":
		var input objectRequest
		if decode(request, &input) != nil || input.Protocol != Protocol {
			notFound(w)
			return
		}
		if s.FetchSlots != nil {
			select {
			case s.FetchSlots <- struct{}{}:
				defer func() { <-s.FetchSlots }()
			default:
				notFound(w)
				return
			}
		}
		leaf, err := s.Authority.Fetch(request.Context(), input.Object)
		if err != nil {
			notFound(w)
			return
		}
		defer leaf.Body.Close()
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(leaf.Size, 10))
		w.Header().Set("X-Filees-Cache-Key", leaf.CacheKey)
		w.Header().Set("X-Filees-MD5", leaf.MD5)
		w.Header().Set("X-Filees-Display-Name", base64.RawURLEncoding.EncodeToString([]byte(leaf.DisplayName)))
		w.Header().Set("X-Filees-Revision", strconv.FormatInt(leaf.Revision, 10))
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, leaf.Body)
	default:
		notFound(w)
	}
}

type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func (c Client) Enter(ctx context.Context, alias, slug string) (authority.Entry, error) {
	var result authority.Entry
	err := c.callJSON(ctx, "/v1/entry", addressRequest{Protocol: Protocol, Alias: alias, Slug: slug}, &result)
	if err == nil && (result.Projection.Validate() != nil || result.Projection.Alias != alias || result.Projection.Slug != slug || result.Revision < 1 || result.FrostProof == "") {
		err = errors.New("public share backchannel entry is invalid")
	}
	return result, err
}

func (c Client) Inspect(alias, slug string) (channel.Projection, error) {
	var result channel.Projection
	err := c.callJSON(context.Background(), "/v1/inspect", addressRequest{Protocol: Protocol, Alias: alias, Slug: slug}, &result)
	if err == nil && (result.Validate() != nil || result.Alias != alias || result.Slug != slug) {
		err = errors.New("public share backchannel projection is invalid")
	}
	return result, err
}

func (c Client) Check(ctx context.Context, object authority.ObjectRequest) (authority.ObjectPermit, error) {
	var result authority.ObjectPermit
	err := c.callJSON(ctx, "/v1/check", objectRequest{Protocol: Protocol, Object: object}, &result)
	if err == nil && !validPermit(result, object.Revision) {
		err = errors.New("public share backchannel permit is invalid")
	}
	return result, err
}

func (c Client) RequestRecipientOTP(ctx context.Context, request recipientotp.Request) error {
	var result struct {
		Status string `json:"status"`
	}
	err := c.callJSON(ctx, "/v1/recipient/request", recipientRequest{Protocol: Protocol, Request: request}, &result)
	if err == nil && result.Status != "accepted" {
		err = errors.New("public share recipient OTP response is invalid")
	}
	return err
}

func (c Client) VerifyRecipientOTP(ctx context.Context, request recipientotp.VerifyRequest) (recipientotp.Grant, error) {
	var result recipientotp.Grant
	err := c.callJSON(ctx, "/v1/recipient/verify", recipientVerifyRequest{Protocol: Protocol, Request: request}, &result)
	if err == nil {
		if len(result.InvitationHash) != sha256.Size*2 || result.Epoch == "" || result.ExpiresAt.IsZero() {
			err = errors.New("public share recipient OTP grant is invalid")
		} else if _, decodeErr := hex.DecodeString(result.InvitationHash); decodeErr != nil {
			err = errors.New("public share recipient OTP grant is invalid")
		}
	}
	return result, err
}

func (c Client) Fetch(ctx context.Context, object authority.ObjectRequest) (authority.FetchedLeaf, error) {
	response, err := c.call(ctx, "/v1/fetch", objectRequest{Protocol: Protocol, Object: object})
	if err != nil {
		return authority.FetchedLeaf{}, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return authority.FetchedLeaf{}, authority.ErrNotFound
	}
	size, errSize := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
	revision, errRevision := strconv.ParseInt(response.Header.Get("X-Filees-Revision"), 10, 64)
	displayRaw, errName := base64.RawURLEncoding.Strict().DecodeString(response.Header.Get("X-Filees-Display-Name"))
	cacheKey, checksum := response.Header.Get("X-Filees-Cache-Key"), response.Header.Get("X-Filees-MD5")
	_, errKey := hex.DecodeString(cacheKey)
	_, errChecksum := hex.DecodeString(checksum)
	permit := authority.ObjectPermit{CacheKey: cacheKey, DisplayName: string(displayRaw), Revision: revision}
	if errSize != nil || size < 0 || errRevision != nil || errName != nil || !validPermit(permit, object.Revision) || len(cacheKey) != 64 || errKey != nil || len(checksum) != 32 || errChecksum != nil || response.Header.Get("Content-Type") != "application/octet-stream" {
		response.Body.Close()
		return authority.FetchedLeaf{}, errors.New("public share backchannel fetch metadata is invalid")
	}
	return authority.FetchedLeaf{ObjectPermit: permit, Size: size, MD5: checksum, Body: response.Body}, nil
}

func (c Client) callJSON(ctx context.Context, path string, input, output any) error {
	response, err := c.call(ctx, path, input)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return authority.ErrNotFound
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("public share backchannel response content type is invalid")
	}
	return decodeJSON(response.Body, output, 2<<20)
}

func validPermit(permit authority.ObjectPermit, revision int64) bool {
	if permit.Revision != revision || len(permit.CacheKey) != 64 || strings.TrimSpace(permit.DisplayName) == "" || len(permit.DisplayName) > 512 || strings.ContainsAny(permit.DisplayName, "\x00\r\n") {
		return false
	}
	_, err := hex.DecodeString(permit.CacheKey)
	return err == nil
}

func (c Client) call(ctx context.Context, path string, input any) (*http.Response, error) {
	base, err := url.Parse(c.BaseURL)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("public share backchannel base URL is invalid")
	}
	base.Path, base.RawQuery, base.Fragment = path, "", ""
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, base.String(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(request)
}

func decode(request *http.Request, output any) error {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return errors.New("content type is invalid")
	}
	defer request.Body.Close()
	return decodeJSON(request.Body, output, 2<<20)
}

func decodeJSON(reader io.Reader, output any, limit int64) error {
	raw, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return err
	}
	if int64(len(raw)) > limit {
		return errors.New("JSON payload is too large")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("request contains trailing JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(value)
}

func notFound(w http.ResponseWriter) { http.Error(w, "not found", http.StatusNotFound) }

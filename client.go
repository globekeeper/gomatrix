// Package gomatrix implements the Matrix Client-Server API.
//
// Specification can be found at http://matrix.org/docs/spec/client_server/r0.2.0.html
package gomatrix

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Client represents a Matrix client.
type Client struct {
	HomeserverURL *url.URL     // The base homeserver URL
	Prefix        string       // The API prefix eg '/_matrix/client/r0'
	UserID        string       // The user ID of the client. Used for forming HTTP paths which use the client's user ID.
	AccessToken   string       // The access_token for the client.
	Client        *http.Client // The underlying HTTP client which will be used to make HTTP requests.
	Syncer        Syncer       // The thing which can process /sync responses
	Store         Storer       // The thing which can store rooms/tokens/ids

	// The ?user_id= query parameter for application services. This must be set *prior* to calling a method. If this is empty,
	// no user_id parameter will be sent.
	// See http://matrix.org/docs/spec/application_service/unstable.html#identity-assertion
	AppServiceUserID string

	syncingMutex           sync.Mutex // protects syncingID
	syncingID              uint32     // Identifies the current Sync. Only one Sync can be active at any given time.
	RandomizeXForwardedFor bool       // If true, client will add a random IP as a X-Forwarded-For header. Used to bypass rate limiting in tests. rand.Seed() is not called.
}

// HTTPError An HTTP Error response, which may wrap an underlying native Go Error.
type HTTPError struct {
	Contents     []byte
	WrappedError error
	MatrixError  RespError
	Code         int
	Path         string
	Method       string
}

func (e HTTPError) Error() string {
	var err error
	if e.MatrixError.ErrCode != "" {
		err = e.MatrixError
	} else {
		err = e.WrappedError
	}
	return fmt.Sprintf("http request failed: code: %d method: %s path: %s err: %v", e.Code, e.Method, e.Path, err)
}

// BuildURL builds a URL with the Client's homeserver/prefix set already.
func (cli *Client) BuildURL(urlPath ...string) string {
	ps := append([]string{cli.Prefix}, urlPath...)
	return cli.BuildBaseURL(ps...)
}

// BuildBaseURL builds a URL with the Client's homeserver set already. You must
// supply the prefix in the path.
func (cli *Client) BuildBaseURL(urlPath ...string) string {
	// copy the URL. Purposefully ignore error as the input is from a valid URL already
	hsURL, _ := url.Parse(cli.HomeserverURL.String())
	parts := []string{hsURL.Path}
	parts = append(parts, urlPath...)
	hsURL.Path = path.Join(parts...)
	// Manually add the trailing slash back to the end of the path if it's explicitly needed
	if strings.HasSuffix(urlPath[len(urlPath)-1], "/") {
		hsURL.Path = hsURL.Path + "/"
	}
	query := hsURL.Query()
	if cli.AppServiceUserID != "" {
		query.Set("user_id", cli.AppServiceUserID)
	}
	hsURL.RawQuery = query.Encode()
	return hsURL.String()
}

// BuildURLWithQuery builds a URL with query parameters in addition to the Client's homeserver/prefix set already.
func (cli *Client) BuildURLWithQuery(urlPath []string, urlQuery map[string]string) string {
	u, _ := url.Parse(cli.BuildURL(urlPath...))
	q := u.Query()
	for k, v := range urlQuery {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

// SetCredentials sets the user ID and access token on this client instance.
func (cli *Client) SetCredentials(userID, accessToken string) {
	cli.AccessToken = accessToken
	cli.UserID = userID
}

// ClearCredentials removes the user ID and access token on this client instance.
func (cli *Client) ClearCredentials() {
	cli.AccessToken = ""
	cli.UserID = ""
}

// Sync starts syncing with the provided Homeserver. If Sync() is called twice then the first sync will be stopped and the
// error will be nil.
//
// This function will block until a fatal /sync error occurs, so it should almost always be started as a new goroutine.
// Fatal sync errors can be caused by:
//   - The failure to create a filter.
//   - Client.Syncer.OnFailedSync returning an error in response to a failed sync.
//   - Client.Syncer.ProcessResponse returning an error.
//
// If you wish to continue retrying in spite of these fatal errors, call Sync() again.
func (cli *Client) Sync(ctx context.Context) error {
	// Mark the client as syncing.
	// We will keep syncing until the syncing state changes. Either because
	// Sync is called or StopSync is called.
	syncingID := cli.incrementSyncingID()
	nextBatch := cli.Store.LoadNextBatch(cli.UserID)
	filterID := cli.Store.LoadFilterID(cli.UserID)
	if filterID == "" {
		filterJSON := cli.Syncer.GetFilterJSON(cli.UserID)
		resFilter, err := cli.CreateFilter(ctx, filterJSON)
		if err != nil {
			return err
		}
		filterID = resFilter.FilterID
		cli.Store.SaveFilterID(cli.UserID, filterID)
	}

	for {
		resSync, err := cli.SyncRequest(ctx, 30000, nextBatch, "91", false, "")
		if err != nil {
			duration, err2 := cli.Syncer.OnFailedSync(resSync, err)
			if err2 != nil {
				return err2
			}
			time.Sleep(duration)
			continue
		}

		// Check that the syncing state hasn't changed
		// Either because we've stopped syncing or another sync has been started.
		// We discard the response from our sync.
		if cli.getSyncingID() != syncingID {
			return nil
		}

		// Save the token now *before* processing it. This means it's possible
		// to not process some events, but it means that we won't get constantly stuck processing
		// a malformed/buggy event which keeps making us panic.
		cli.Store.SaveNextBatch(cli.UserID, resSync.NextBatch)
		if err = cli.Syncer.ProcessResponse(resSync, nextBatch); err != nil {
			return err
		}

		nextBatch = resSync.NextBatch
	}
}

func (cli *Client) incrementSyncingID() uint32 {
	cli.syncingMutex.Lock()
	defer cli.syncingMutex.Unlock()
	cli.syncingID++
	return cli.syncingID
}

func (cli *Client) getSyncingID() uint32 {
	cli.syncingMutex.Lock()
	defer cli.syncingMutex.Unlock()
	return cli.syncingID
}

// StopSync stops the ongoing sync started by Sync.
func (cli *Client) StopSync() {
	// Advance the syncing state so that any running Syncs will terminate.
	cli.incrementSyncingID()
}

// MakeRequest makes a JSON HTTP request to the given URL.
// The response body will be stream decoded into an interface. This will automatically stop if the response
// body is nil.
//
// Returns an error if the response is not 2xx along with the HTTP body bytes if it got that far. This error is
// an HTTPError which includes the returned HTTP status code, byte contents of the response body and possibly a
// RespError as the WrappedError, if the HTTP body could be decoded as a RespError.
func (cli *Client) MakeRequest(ctx context.Context, method string, httpURL string, reqBody interface{}, resBody interface{}) error {
	var req *http.Request
	var err error
	if reqBody != nil {
		buf := new(bytes.Buffer)
		if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
			return err
		}
		req, err = http.NewRequestWithContext(ctx, method, httpURL, buf)
	} else {
		req, err = http.NewRequestWithContext(ctx, method, httpURL, nil)
	}

	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	if cli.AccessToken != "" {
		req.Header.Set("Authorization", "Bearer "+cli.AccessToken)
	}
	if cli.RandomizeXForwardedFor {
		ip := rand.Uint32()
		buf := make([]byte, 4)
		binary.LittleEndian.PutUint32(buf, ip)
		req.Header.Set("X-Forwarded-For", net.IP(buf).String())
	}

	res, err := cli.Client.Do(req)
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		return err
	}
	if res.StatusCode/100 != 2 { // not 2xx
		return respToHttpErr(res, req, method)
	}

	if resBody != nil && res.Body != nil {
		return json.NewDecoder(res.Body).Decode(&resBody)
	}

	return nil
}

func respToHttpErr(res *http.Response, req *http.Request, method string) *HTTPError {
	httpErr := &HTTPError{
		Code:   res.StatusCode,
		Path:   req.URL.Path,
		Method: req.Method,
	}
	contents, err := ioutil.ReadAll(res.Body)
	if err != nil {
		httpErr.WrappedError = fmt.Errorf("upload request failed: failed to read response body: %w", err)
		return httpErr
	}
	httpErr.Contents = contents
	err = json.Unmarshal(contents, &httpErr.MatrixError)
	if err != nil {
		httpErr.WrappedError = fmt.Errorf("upload request failed: failed to unmarshall response: %w", err)
		return httpErr
	}
	httpErr.WrappedError = fmt.Errorf("request failed: method: %s path: %s body: %s", method, req.URL.Path, contents)
	return httpErr
}

// CreateFilter makes an HTTP request according to http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-user-userid-filter
func (cli *Client) CreateFilter(ctx context.Context, filter json.RawMessage) (resp *RespCreateFilter, err error) {
	urlPath := cli.BuildURL("user", cli.UserID, "filter")
	err = cli.MakeRequest(ctx, "POST", urlPath, &filter, &resp)
	return
}

// SyncRequest makes an HTTP request according to http://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-sync
func (cli *Client) SyncRequest(ctx context.Context, timeout int, since, filterID string, fullState bool, setPresence string) (resp *RespSync, err error) {
	query := map[string]string{
		"timeout": strconv.Itoa(timeout),
	}
	if since != "" {
		query["since"] = since
	}
	if filterID != "" {
		query["filter"] = filterID
	}
	if setPresence != "" {
		query["set_presence"] = setPresence
	}
	if fullState {
		query["full_state"] = "true"
	}
	urlPath := cli.BuildURLWithQuery([]string{"sync"}, query)
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

func (cli *Client) register(ctx context.Context, u string, req *ReqRegister) (resp *RespRegister, uiaResp *RespUserInteractive, err error) {
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	if err != nil {
		httpErr, ok := err.(*HTTPError)
		if !ok { // network error
			return
		}
		if httpErr.Code == 401 {
			// body should be RespUserInteractive, if it isn't, fail with the error
			err = json.Unmarshal(httpErr.Contents, &uiaResp)
			return
		}
	}
	return
}

// Register makes an HTTP request according to http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-register
//
// Registers with kind=user. For kind=guest, see RegisterGuest.
func (cli *Client) Register(ctx context.Context, req *ReqRegister) (*RespRegister, *RespUserInteractive, error) {
	u := cli.BuildURL("register")
	return cli.register(ctx, u, req)
}

// RegisterGuest makes an HTTP request according to http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-register
// with kind=guest.
//
// For kind=user, see Register.
func (cli *Client) RegisterGuest(ctx context.Context, req *ReqRegister) (*RespRegister, *RespUserInteractive, error) {
	query := map[string]string{
		"kind": "guest",
	}
	u := cli.BuildURLWithQuery([]string{"register"}, query)
	return cli.register(ctx, u, req)
}

// RegisterDummy performs m.login.dummy registration according to https://matrix.org/docs/spec/client_server/r0.2.0.html#dummy-auth
//
// Only a username and password need to be provided on the ReqRegister struct. Most local/developer homeservers will allow registration
// this way. If the homeserver does not, an error is returned.
//
// This does not set credentials on the client instance. See SetCredentials() instead.
//
//		res, err := cli.RegisterDummy(&gomatrix.ReqRegister{
//			Username: "alice",
//			Password: "wonderland",
//		})
//	 if err != nil {
//			panic(err)
//		}
//		token := res.AccessToken
func (cli *Client) RegisterDummy(ctx context.Context, req *ReqRegister) (*RespRegister, error) {
	res, uia, err := cli.Register(ctx, req)
	if err != nil && uia == nil {
		return nil, err
	}
	if uia != nil && uia.HasSingleStageFlow("m.login.dummy") {
		req.Auth = struct {
			Type    string `json:"type"`
			Session string `json:"session,omitempty"`
		}{"m.login.dummy", uia.Session}
		res, _, err = cli.Register(ctx, req)
		if err != nil {
			return nil, err
		}
	}
	if res == nil {
		return nil, fmt.Errorf("registration failed: does this server support m.login.dummy?")
	}
	return res, nil
}

// Login a user to the homeserver according to http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-login
// This does not set credentials on this client instance. See SetCredentials() instead.
func (cli *Client) Login(ctx context.Context, req *ReqLogin) (resp *RespLogin, err error) {
	urlPath := cli.BuildURL("login")
	err = cli.MakeRequest(ctx, "POST", urlPath, req, &resp)
	return
}

// Logout the current user. See http://matrix.org/docs/spec/client_server/r0.6.0.html#post-matrix-client-r0-logout
// This does not clear the credentials from the client instance. See ClearCredentials() instead.
func (cli *Client) Logout(ctx context.Context) (resp *RespLogout, err error) {
	urlPath := cli.BuildURL("logout")
	err = cli.MakeRequest(ctx, "POST", urlPath, nil, &resp)
	return
}

// LogoutAll logs the current user out on all devices. See https://matrix.org/docs/spec/client_server/r0.6.0#post-matrix-client-r0-logout-all
// This does not clear the credentials from the client instance. See ClearCredentails() instead.
func (cli *Client) LogoutAll(ctx context.Context) (resp *RespLogoutAll, err error) {
	urlPath := cli.BuildURL("logout/all")
	err = cli.MakeRequest(ctx, "POST", urlPath, nil, &resp)
	return
}

// Versions returns the list of supported Matrix versions on this homeserver. See http://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-versions
func (cli *Client) Versions(ctx context.Context) (resp *RespVersions, err error) {
	urlPath := cli.BuildBaseURL("_matrix", "client", "versions")
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// PublicRooms returns the list of public rooms on target server. See https://matrix.org/docs/spec/client_server/r0.6.0#get-matrix-client-unstable-publicrooms
func (cli *Client) PublicRooms(ctx context.Context, limit int, since string, server string) (resp *RespPublicRooms, err error) {
	args := map[string]string{}

	if limit != 0 {
		args["limit"] = strconv.Itoa(limit)
	}
	if since != "" {
		args["since"] = since
	}
	if server != "" {
		args["server"] = server
	}

	urlPath := cli.BuildURLWithQuery([]string{"publicRooms"}, args)
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// PublicRoomsFiltered returns a subset of PublicRooms filtered server side.
// See https://matrix.org/docs/spec/client_server/r0.6.0#post-matrix-client-unstable-publicrooms
func (cli *Client) PublicRoomsFiltered(ctx context.Context, limit int, since string, server string, filter string) (resp *RespPublicRooms, err error) {
	content := map[string]string{}

	if limit != 0 {
		content["limit"] = strconv.Itoa(limit)
	}
	if since != "" {
		content["since"] = since
	}
	if filter != "" {
		content["filter"] = filter
	}

	var urlPath string
	if server == "" {
		urlPath = cli.BuildURL("publicRooms")
	} else {
		urlPath = cli.BuildURLWithQuery([]string{"publicRooms"}, map[string]string{
			"server": server,
		})
	}

	err = cli.MakeRequest(ctx, "POST", urlPath, content, &resp)
	return
}

// JoinRoom joins the client to a room ID or alias. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-join-roomidoralias
//
// If serverName is specified, this will be added as a query param to instruct the homeserver to join via that server. If content is specified, it will
// be JSON encoded and used as the request body.
func (cli *Client) JoinRoom(ctx context.Context, roomIDorAlias, serverName string, content interface{}) (resp *RespJoinRoom, err error) {
	var urlPath string
	if serverName != "" {
		urlPath = cli.BuildURLWithQuery([]string{"join", roomIDorAlias}, map[string]string{
			"server_name": serverName,
		})
	} else {
		urlPath = cli.BuildURL("join", roomIDorAlias)
	}
	err = cli.MakeRequest(ctx, "POST", urlPath, content, &resp)
	return
}

// GetDisplayName returns the display name of the user from the specified MXID. See https://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-profile-userid-displayname
func (cli *Client) GetDisplayName(ctx context.Context, mxid string) (resp *RespUserDisplayName, err error) {
	urlPath := cli.BuildURL("profile", mxid, "displayname")
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// GetOwnDisplayName returns the user's display name. See https://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-profile-userid-displayname
func (cli *Client) GetOwnDisplayName(ctx context.Context) (resp *RespUserDisplayName, err error) {
	urlPath := cli.BuildURL("profile", cli.UserID, "displayname")
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// SetDisplayName sets the user's profile display name. See http://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-profile-userid-displayname
func (cli *Client) SetDisplayName(ctx context.Context, displayName string) (err error) {
	urlPath := cli.BuildURL("profile", cli.UserID, "displayname")
	s := struct {
		DisplayName string `json:"displayname"`
	}{displayName}
	err = cli.MakeRequest(ctx, "PUT", urlPath, &s, nil)
	return
}

// GetAvatarURL gets the user's avatar URL. See http://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-profile-userid-avatar-url
func (cli *Client) GetAvatarURL(ctx context.Context) (string, error) {
	urlPath := cli.BuildURL("profile", cli.UserID, "avatar_url")
	s := struct {
		AvatarURL string `json:"avatar_url"`
	}{}

	err := cli.MakeRequest(ctx, "GET", urlPath, nil, &s)
	if err != nil {
		return "", err
	}

	return s.AvatarURL, nil
}

// SetAvatarURL sets the user's avatar URL. See http://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-profile-userid-avatar-url
func (cli *Client) SetAvatarURL(ctx context.Context, url string) error {
	urlPath := cli.BuildURL("profile", cli.UserID, "avatar_url")
	s := struct {
		AvatarURL string `json:"avatar_url"`
	}{url}
	err := cli.MakeRequest(ctx, "PUT", urlPath, &s, nil)
	if err != nil {
		return err
	}

	return nil
}

// GetStatus returns the status of the user from the specified MXID. See https://matrix.org/docs/spec/client_server/r0.6.0#get-matrix-client-r0-presence-userid-status
func (cli *Client) GetStatus(ctx context.Context, mxid string) (resp *RespUserStatus, err error) {
	urlPath := cli.BuildURL("presence", mxid, "status")
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// GetOwnStatus returns the user's status. See https://matrix.org/docs/spec/client_server/r0.6.0#get-matrix-client-r0-presence-userid-status
func (cli *Client) GetOwnStatus(ctx context.Context) (resp *RespUserStatus, err error) {
	return cli.GetStatus(ctx, cli.UserID)
}

// SetStatus sets the user's status. See https://matrix.org/docs/spec/client_server/r0.6.0#put-matrix-client-r0-presence-userid-status
func (cli *Client) SetStatus(ctx context.Context, presence, status string) (err error) {
	urlPath := cli.BuildURL("presence", cli.UserID, "status")
	s := struct {
		Presence  string `json:"presence"`
		StatusMsg string `json:"status_msg"`
	}{presence, status}
	err = cli.MakeRequest(ctx, "PUT", urlPath, &s, nil)
	return
}

// SendMessageEvent sends a message event into a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-rooms-roomid-send-eventtype-txnid
// contentJSON should be a pointer to something that can be encoded as JSON using json.Marshal.
func (cli *Client) SendMessageEvent(ctx context.Context, roomID string, eventType string, contentJSON interface{}) (resp *RespSendEvent, err error) {
	txnID := txnID()
	urlPath := cli.BuildURL("rooms", roomID, "send", eventType, txnID)
	err = cli.MakeRequest(ctx, "PUT", urlPath, contentJSON, &resp)
	return
}

// SendStateEvent sends a state event into a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-rooms-roomid-state-eventtype-statekey
// contentJSON should be a pointer to something that can be encoded as JSON using json.Marshal.
func (cli *Client) SendStateEvent(ctx context.Context, roomID, eventType, stateKey string, contentJSON interface{}) (resp *RespSendEvent, err error) {
	urlPath := cli.BuildURL("rooms", roomID, "state", eventType, stateKey)
	err = cli.MakeRequest(ctx, "PUT", urlPath, contentJSON, &resp)
	return
}

// SendText sends an m.room.message event into the given room with a msgtype of m.text
// See http://matrix.org/docs/spec/client_server/r0.2.0.html#m-text
func (cli *Client) SendText(ctx context.Context, roomID, text string) (*RespSendEvent, error) {
	return cli.SendMessageEvent(ctx, roomID, "m.room.message",
		TextMessage{MsgType: "m.text", Body: text})
}

// SendFormattedText sends an m.room.message event into the given room with a msgtype of m.text, supports a subset of HTML for formatting.
// See https://matrix.org/docs/spec/client_server/r0.6.0#m-text
func (cli *Client) SendFormattedText(ctx context.Context, roomID, text, formattedText string) (*RespSendEvent, error) {
	return cli.SendMessageEvent(ctx, roomID, "m.room.message",
		TextMessage{MsgType: "m.text", Body: text, FormattedBody: formattedText, Format: "org.matrix.custom.html"})
}

// SendImage sends an m.room.message event into the given room with a msgtype of m.image
// See https://matrix.org/docs/spec/client_server/r0.2.0.html#m-image
func (cli *Client) SendImage(ctx context.Context, roomID, body, url string) (*RespSendEvent, error) {
	return cli.SendMessageEvent(ctx, roomID, "m.room.message",
		ImageMessage{
			MsgType: "m.image",
			Body:    body,
			URL:     url,
		})
}

// SendVideo sends an m.room.message event into the given room with a msgtype of m.video
// See https://matrix.org/docs/spec/client_server/r0.2.0.html#m-video
func (cli *Client) SendVideo(ctx context.Context, roomID, body, url string) (*RespSendEvent, error) {
	return cli.SendMessageEvent(ctx, roomID, "m.room.message",
		VideoMessage{
			MsgType: "m.video",
			Body:    body,
			URL:     url,
		})
}

// SendNotice sends an m.room.message event into the given room with a msgtype of m.notice
// See http://matrix.org/docs/spec/client_server/r0.2.0.html#m-notice
func (cli *Client) SendNotice(ctx context.Context, roomID, text string) (*RespSendEvent, error) {
	return cli.SendMessageEvent(ctx, roomID, "m.room.message",
		TextMessage{MsgType: "m.notice", Body: text})
}

// RedactEvent redacts the given event. See http://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-rooms-roomid-redact-eventid-txnid
func (cli *Client) RedactEvent(ctx context.Context, roomID, eventID string, req *ReqRedact) (resp *RespSendEvent, err error) {
	txnID := txnID()
	urlPath := cli.BuildURL("rooms", roomID, "redact", eventID, txnID)
	err = cli.MakeRequest(ctx, "PUT", urlPath, req, &resp)
	return
}

// MarkRead marks eventID in roomID as read, signifying the event, and all before it have been read. See https://matrix.org/docs/spec/client_server/r0.6.0#post-matrix-client-r0-rooms-roomid-receipt-receipttype-eventid
func (cli *Client) MarkRead(ctx context.Context, roomID, eventID string) error {
	urlPath := cli.BuildURL("rooms", roomID, "receipt", "m.read", eventID)
	return cli.MakeRequest(ctx, "POST", urlPath, nil, nil)
}

// CreateRoom creates a new Matrix room. See https://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-createroom
func (cli *Client) CreateRoom(ctx context.Context, req *ReqCreateRoom) (resp *RespCreateRoom, err error) {
	urlPath := cli.BuildURL("createRoom")
	err = cli.MakeRequest(ctx, "POST", urlPath, req, &resp)
	return
}

// LeaveRoom leaves the given room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-leave
func (cli *Client) LeaveRoom(ctx context.Context, roomID string) (resp *RespLeaveRoom, err error) {
	u := cli.BuildURL("rooms", roomID, "leave")
	err = cli.MakeRequest(ctx, "POST", u, struct{}{}, &resp)
	return
}

// ForgetRoom forgets a room entirely. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-forget
func (cli *Client) ForgetRoom(ctx context.Context, roomID string) (resp *RespForgetRoom, err error) {
	u := cli.BuildURL("rooms", roomID, "forget")
	err = cli.MakeRequest(ctx, "POST", u, struct{}{}, &resp)
	return
}

// InviteUser invites a user to a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-invite
func (cli *Client) InviteUser(ctx context.Context, roomID string, req *ReqInviteUser) (resp *RespInviteUser, err error) {
	u := cli.BuildURL("rooms", roomID, "invite")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

// InviteUserByThirdParty invites a third-party identifier to a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#invite-by-third-party-id-endpoint
func (cli *Client) InviteUserByThirdParty(ctx context.Context, roomID string, req *ReqInvite3PID) (resp *RespInviteUser, err error) {
	u := cli.BuildURL("rooms", roomID, "invite")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

// KickUser kicks a user from a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-kick
func (cli *Client) KickUser(ctx context.Context, roomID string, req *ReqKickUser) (resp *RespKickUser, err error) {
	u := cli.BuildURL("rooms", roomID, "kick")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

// BanUser bans a user from a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-ban
func (cli *Client) BanUser(ctx context.Context, roomID string, req *ReqBanUser) (resp *RespBanUser, err error) {
	u := cli.BuildURL("rooms", roomID, "ban")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

// UnbanUser unbans a user from a room. See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-unban
func (cli *Client) UnbanUser(ctx context.Context, roomID string, req *ReqUnbanUser) (resp *RespUnbanUser, err error) {
	u := cli.BuildURL("rooms", roomID, "unban")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

// UserTyping sets the typing status of the user. See https://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-rooms-roomid-typing-userid
func (cli *Client) UserTyping(ctx context.Context, roomID string, typing bool, timeout int64) (resp *RespTyping, err error) {
	req := ReqTyping{Typing: typing, Timeout: timeout}
	u := cli.BuildURL("rooms", roomID, "typing", cli.UserID)
	err = cli.MakeRequest(ctx, "PUT", u, req, &resp)
	return
}

// StateEvent gets a single state event in a room. It will attempt to JSON unmarshal into the given "outContent" struct with
// the HTTP response body, or return an error.
// See http://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-rooms-roomid-state-eventtype-statekey
func (cli *Client) StateEvent(ctx context.Context, roomID, eventType, stateKey string, outContent interface{}) (err error) {
	u := cli.BuildURL("rooms", roomID, "state", eventType, stateKey)
	err = cli.MakeRequest(ctx, "GET", u, nil, outContent)
	return
}

// UploadLink uploads an HTTP URL and then returns an MXC URI.
func (cli *Client) UploadLink(ctx context.Context, link string) (*RespMediaUpload, error) {
	res, err := cli.Client.Get(link)
	if res != nil {
		defer res.Body.Close()
	}
	if err != nil {
		return nil, err
	}
	return cli.UploadToContentRepo(ctx, res.Body, res.Header.Get("Content-Type"), res.ContentLength)
}

// UploadToContentRepo uploads the given bytes to the content repository and returns an MXC URI.
// See http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-media-r0-upload
func (cli *Client) UploadToContentRepo(ctx context.Context, content io.Reader, contentType string, contentLength int64) (*RespMediaUpload, error) {
	req, err := http.NewRequest(http.MethodPost, cli.BuildBaseURL("_matrix/media/r0/upload"), content)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+cli.AccessToken)

	req.ContentLength = contentLength

	res, err := cli.Client.Do(req)
	if res != nil {
		defer res.Body.Close()
	}

	if err != nil {
		return nil, err
	}

	if res.StatusCode != 200 {
		return nil, respToHttpErr(res, req, http.MethodPost)
	}

	var m RespMediaUpload
	if err := json.NewDecoder(res.Body).Decode(&m); err != nil {
		return nil, err
	}

	return &m, nil
}

// JoinedMembers returns a map of joined room members. See TODO-SPEC. https://github.com/matrix-org/synapse/pull/1680
//
// In general, usage of this API is discouraged in favour of /sync, as calling this API can race with incoming membership changes.
// This API is primarily designed for application services which may want to efficiently look up joined members in a room.
func (cli *Client) JoinedMembers(ctx context.Context, roomID string) (resp *RespJoinedMembers, err error) {
	u := cli.BuildURL("rooms", roomID, "joined_members")
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// JoinedRooms returns a list of rooms which the client is joined to. See TODO-SPEC. https://github.com/matrix-org/synapse/pull/1680
//
// In general, usage of this API is discouraged in favour of /sync, as calling this API can race with incoming membership changes.
// This API is primarily designed for application services which may want to efficiently look up joined rooms.
func (cli *Client) JoinedRooms(ctx context.Context) (resp *RespJoinedRooms, err error) {
	u := cli.BuildURL("joined_rooms")
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// Messages returns a list of message and state events for a room. It uses
// pagination query parameters to paginate history in the room.
// See https://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-rooms-roomid-messages
func (cli *Client) Messages(ctx context.Context, roomID, from, to string, dir rune, limit int) (resp *RespMessages, err error) {
	query := map[string]string{
		"from": from,
		"dir":  string(dir),
	}
	if to != "" {
		query["to"] = to
	}
	if limit != 0 {
		query["limit"] = strconv.Itoa(limit)
	}

	urlPath := cli.BuildURLWithQuery([]string{"rooms", roomID, "messages"}, query)
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// TurnServer returns turn server details and credentials for the client to use when initiating calls.
// See http://matrix.org/docs/spec/client_server/r0.2.0.html#get-matrix-client-r0-voip-turnserver
func (cli *Client) TurnServer(ctx context.Context) (resp *RespTurnServer, err error) {
	urlPath := cli.BuildURL("voip", "turnServer")
	err = cli.MakeRequest(ctx, "GET", urlPath, nil, &resp)
	return
}

// WhoAmI Gets information about the owner of a given access token.
// See https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-account-whoami
func (cli *Client) WhoAmI(ctx context.Context) (resp *RespWhoAmI, err error) {
	u := cli.BuildURL("account", "whoami")
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// RoomAlias requests that the server resolve a room alias to a room ID.
// See https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-directory-room-roomalias
func (cli *Client) RoomAlias(ctx context.Context, roomAlias string) (resp *RespRoomAlias, err error) {
	u := cli.BuildURL("directory", "room", roomAlias)
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// EmailRequestToken requests email from homeserver so that it email be bound to existing account after validation.
// See https://matrix.org/docs/spec/client_server/r0.6.1#post-matrix-client-r0-account-3pid-email-requesttoken
func (cli *Client) Account3PidEmailRequestToken(ctx context.Context, req ReqEmailRequestToken) (resp *RespEmailRequestToken, err error) {
	u := cli.BuildURL("account", "3pid", "email", "requestToken")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

func (cli *Client) RegisterEmailRequestToken(ctx context.Context, req ReqEmailRequestToken) (resp *RespEmailRequestToken, err error) {
	u := cli.BuildURL("register", "email", "requestToken")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

func (cli *Client) PasswordEmailRequestToken(ctx context.Context, req ReqEmailRequestToken) (resp *RespEmailRequestToken, err error) {
	u := cli.BuildURL("account", "password", "email", "requestToken")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

func (cli *Client) AccountPassword(ctx context.Context, req ReqAccountPassword) (err error) {
	u := cli.BuildURL("account", "password")
	err = cli.MakeRequest(ctx, "POST", u, req, nil)
	return
}

// GetAccountData gets some account_data for the client.
// See https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-user-userid-account-data-type
func (cli *Client) GetAccountData(ctx context.Context, req ReqGetAccountData) (resp RespAccountData, err error) {
	resp = make(RespAccountData)
	u := cli.BuildURL("user", cli.UserID, "account_data", req.Type)
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// PutAccountData sets some account_data for the client.
// See https://matrix.org/docs/spec/client_server/r0.6.1#put-matrix-client-r0-user-userid-account-data-type
func (cli *Client) PutAccountData(ctx context.Context, req ReqPutAccountData) (err error) {
	u := cli.BuildURL("user", cli.UserID, "account_data", req.Type)
	err = cli.MakeRequest(ctx, "PUT", u, req.Data, nil)
	return
}

// GetDevices gets information about all devices for the current user.
// See https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-devices
func (cli *Client) GetDevices(ctx context.Context) (resp RespGetDevices, err error) {
	resp.Devices = make([]Device, 0)
	u := cli.BuildURL("devices")
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// GetThreePID gets a list of the third party identifiers that the homeserver has associated with the user's account.
// See https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-account-3pid
func (cli *Client) GetThreePID(ctx context.Context) (resp RespGetThreePID, err error) {
	u := cli.BuildURL("account", "3pid")
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

func (cli *Client) PostThreePID(ctx context.Context, req ReqPostThreePID) (err error) {
	u := cli.BuildURL("account", "3pid")
	err = cli.MakeRequest(ctx, http.MethodPost, u, req, nil)
	return
}

// Available checks to see if a username is available, and valid, for the server.
// See https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-register-available
func (cli *Client) Available(ctx context.Context, username string) (err error) {
	u := cli.BuildURLWithQuery(
		[]string{"register", "available"},
		map[string]string{
			"username": username,
		})
	err = cli.MakeRequest(ctx, "GET", u, nil, nil)
	return
}

// PowerLevels gets most recent m.room.power_levels event.
// See https://matrix.org/docs/spec/client_server/r0.6.1#m-room-power-levels
func (cli *Client) PowerLevels(ctx context.Context, roomID string) (resp PowerLevels, err error) {
	err = cli.StateEvent(ctx, roomID, "m.room.power_levels", "", &resp)
	return
}

func (cli *Client) LeftMembers(ctx context.Context, roomId string) (resp RespMembers, err error) {
	query := map[string]string{
		"membership": "leave",
	}
	u := cli.BuildURLWithQuery([]string{"rooms", roomId, "members"}, query)
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

func (cli *Client) InvitedMembers(ctx context.Context, roomId string) (resp RespMembers, err error) {
	query := map[string]string{
		"membership": "invite",
	}
	u := cli.BuildURLWithQuery([]string{"rooms", roomId, "members"}, query)
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

func (cli *Client) Members(ctx context.Context, roomId string) (resp RespMembers, err error) {
	u := cli.BuildURL("rooms", roomId, "members")
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

// SendPowerLevels sends m.room.power_levels event.
// See https://matrix.org/docs/spec/client_server/r0.6.1#m-room-power-levels
func (cli *Client) SendPowerLevels(ctx context.Context, roomID string, pl PowerLevels) (*RespSendEvent, error) {
	return cli.SendStateEvent(ctx, roomID, "m.room.power_levels", "", pl)
}

func (cli *Client) Hierarchy(ctx context.Context, req ReqHierarchy) (resp RespHierarchy, err error) {
	u := cli.BuildURLWithQuery([]string{"rooms", req.RoomId, "hierarchy"}, map[string]string{
		"suggested_only": strconv.FormatBool(req.SuggestedOnly),
		"limit":          strconv.Itoa(req.Limit),
	})
	err = cli.MakeRequest(ctx, "GET", u, nil, &resp)
	return
}

func (cli *Client) Deactivate(ctx context.Context) (err error) {
	u := cli.BuildURL("account", "deactivate")
	err = cli.MakeRequest(ctx, "POST", u, struct{}{}, nil)
	return
}

func (cli *Client) UserDirectorySearch(ctx context.Context, req *ReqUserDirectorySearch) (resp RespUserDirectorySearch, err error) {
	u := cli.BuildURL("user_directory", "search")
	err = cli.MakeRequest(ctx, "POST", u, req, &resp)
	return
}

func txnID() string {
	return "go" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

// NewClient creates a new Matrix Client ready for syncing
func NewClient(homeserverURL, userID, accessToken string) (*Client, error) {
	hsURL, err := url.Parse(homeserverURL)
	if err != nil {
		return nil, err
	}
	// By default, use an in-memory store which will never save filter ids / next batch tokens to disk.
	// The client will work with this storer: it just won't remember across restarts.
	// In practice, a database backend should be used.
	store := NewInMemoryStore()
	cli := Client{
		AccessToken:   accessToken,
		HomeserverURL: hsURL,
		UserID:        userID,
		Prefix:        "/_matrix/client/r0",
		Syncer:        NewDefaultSyncer(userID, store),
		Store:         store,
	}
	// By default, use the default HTTP client.
	cli.Client = http.DefaultClient

	return &cli, nil
}

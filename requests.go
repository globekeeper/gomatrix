package gomatrix

// ReqRegister is the JSON request for http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-register
type ReqRegister struct {
	Username                 string      `json:"username,omitempty"`
	BindEmail                bool        `json:"bind_email,omitempty"`
	Password                 string      `json:"password,omitempty"`
	DeviceID                 string      `json:"device_id,omitempty"`
	InitialDeviceDisplayName string      `json:"initial_device_display_name"`
	InhibitLogin             bool        `json:"inhibit_login"`
	Auth                     interface{} `json:"auth,omitempty"`
}

// ReqLogin is the JSON request for http://matrix.org/docs/spec/client_server/r0.6.0.html#post-matrix-client-r0-login
type ReqLogin struct {
	Type                     string     `json:"type"`
	Identifier               Identifier `json:"identifier,omitempty"`
	Password                 string     `json:"password,omitempty"`
	Medium                   string     `json:"medium,omitempty"`
	User                     string     `json:"user,omitempty"`
	Address                  string     `json:"address,omitempty"`
	Token                    string     `json:"token,omitempty"`
	DeviceID                 string     `json:"device_id,omitempty"`
	InitialDeviceDisplayName string     `json:"initial_device_display_name,omitempty"`
	InhibitDevice            bool       `json:"inhibit_device"`
	TotpSid                  string     `json:"totp_sid"`
	Passcode                 string     `json:"passcode"`
	Sid                      string     `json:"sid,omitempty"`
}

// ReqCreateRoom is the JSON request for https://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-createroom
type ReqCreateRoom struct {
	Visibility      string                 `json:"visibility,omitempty"`
	RoomAliasName   string                 `json:"room_alias_name,omitempty"`
	Name            string                 `json:"name,omitempty"`
	Topic           string                 `json:"topic,omitempty"`
	Invite          []string               `json:"invite,omitempty"`
	Invite3PID      []ReqInvite3PID        `json:"invite_3pid,omitempty"`
	CreationContent map[string]interface{} `json:"creation_content,omitempty"`
	InitialState    []Event                `json:"initial_state,omitempty"`
	Preset          string                 `json:"preset,omitempty"`
	IsDirect        bool                   `json:"is_direct,omitempty"`
	RoomVersion     string                 `json:"room_version,omitempty"`
}

// ReqRedact is the JSON request for http://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-rooms-roomid-redact-eventid-txnid
type ReqRedact struct {
	Reason string `json:"reason,omitempty"`
}

// ReqInvite3PID is the JSON request for https://matrix.org/docs/spec/client_server/r0.2.0.html#id57
// It is also a JSON object used in https://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-createroom
type ReqInvite3PID struct {
	IDServer string `json:"id_server"`
	Medium   string `json:"medium"`
	Address  string `json:"address"`
}

// ReqInviteUser is the JSON request for http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-invite
type ReqInviteUser struct {
	UserID string `json:"user_id"`
	Reason string `json:"reason"`
}

// ReqKickUser is the JSON request for http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-kick
type ReqKickUser struct {
	Reason string `json:"reason,omitempty"`
	UserID string `json:"user_id"`
}

// ReqBanUser is the JSON request for http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-ban
type ReqBanUser struct {
	Reason string `json:"reason,omitempty"`
	UserID string `json:"user_id"`
}

// ReqUnbanUser is the JSON request for http://matrix.org/docs/spec/client_server/r0.2.0.html#post-matrix-client-r0-rooms-roomid-unban
type ReqUnbanUser struct {
	UserID string `json:"user_id"`
}

// ReqTyping is the JSON request for https://matrix.org/docs/spec/client_server/r0.2.0.html#put-matrix-client-r0-rooms-roomid-typing-userid
type ReqTyping struct {
	Typing  bool  `json:"typing"`
	Timeout int64 `json:"timeout"`
}

// ReqGetAccountData is the JSON request for https://matrix.org/docs/spec/client_server/r0.6.1#get-matrix-client-r0-user-userid-account-data-type
type ReqGetAccountData struct {
	Type string
}

// ReqPutAccountData is the JSON request for https://matrix.org/docs/spec/client_server/r0.6.1#put-matrix-client-r0-user-userid-account-data-type
type ReqPutAccountData struct {
	ReqGetAccountData
	Data map[string]interface{}
}

// ReqEmailRequestToken is the JSON request for
//
//	https://matrix.org/docs/spec/client_server/r0.6.1#post-matrix-client-r0-register-email-requesttoken
//	https://matrix.org/docs/spec/client_server/r0.6.1#post-matrix-client-r0-account-password-email-requesttoken
//	https://matrix.org/docs/spec/client_server/r0.6.1#post-matrix-client-r0-account-3pid-email-requesttoken
type ReqEmailRequestToken struct {
	IdServer      string `json:"id_server,omitempty"`
	IdAccessToken string `json:"id_access_token,omitempty"`
	Secret        string `json:"client_secret"`
	Email         string `json:"email"`
	SendAttempt   int    `json:"send_attempt"`
	NextLink      string `json:"next_link,omitempty"`
}

type ReqPostThreePID struct {
	ThreePIDCredes ThreePIDCreds `json:"three_pid_creds"`
}

type ThreePIDCreds struct {
	ClientSecret  string `json:"client_secret"`
	IdAccessToken string `json:"id_access_token,omitempty"`
	IdServer      string `json:"id_server"`
	Sid           string `json:"sid"`
}

type ReqHierarchy struct {
	RoomId        string
	SuggestedOnly bool
	Limit         int
}

type ReqAccountPassword struct {
	LogoutDevices bool        `json:"logout_devices"`
	NewPassword   string      `json:"new_password"`
	Auth          interface{} `json:"auth"`
}

type ReqUserDirectorySearch struct {
	Limit      int32  `json:"limit"`
	SearchTerm string `json:"search_term"`
}

type ReqPutPushRule struct {
	Before string `json:"-"`
	After  string `json:"-"`

	Actions    []PushActionType `json:"actions"`
	Conditions []PushCondition  `json:"conditions"`
	Pattern    string           `json:"pattern"`
}

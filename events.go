package gomatrix

import (
	"html"
	"regexp"
)

// Event represents a single Matrix event.
type Event struct {
	StateKey    *string                `json:"state_key,omitempty"`    // The state key for the event. Only present on State Events.
	Sender      string                 `json:"sender"`                 // The user ID of the sender of the event
	Type        string                 `json:"type"`                   // The event type
	Timestamp   int64                  `json:"origin_server_ts"`       // The unix timestamp when this message was sent by the origin server
	ID          string                 `json:"event_id"`               // The unique ID of this event
	RoomID      string                 `json:"room_id"`                // The room the event was sent to. May be nil (e.g. for presence)
	Redacts     string                 `json:"redacts,omitempty"`      // The event ID that was redacted if a m.room.redaction event
	Unsigned    map[string]interface{} `json:"unsigned"`               // The unsigned portions of the event, such as age and prev_content
	Content     map[string]interface{} `json:"content"`                // The JSON content of the event.
	PrevContent map[string]interface{} `json:"prev_content,omitempty"` // The JSON prev_content of the event.
}

// Body returns the value of the "body" key in the event content if it is
// present and is a string.
func (event *Event) Body() (body string, ok bool) {
	value, exists := event.Content["body"]
	if !exists {
		return
	}
	body, ok = value.(string)
	return
}

// MessageType returns the value of the "msgtype" key in the event content if
// it is present and is a string.
func (event *Event) MessageType() (msgtype string, ok bool) {
	value, exists := event.Content["msgtype"]
	if !exists {
		return
	}
	msgtype, ok = value.(string)
	return
}

// TextMessage is the contents of a Matrix formated message event.
type TextMessage struct {
	MsgType       string `json:"msgtype"`
	Body          string `json:"body"`
	FormattedBody string `json:"formatted_body"`
	Format        string `json:"format"`
}

// ThumbnailInfo contains info about an thumbnail image - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-image
type ThumbnailInfo struct {
	Height   uint   `json:"h,omitempty"`
	Width    uint   `json:"w,omitempty"`
	Mimetype string `json:"mimetype,omitempty"`
	Size     uint   `json:"size,omitempty"`
}

// ImageInfo contains info about an image - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-image
type ImageInfo struct {
	Height        uint          `json:"h,omitempty"`
	Width         uint          `json:"w,omitempty"`
	Mimetype      string        `json:"mimetype,omitempty"`
	Size          uint          `json:"size,omitempty"`
	ThumbnailInfo ThumbnailInfo `json:"thumbnail_info,omitempty"`
	ThumbnailURL  string        `json:"thumbnail_url,omitempty"`
}

// VideoInfo contains info about a video - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-video
type VideoInfo struct {
	Mimetype      string        `json:"mimetype,omitempty"`
	ThumbnailInfo ThumbnailInfo `json:"thumbnail_info"`
	ThumbnailURL  string        `json:"thumbnail_url,omitempty"`
	Height        uint          `json:"h,omitempty"`
	Width         uint          `json:"w,omitempty"`
	Duration      uint          `json:"duration,omitempty"`
	Size          uint          `json:"size,omitempty"`
}

// VideoMessage is an m.video  - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-video
type VideoMessage struct {
	MsgType string    `json:"msgtype"`
	Body    string    `json:"body"`
	URL     string    `json:"url"`
	Info    VideoInfo `json:"info"`
}

// ImageMessage is an m.image event
type ImageMessage struct {
	MsgType string    `json:"msgtype"`
	Body    string    `json:"body"`
	URL     string    `json:"url"`
	Info    ImageInfo `json:"info"`
}

// An HTMLMessage is the contents of a Matrix HTML formated message event.
type HTMLMessage struct {
	Body          string `json:"body"`
	MsgType       string `json:"msgtype"`
	Format        string `json:"format"`
	FormattedBody string `json:"formatted_body"`
}

// FileInfo contains info about an file - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-file
type FileInfo struct {
	Mimetype string `json:"mimetype,omitempty"`
	Size     uint   `json:"size,omitempty"` //filesize in bytes
}

// FileMessage is an m.file event - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-file
type FileMessage struct {
	MsgType       string    `json:"msgtype"`
	Body          string    `json:"body"`
	URL           string    `json:"url"`
	Filename      string    `json:"filename"`
	Info          FileInfo  `json:"info,omitempty"`
	ThumbnailURL  string    `json:"thumbnail_url,omitempty"`
	ThumbnailInfo ImageInfo `json:"thumbnail_info,omitempty"`
}

// LocationMessage is an m.location event - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-location
type LocationMessage struct {
	MsgType       string    `json:"msgtype"`
	Body          string    `json:"body"`
	GeoURI        string    `json:"geo_uri"`
	ThumbnailURL  string    `json:"thumbnail_url,omitempty"`
	ThumbnailInfo ImageInfo `json:"thumbnail_info,omitempty"`
}

// AudioInfo contains info about an file - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-audio
type AudioInfo struct {
	Mimetype string `json:"mimetype,omitempty"`
	Size     uint   `json:"size,omitempty"`     //filesize in bytes
	Duration uint   `json:"duration,omitempty"` //audio duration in ms
}

// AudioMessage is an m.audio event - http://matrix.org/docs/spec/client_server/r0.2.0.html#m-audio
type AudioMessage struct {
	MsgType string    `json:"msgtype"`
	Body    string    `json:"body"`
	URL     string    `json:"url"`
	Info    AudioInfo `json:"info,omitempty"`
}

// PowerLevels is and m.room.power_levels event - https://matrix.org/docs/spec/client_server/r0.6.1#m-room-power-levels
type PowerLevels struct {
	Ban           int                     `json:"ban"`
	Invite        int                     `json:"invite"`
	Kick          int                     `json:"kick"`
	Redact        int                     `json:"redact"`
	Events        map[string]int          `json:"events"`
	Users         map[string]int          `json:"users"`
	Notifications NotificationPowerLevels `json:"notifications"`
	EventsDefault int                     `json:"events_default"`
	StateDefault  int                     `json:"state_default"`
	UsersDefault  int                     `json:"users_default"`
}

type NotificationPowerLevels struct {
	Room int `json:"room"`
}

type RespMembers struct {
	Chunk []Event `json:"chunk"`
}

type PushRuleType string

const (
	OverrideRule  PushRuleType = "override"
	ContentRule   PushRuleType = "content"
	RoomRule      PushRuleType = "room"
	SenderRule    PushRuleType = "sender"
	UnderrideRule PushRuleType = "underride"
)

// PushActionType is the type of a PushAction
type PushActionType string

// The allowed push action types as specified in spec section 11.12.1.4.1.
const (
	ActionNotify     PushActionType = "notify"
	ActionDontNotify PushActionType = "dont_notify"
	ActionCoalesce   PushActionType = "coalesce"
	ActionSetTweak   PushActionType = "set_tweak"
)

// PushCondKind is the type of a push condition.
type PushCondKind string

// The allowed push condition kinds as specified in https://spec.matrix.org/v1.2/client-server-api/#conditions-1
const (
	KindEventMatch            PushCondKind = "event_match"
	KindContainsDisplayName   PushCondKind = "contains_display_name"
	KindRoomMemberCount       PushCondKind = "room_member_count"
	KindEventPropertyIs       PushCondKind = "event_property_is"
	KindEventPropertyContains PushCondKind = "event_property_contains"

	// MSC3664: https://github.com/matrix-org/matrix-spec-proposals/pull/3664

	KindRelatedEventMatch         PushCondKind = "related_event_match"
	KindUnstableRelatedEventMatch PushCondKind = "im.nheko.msc3664.related_event_match"
)

type RelationType string

const (
	RelReplace    RelationType = "m.replace"
	RelReference  RelationType = "m.reference"
	RelAnnotation RelationType = "m.annotation"
	RelThread     RelationType = "m.thread"
)

// any is an alias for interface{} and is equivalent to interface{} in all ways.
type any = interface{}

// PushCondition wraps a condition that is required for a specific PushRule to be used.
type PushCondition struct {
	// The type of the condition.
	Kind PushCondKind `json:"kind"`
	// The dot-separated field of the event to match. Only applicable if kind is EventMatch.
	Key string `json:"key,omitempty"`
	// The glob-style pattern to match the field against. Only applicable if kind is EventMatch.
	Pattern string `json:"pattern,omitempty"`
	// The exact value to match the field against. Only applicable if kind is EventPropertyIs or EventPropertyContains.
	Value any `json:"value,omitempty"`
	// The condition that needs to be fulfilled for RoomMemberCount-type conditions.
	// A decimal integer optionally prefixed by ==, <, >, >= or <=. Prefix "==" is assumed if no prefix found.
	MemberCountCondition string `json:"is,omitempty"`

	// The relation type for related_event_match from MSC3664
	RelType RelationType `json:"rel_type,omitempty"`
}

var htmlRegex = regexp.MustCompile("<[^<]+?>")

// GetHTMLMessage returns an HTMLMessage with the body set to a stripped version of the provided HTML, in addition
// to the provided HTML.
func GetHTMLMessage(msgtype, htmlText string) HTMLMessage {
	return HTMLMessage{
		Body:          html.UnescapeString(htmlRegex.ReplaceAllLiteralString(htmlText, "")),
		MsgType:       msgtype,
		Format:        "org.matrix.custom.html",
		FormattedBody: htmlText,
	}
}

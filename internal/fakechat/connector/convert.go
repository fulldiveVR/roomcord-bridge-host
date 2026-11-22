// roomcord-bridge-host - A host process for the mautrix bridgev2 network connectors.
// Copyright (C) 2026 The roomcord-bridge-host authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, version 3 of the License.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.
//
// SPDX-License-Identifier: AGPL-3.0-only

package connector

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"
	"unicode/utf8"

	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/database"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"maunium.net/go/mautrix/event"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// convertMessage maps one FakeChat message to Matrix content, the way the real
// connectors do: media is downloaded from the network and uploaded through the
// intent, a contact becomes a vCard file, a location an m.location.
func (c *Client) convertMessage(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, m *proto.Message) (*bridgev2.ConvertedMessage, error) {
	part := c.convertPart(ctx, portal, intent, m)
	out := &bridgev2.ConvertedMessage{Parts: []*bridgev2.ConvertedMessagePart{part}}
	if m.ReplyTo != "" {
		out.ReplyTo = &networkid.MessageOptionalPartID{MessageID: networkid.MessageID(m.ReplyTo)}
	}
	return out, nil
}

func textContent(text string, entities []proto.Entity) *event.MessageEventContent {
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: text}
	if formatted := entitiesToHTML(text, entities); formatted != "" {
		content.Format = event.FormatHTML
		content.FormattedBody = formatted
	}
	return content
}

func (c *Client) convertPart(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, m *proto.Message) *bridgev2.ConvertedMessagePart {
	part := &bridgev2.ConvertedMessagePart{Type: event.EventMessage}
	switch m.Kind {
	case proto.KindText, "":
		part.Content = textContent(m.Text, m.Entities)
	case proto.KindLocation:
		loc := m.Location
		if loc == nil {
			loc = &proto.Location{}
		}
		name := loc.Name
		if name == "" {
			name = fmt.Sprintf("%.5f, %.5f", loc.Lat, loc.Lon)
		}
		mapURL := fmt.Sprintf("https://maps.google.com/?q=%.5f,%.5f", loc.Lat, loc.Lon)
		part.Content = &event.MessageEventContent{
			MsgType:       event.MsgLocation,
			Body:          fmt.Sprintf("Location: %s\n%s", name, mapURL),
			Format:        event.FormatHTML,
			FormattedBody: fmt.Sprintf(`Location: <a href="%s">%s</a>`, html.EscapeString(mapURL), html.EscapeString(name)),
			GeoURI:        fmt.Sprintf("geo:%.5f,%.5f", loc.Lat, loc.Lon),
		}
	case proto.KindContact:
		ct := m.Contact
		if ct == nil {
			ct = &proto.Contact{Name: "Contact"}
		}
		vcard := fmt.Sprintf("BEGIN:VCARD\r\nVERSION:3.0\r\nFN:%s\r\nTEL;TYPE=CELL:%s\r\nEND:VCARD\r\n", ct.Name, ct.Phone)
		name := ct.Name + ".vcf"
		uri, file, err := intent.UploadMedia(ctx, portal.MXID, []byte(vcard), name, "text/vcard")
		if err != nil {
			part.Content = failedMedia(err)
			break
		}
		part.Content = &event.MessageEventContent{MsgType: event.MsgFile, Body: name, FileName: name, URL: uri, File: file,
			Info: &event.FileInfo{MimeType: "text/vcard", Size: len(vcard)}}
	case proto.KindSystem:
		part.Content = &event.MessageEventContent{MsgType: event.MsgNotice, Body: systemText(m.System)}
	default:
		part.Content = c.mediaContent(ctx, portal, intent, m)
		if m.Kind == proto.KindSticker && part.Content.URL != "" {
			part.Type = event.EventSticker
			part.Content.MsgType = ""
		}
	}
	return part
}

func failedMedia(err error) *event.MessageEventContent {
	return &event.MessageEventContent{MsgType: event.MsgNotice, Body: "Failed to bridge media: " + err.Error()}
}

func (c *Client) mediaContent(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, m *proto.Message) *event.MessageEventContent {
	md := m.Media
	if md == nil {
		return &event.MessageEventContent{MsgType: event.MsgNotice, Body: "Unsupported FakeChat message"}
	}
	data, err := c.api.Download(ctx, md.ID)
	if err != nil {
		return failedMedia(err)
	}
	uri, file, err := intent.UploadMedia(ctx, portal.MXID, data, md.Name, md.Mime)
	if err != nil {
		return failedMedia(err)
	}
	content := &event.MessageEventContent{
		Body: md.Name, FileName: md.Name, URL: uri, File: file,
		Info: &event.FileInfo{MimeType: md.Mime, Size: len(data), Width: md.Width, Height: md.Height, Duration: md.DurationMs},
	}
	if m.Text != "" {
		content.Body = m.Text
		if formatted := entitiesToHTML(m.Text, m.Entities); formatted != "" {
			content.Format, content.FormattedBody = event.FormatHTML, formatted
		}
	}
	switch md.Kind {
	case proto.KindImage, proto.KindSticker:
		content.MsgType = event.MsgImage
	case proto.KindVideo:
		content.MsgType = event.MsgVideo
	case proto.KindVoice:
		content.MsgType = event.MsgAudio
		content.MSC3245Voice = &event.MSC3245Voice{}
		content.MSC1767Audio = &event.MSC1767Audio{Duration: md.DurationMs}
	case proto.KindAudio:
		content.MsgType = event.MsgAudio
		content.MSC1767Audio = &event.MSC1767Audio{Duration: md.DurationMs}
	default:
		content.MsgType = event.MsgFile
	}
	return content
}

func systemText(sys *proto.System) string {
	if sys == nil {
		return "System message"
	}
	switch sys.Action {
	case proto.SystemMemberJoined:
		return sys.UserID + " joined"
	case proto.SystemMemberLeft:
		return sys.UserID + " left"
	case proto.SystemRenamed:
		return "Group renamed to " + sys.Title
	}
	return "System message: " + sys.Action
}

func (c *Client) convertEdit(ctx context.Context, portal *bridgev2.Portal, intent bridgev2.MatrixAPI, existing []*database.Message, m *proto.Message) (*bridgev2.ConvertedEdit, error) {
	if len(existing) == 0 {
		return nil, fmt.Errorf("no bridged message to edit")
	}
	var content *event.MessageEventContent
	if m.Kind == proto.KindText || m.Kind == "" {
		content = textContent(m.Text, m.Entities)
	} else {
		content = &event.MessageEventContent{MsgType: event.MsgText, Body: m.Text}
	}
	part := &bridgev2.ConvertedMessagePart{Type: event.EventMessage, Content: content}
	return &bridgev2.ConvertedEdit{ModifiedParts: []*bridgev2.ConvertedEditPart{part.ToEditPart(existing[0])}}, nil
}

// entitiesToHTML renders the entities (rune offsets) as Matrix HTML. It returns
// "" when there is nothing to format.
func entitiesToHTML(text string, entities []proto.Entity) string {
	if len(entities) == 0 {
		return ""
	}
	runes := []rune(text)
	type mark struct {
		pos   int
		open  bool
		order int
		tag   string
	}
	var marks []mark
	for i, e := range entities {
		if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > len(runes) {
			continue
		}
		open, close := "", ""
		switch e.Type {
		case "bold":
			open, close = "<strong>", "</strong>"
		case "italic":
			open, close = "<em>", "</em>"
		case "strike":
			open, close = "<del>", "</del>"
		case "code":
			open, close = "<code>", "</code>"
		case "pre":
			open, close = "<pre><code>", "</code></pre>"
		case "link":
			open, close = `<a href="`+html.EscapeString(e.URL)+`">`, "</a>"
		default:
			continue
		}
		marks = append(marks, mark{e.Offset, true, i, open}, mark{e.Offset + e.Length, false, i, close})
	}
	if len(marks) == 0 {
		return ""
	}
	sort.SliceStable(marks, func(i, j int) bool {
		if marks[i].pos != marks[j].pos {
			return marks[i].pos < marks[j].pos
		}
		if marks[i].open != marks[j].open {
			return !marks[i].open
		}
		if marks[i].open {
			return marks[i].order < marks[j].order
		}
		return marks[i].order > marks[j].order
	})
	var b strings.Builder
	pos := 0
	for _, mk := range marks {
		b.WriteString(strings.ReplaceAll(html.EscapeString(string(runes[pos:mk.pos])), "\n", "<br>"))
		pos = mk.pos
		b.WriteString(mk.tag)
	}
	b.WriteString(strings.ReplaceAll(html.EscapeString(string(runes[pos:])), "\n", "<br>"))
	return b.String()
}

// FetchMessages serves bridgev2 backfill from the server history: forward from
// the newest bridged message (or the newest page), backward from a cursor.
func (c *Client) FetchMessages(ctx context.Context, params bridgev2.FetchMessagesParams) (*bridgev2.FetchMessagesResponse, error) {
	chatID := string(params.Portal.ID)
	limit := min(max(params.Count, 1), 200)
	var page proto.MessagePage
	var err error
	switch {
	case params.Forward && params.AnchorMessage != nil:
		page, err = c.api.Messages(ctx, chatID, "", string(params.AnchorMessage.ID), limit)
		if apiCode(err) == proto.ErrNotFound {
			page, err = c.api.Messages(ctx, chatID, "", "", limit)
		}
	case params.Forward:
		page, err = c.api.Messages(ctx, chatID, "", "", limit)
	default:
		before := string(params.Cursor)
		if before == "" && params.AnchorMessage != nil {
			before = string(params.AnchorMessage.ID)
		}
		page, err = c.api.Messages(ctx, chatID, before, "", limit)
	}
	if err != nil {
		return nil, err
	}
	resp := &bridgev2.FetchMessagesResponse{Forward: params.Forward, HasMore: page.HasMore}
	if len(page.Messages) > 0 {
		resp.Cursor = networkid.PaginationCursor(page.Messages[0].ID)
	}
	for i := range page.Messages {
		m := &page.Messages[i]
		if m.Deleted || m.Kind == proto.KindSystem {
			continue
		}
		sender := c.sender(m.SenderID)
		intent, ok := params.Portal.GetIntentFor(ctx, sender, c.login, bridgev2.RemoteEventMessage)
		if !ok {
			continue
		}
		conv, _ := c.convertMessage(ctx, params.Portal, intent, m)
		bm := &bridgev2.BackfillMessage{ConvertedMessage: conv, Sender: sender, ID: networkid.MessageID(m.ID), Timestamp: m.TS}
		for _, r := range m.Reactions {
			bm.Reactions = append(bm.Reactions, &bridgev2.BackfillReaction{
				Timestamp: r.At, Sender: c.sender(r.UserID), EmojiID: networkid.EmojiID(r.Emoji), Emoji: r.Emoji,
			})
		}
		resp.Messages = append(resp.Messages, bm)
	}
	return resp, nil
}

// ---- outbound (Matrix to FakeChat)

func (c *Client) notLoggedIn(err error) error {
	if IsUnauthorized(err) {
		c.loggedIn.Store(false)
		return bridgev2.ErrNotLoggedIn
	}
	return err
}

func (c *Client) HandleMatrixMessage(ctx context.Context, msg *bridgev2.MatrixMessage) (*bridgev2.MatrixMessageResponse, error) {
	content := msg.Content
	req := proto.SendMessage{ClientID: string(msg.Event.ID)}
	if msg.ReplyTo != nil {
		req.ReplyTo = string(msg.ReplyTo.ID)
	}
	isSticker := msg.Event.Type == event.EventSticker
	switch {
	case isSticker || content.MsgType == event.MsgImage || content.MsgType == event.MsgVideo ||
		content.MsgType == event.MsgAudio || content.MsgType == event.MsgFile:
		data, err := msg.Portal.Bridge.Bot.DownloadMedia(ctx, content.URL, content.File)
		if err != nil {
			return nil, fmt.Errorf("download media from the host: %w", err)
		}
		name := content.FileName
		if name == "" {
			name = content.Body
		}
		mime := ""
		info := UploadInfo{}
		if content.Info != nil {
			mime = content.Info.MimeType
			info.Width, info.Height, info.DurationMs = content.Info.Width, content.Info.Height, content.Info.Duration
		}
		switch {
		case isSticker:
			info.Kind = proto.KindSticker
		case content.MSC3245Voice != nil:
			info.Kind = proto.KindVoice
		}
		md, err := c.api.Upload(ctx, name, mime, data, info)
		if err != nil {
			return nil, c.notLoggedIn(err)
		}
		req.MediaID = md.ID
		req.Kind = info.Kind
		if content.FileName != "" && content.Body != content.FileName {
			req.Text = content.Body
		}
	case content.MsgType == event.MsgLocation:
		var lat, lon float64
		if _, err := fmt.Sscanf(strings.TrimPrefix(content.GeoURI, "geo:"), "%f,%f", &lat, &lon); err != nil {
			return nil, fmt.Errorf("invalid geo URI")
		}
		req.Location = &proto.Location{Lat: lat, Lon: lon, Name: content.Body}
	case content.MsgType == event.MsgEmote:
		req.Text = "* " + content.Body
	default:
		req.Text = content.Body
	}
	if req.MediaID == "" && req.Location == nil && strings.TrimSpace(req.Text) == "" {
		return nil, fmt.Errorf("empty message")
	}
	if utf8.RuneCountInString(req.Text) > roomFeatures().MaxTextLength {
		return nil, fmt.Errorf("text is too long")
	}
	m, err := c.api.Send(ctx, string(msg.Portal.ID), req)
	if err != nil {
		return nil, c.notLoggedIn(err)
	}
	return &bridgev2.MatrixMessageResponse{DB: &database.Message{ID: networkid.MessageID(m.ID), SenderID: networkid.UserID(c.self), Timestamp: m.TS}}, nil
}

func (c *Client) HandleMatrixEdit(ctx context.Context, msg *bridgev2.MatrixEdit) error {
	_, err := c.api.Edit(ctx, string(msg.Portal.ID), string(msg.EditTarget.ID), proto.EditMessage{Text: msg.Content.Body})
	return c.notLoggedIn(err)
}

func (c *Client) HandleMatrixMessageRemove(ctx context.Context, msg *bridgev2.MatrixMessageRemove) error {
	return c.notLoggedIn(c.api.Delete(ctx, string(msg.Portal.ID), string(msg.TargetMessage.ID)))
}

func (c *Client) PreHandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (bridgev2.MatrixReactionPreResponse, error) {
	emoji := msg.Content.RelatesTo.Key
	return bridgev2.MatrixReactionPreResponse{SenderID: networkid.UserID(c.self), EmojiID: networkid.EmojiID(emoji), Emoji: emoji}, nil
}

func (c *Client) HandleMatrixReaction(ctx context.Context, msg *bridgev2.MatrixReaction) (*database.Reaction, error) {
	emoji := msg.Content.RelatesTo.Key
	if err := c.api.React(ctx, string(msg.Portal.ID), string(msg.TargetMessage.ID), emoji, true); err != nil {
		return nil, c.notLoggedIn(err)
	}
	return &database.Reaction{}, nil
}

func (c *Client) HandleMatrixReactionRemove(ctx context.Context, msg *bridgev2.MatrixReactionRemove) error {
	r := msg.TargetReaction
	emoji := r.Emoji
	if emoji == "" {
		emoji = string(r.EmojiID)
	}
	return c.notLoggedIn(c.api.React(ctx, string(msg.Portal.ID), string(r.MessageID), emoji, false))
}

func (c *Client) HandleMatrixReadReceipt(ctx context.Context, msg *bridgev2.MatrixReadReceipt) error {
	target := ""
	if msg.ExactMessage != nil {
		target = string(msg.ExactMessage.ID)
	}
	return c.notLoggedIn(c.api.Read(ctx, string(msg.Portal.ID), target))
}

func (c *Client) HandleMatrixTyping(ctx context.Context, msg *bridgev2.MatrixTyping) error {
	return c.notLoggedIn(c.api.Typing(ctx, string(msg.Portal.ID), msg.IsTyping))
}

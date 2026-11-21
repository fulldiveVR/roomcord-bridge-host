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

package api

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakenet"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/wire"
)

type seedResult struct {
	SendTarget struct {
		ConversationID string `json:"conversationId"`
	} `json:"sendTarget"`
	Media struct {
		ConversationID string `json:"conversationId"`
		MessageID      string `json:"messageId"`
		AttachmentID   string `json:"attachmentId"`
	} `json:"media"`
}

func (h *harness) download(key, convID, msgID, attID string) (int, http.Header, []byte) {
	h.t.Helper()
	req, _ := http.NewRequest("GET", h.ts.URL+"/accounts/"+key+"/media/"+msgID+"/"+attID+"?conversationId="+convID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header, raw
}

func (h *harness) upload(key, fileName, mimeType string, data []byte) string {
	h.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part := textproto.MIMEHeader{}
	part.Set("Content-Disposition", `form-data; name="file"; filename="`+fileName+`"`)
	part.Set("Content-Type", mimeType)
	pw, _ := mw.CreatePart(part)
	_, _ = pw.Write(data)
	_ = mw.Close()
	req, _ := http.NewRequest("POST", h.ts.URL+"/accounts/"+key+"/media", &body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		h.t.Fatalf("upload: %d %s", resp.StatusCode, raw)
	}
	var out struct {
		UploadRef string `json:"uploadRef"`
	}
	_ = json.Unmarshal(raw, &out)
	return out.UploadRef
}

func TestMediaRoundTrip(t *testing.T) {
	h := newHarness(t)
	key, _ := h.login("u1", "+15550001111")
	var seed seedResult
	h.doJSON("POST", "/fake/seed", map[string]string{"loginId": key}, &seed, 200)
	if seed.Media.AttachmentID == "" || seed.SendTarget.ConversationID != fakenet.SeedGroup {
		t.Fatalf("seed %+v", seed)
	}
	var again seedResult
	h.doJSON("POST", "/fake/seed", map[string]string{"loginId": key}, &again, 200)
	if again != seed {
		t.Fatalf("second seed %+v, first %+v", again, seed)
	}
	items := waitHistory(t, h, key, fakenet.SeedGroup, fakenet.SeedGroupSize)
	if len(items) != fakenet.SeedGroupSize {
		t.Fatalf("seeded history has %d messages", len(items))
	}

	code, hdr, data := h.download(key, seed.Media.ConversationID, seed.Media.MessageID, seed.Media.AttachmentID)
	if code != 200 || !bytes.Equal(data, fakenet.SeedPNG) || hdr.Get("Content-Type") != "image/png" {
		t.Fatalf("seed media: %d %q %d bytes", code, hdr.Get("Content-Type"), len(data))
	}
	if _, params, err := mime.ParseMediaType(hdr.Get("Content-Disposition")); err != nil || params["filename"] != "pixel.png" {
		t.Fatalf("content-disposition %q", hdr.Get("Content-Disposition"))
	}

	payload := []byte("hello media \x00\x01\x02")
	ref := h.upload(key, "nötes.txt", "text/plain", payload)
	body := map[string]any{"conversationId": fakenet.SeedGroup, "clientMessageId": "media-1", "text": "caption", "uploadRefs": []string{ref}}
	var sent wire.SendResponse
	h.doJSON("POST", "/accounts/"+key+"/send", body, &sent, 200)
	if len(sent.Attachments) != 1 || sent.Attachments[0].ExternalAttachmentID != ref || sent.Attachments[0].MimeType != "text/plain" || sent.Attachments[0].Kind != "file" {
		t.Fatalf("send response %+v", sent)
	}
	var repeat wire.SendResponse
	h.doJSON("POST", "/accounts/"+key+"/send", body, &repeat, 200)
	if repeat.ExternalMessageID != sent.ExternalMessageID || len(repeat.Attachments) != 1 || repeat.Attachments[0].ExternalAttachmentID != sent.Attachments[0].ExternalAttachmentID {
		t.Fatalf("repeat %+v, first %+v", repeat, sent)
	}
	code, hdr, data = h.download(key, fakenet.SeedGroup, sent.ExternalMessageID, sent.Attachments[0].ExternalAttachmentID)
	if code != 200 || !bytes.Equal(data, payload) || hdr.Get("Content-Type") != "text/plain" {
		t.Fatalf("sent media: %d %q %q", code, hdr.Get("Content-Type"), data)
	}
	if _, params, _ := mime.ParseMediaType(hdr.Get("Content-Disposition")); params["filename"] != "nötes.txt" {
		t.Fatalf("utf-8 file name %q", hdr.Get("Content-Disposition"))
	}

	keyB, _ := h.login("u2", "+15550002222")
	if code, _, _ := h.download(keyB, fakenet.SeedGroup, sent.ExternalMessageID, ref); code != 404 {
		t.Fatalf("other login downloaded the file: %d", code)
	}
}

func TestRevokeHoldsEventsUntilRelogin(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	cp := next(t, conn)
	client.Incoming("dm-alice", "alice", "m1", "before")
	until(t, conn, wire.EventMessageUpsert)

	h.doJSON("POST", "/fake/revoke", map[string]string{"loginId": key}, nil, 204)
	f, _ := until(t, conn, wire.EventAccountStatus)
	if st := decodeP[wire.AccountStatusPayload](t, f); st.Status != "needs_reauth" {
		t.Fatalf("status %+v", st)
	}
	code, raw := h.do("GET", "/accounts/"+key+"/conversations", nil)
	if code != 409 || !strings.Contains(string(raw), "session_revoked") {
		t.Fatalf("conversations after revoke: %d %s", code, raw)
	}
	code, _ = h.do("POST", "/accounts/"+key+"/send", map[string]any{"conversationId": "dm-alice", "clientMessageId": "x", "text": "x"})
	if code != 409 {
		t.Fatalf("send after revoke: %d", code)
	}
	var health wire.Health
	h.doJSON("GET", "/accounts/"+key+"/health", nil, &health, 200)
	if health.State != "needs_reauth" {
		t.Fatalf("health %+v", health)
	}
	h.doJSON("POST", "/fake/inject", map[string]any{"loginId": key, "kind": "message", "conversationId": "dm-alice", "messageId": "m2", "text": "while revoked"}, nil, 204)

	h.login("u1", "+15550001111")
	h.doJSON("GET", "/accounts/"+key+"/health", nil, &health, 200)
	if health.State != "connected" {
		t.Fatalf("health after re-login %+v", health)
	}
	cursor := cp.Seq
	resumed := h.dial(key, &cursor)
	for range 50 {
		f := next(t, resumed)
		if f.Type == wire.EventAccountStatus {
			if st := decodeP[wire.AccountStatusPayload](t, f); st.Status != "connected" {
				t.Fatalf("resumed stream replayed a stale status %+v", st)
			}
		}
		if f.Type == wire.EventMessageUpsert && decodeP[wire.MessageUpsertPayload](t, f).Message.ExternalMessageID == "m2" {
			return
		}
	}
	t.Fatal("message sent while revoked never arrived")
}

func TestReactionListAndSelfReceipt(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	next(t, conn)
	base := time.Now().Add(-time.Minute)
	client.IncomingAt("grp", "alice", "m1", "one", nil, base)
	client.IncomingAt("grp", "bob", "m2", "two", nil, base.Add(time.Second))
	client.IncomingAt("grp", "alice", "m3", "three", nil, base.Add(2*time.Second))
	waitHistory(t, h, key, "grp", 3)

	client.React("grp", "alice", "m1", "👍", false)
	until(t, conn, wire.EventReactionUpsert)
	client.React("grp", "bob", "m1", "🎉", false)
	f, _ := until(t, conn, wire.EventReactionUpsert)
	if r := decodeP[wire.ReactionPayload](t, f); len(r.Reactions) != 2 {
		t.Fatalf("full list after second reaction %+v", r.Reactions)
	}
	client.React("grp", "alice", "m1", "👍", true)
	f, _ = until(t, conn, wire.EventReactionDelete)
	if r := decodeP[wire.ReactionPayload](t, f); len(r.Reactions) != 1 || r.Reactions[0].SenderExternalID != "bob" {
		t.Fatalf("full list after removal %+v", r.Reactions)
	}

	client.Read("grp", string(client.SelfID()), "m1")
	f, _ = until(t, conn, wire.EventReadReceipt)
	rr := decodeP[wire.ReadReceiptPayload](t, f)
	if !rr.IsSelf || rr.UnreadCount == nil || *rr.UnreadCount != 2 || rr.LastReadExternalMessageID != "m1" {
		t.Fatalf("self receipt %+v unread %v", rr, rr.UnreadCount)
	}
	var convs wire.Page[wire.Conversation]
	h.doJSON("GET", "/accounts/"+key+"/conversations", nil, &convs, 200)
	if c := convs.Items[0]; c.LastReadExternalMessageID == nil || *c.LastReadExternalMessageID != "m1" || c.UnreadCount == nil || *c.UnreadCount != 2 {
		t.Fatalf("conversation after self receipt %+v", c)
	}
}

func TestDeleteDataKeepsSession(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	client.Incoming("dm-alice", "alice", "m1", "hello")
	waitHistory(t, h, key, "dm-alice", 1)
	h.doJSON("DELETE", "/accounts/"+key+"/data", nil, nil, 204)
	if lo := h.fake.LoggedOut(); len(lo) != 0 {
		t.Fatalf("data deletion logged out: %v", lo)
	}
	var health wire.Health
	h.doJSON("GET", "/accounts/"+key+"/health", nil, &health, 200)
	if health.State != "connected" {
		t.Fatalf("health after data deletion %+v", health)
	}
	var convs wire.Page[wire.Conversation]
	h.doJSON("GET", "/accounts/"+key+"/conversations", nil, &convs, 200)
	if len(convs.Items) != 0 {
		t.Fatalf("conversations after data deletion %+v", convs.Items)
	}
	client.Incoming("dm-alice", "alice", "m2", "after")
	waitHistory(t, h, key, "dm-alice", 1)
}

func TestRedeliverRepeatsNewestMessage(t *testing.T) {
	h := newHarness(t)
	key, client := h.login("u1", "+15550001111")
	conn := h.dial(key, nil)
	next(t, conn)
	client.Incoming("dm-alice", "alice", "m1", "hello")
	first, _ := until(t, conn, wire.EventMessageUpsert)
	h.doJSON("POST", "/fake/redeliver", map[string]string{"loginId": key}, nil, 200)
	second, _ := until(t, conn, wire.EventMessageUpsert)
	if second.Seq <= first.Seq || decodeP[wire.MessageUpsertPayload](t, second).Message.ExternalMessageID != "m1" {
		t.Fatalf("redelivered %+v after %+v", second, first)
	}
	waitHistory(t, h, key, "dm-alice", 1)
	time.Sleep(10 * time.Millisecond)
}

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

// Package server is the FakeChat network: a separate HTTP and WebSocket server
// with its own accounts, logins, chats, message store, media and event streams.
// The bridge connector talks to it over the network like to a real service.
// State is in memory. See README.md, section FakeChat.
package server

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image/color"
	mrand "math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/media"
	"github.com/fulldiveVR/roomcord-bridge-host/internal/fakechat/proto"
)

// Fixed credentials of the test accounts and login flows.
const (
	SMSCode        = "123456"
	CodeAttempts   = 3
	DemoAccount    = "demo"
	DemoPassword   = "demo-pass"
	DemoPhone      = "+15550100001"
	SecureAccount  = "secure"
	SecurePassword = "secure-pass"
	SecurePhone    = "+15550100002"
	Secure2FA      = "654321"
	// InvalidPhonePrefix marks numbers that the server refuses as invalid.
	InvalidPhonePrefix = "+999"
	MaxUpload          = 64 << 20

	WebSessionCookie = "fc_session"
	WebUserCookie    = "fc_user"
)

// Seed chat ids. Every new account gets these chats.
const (
	ChatAlice   = "dm-alice"
	ChatBob     = "dm-bob"
	ChatProject = "grp-project"
	ChatFamily  = "grp-family"
	// ProjectHistory is larger than the bridge's initial backfill (50).
	ProjectHistory = 60
)

type Config struct {
	// ControlToken protects /control/*. Required.
	ControlToken string
	// Seed makes ids, tokens and message timestamps deterministic when Deterministic is set.
	Seed          int64
	Deterministic bool
	// PublicURL is the base URL in QR data and page links. Empty: taken from each request.
	PublicURL string
	// QRTTL is the lifetime of one QR code. Default 60 s.
	QRTTL time.Duration
	Log   zerolog.Logger
}

type contact struct {
	id, name, phone string
	color           color.RGBA
}

var contacts = []contact{
	{"alice", "Alice Andersen", "+15550200001", color.RGBA{R: 214, G: 69, B: 65, A: 255}},
	{"bob", "Bob Brown", "+15550200002", color.RGBA{R: 52, G: 120, B: 246, A: 255}},
	{"carol", "Carol Chen", "+15550200003", color.RGBA{R: 46, G: 160, B: 67, A: 255}},
	{"dave", "Dave Diaz", "+15550200004", color.RGBA{R: 191, G: 135, B: 0, A: 255}},
	{"erin", "Erin Evans", "+15550200005", color.RGBA{R: 137, G: 87, B: 229, A: 255}},
}

type eventRec struct {
	evt    proto.Event
	origin string // session token that caused it; its own stream skips it
}

type chat struct {
	proto.Chat
	messages []*proto.Message
	byID     map[string]*proto.Message
}

type account struct {
	proto.Account
	password  string
	twoFactor string
	chats     map[string]*chat
	events    []eventRec
	notify    chan struct{}
	sent      []proto.SentRecord
	clientIDs map[string]string
	streams   map[*stream]struct{}
}

type session struct {
	token     string
	accountID string
}

type qrCode struct {
	proto.PendingQR
	approvedBy string
	token      string
}

type challenge struct {
	id        string
	accountID string
	attempts  int
	kind      string // phone | 2fa
}

type storedMedia struct {
	meta proto.Media
	data func() []byte
}

type fault struct {
	proto.Fault
}

type stream struct {
	token string
	kick  chan int // close code
}

// Server holds the whole network state behind one lock.
type Server struct {
	cfg Config
	log zerolog.Logger

	mu      sync.Mutex
	rng     *mrand.Rand
	clock   time.Time
	counter int
	// epoch is part of every id without a seed, so a restarted or reset server
	// never hands out an id that a bridge already stored from an earlier run.
	epoch       string
	accounts    map[string]*account
	sessions    map[string]*session // access tokens
	webSessions map[string]string   // web cookie value -> account id
	qrs         map[string]*qrCode
	challenges  map[string]*challenge
	media       map[string]*storedMedia
	faults      []*fault
}

func New(cfg Config) *Server {
	if cfg.QRTTL <= 0 {
		cfg.QRTTL = 60 * time.Second
	}
	s := &Server{cfg: cfg, log: cfg.Log}
	s.reset(cfg.Seed)
	return s
}

// Reset drops every account, session, message and fault and seeds again.
func (s *Server) Reset(seed int64) {
	s.mu.Lock()
	streams := s.allStreamsLocked()
	s.reset(seed)
	s.mu.Unlock()
	for _, st := range streams {
		kick(st, proto.CloseLoggedOut)
	}
}

func (s *Server) reset(seed int64) {
	s.cfg.Seed = seed
	s.rng = mrand.New(mrand.NewPCG(uint64(seed), 0xfa4ec4a7))
	s.clock = time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC).Add(time.Duration(seed) * time.Second)
	s.counter = 0
	s.epoch = ""
	if !s.cfg.Deterministic {
		s.epoch = strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	s.accounts = map[string]*account{}
	s.sessions = map[string]*session{}
	s.webSessions = map[string]string{}
	s.qrs = map[string]*qrCode{}
	s.challenges = map[string]*challenge{}
	s.media = map[string]*storedMedia{}
	s.faults = nil
	for _, name := range media.Names() {
		e, _ := media.Get(name)
		s.media[catalogID(name)] = &storedMedia{meta: e.Meta(catalogID(name)), data: e.Data}
	}
	for _, c := range contacts {
		data := media.Avatar(c.color)
		id := "avatar-" + c.id
		s.media[id] = &storedMedia{meta: proto.Media{ID: id, Kind: proto.KindImage, Name: c.id + ".png", Mime: "image/png", Size: int64(len(data)), Width: 96, Height: 96},
			data: func() []byte { return data }}
	}
	s.createAccountLocked(proto.CreateAccount{ID: DemoAccount, DisplayName: "Demo User", Phone: DemoPhone, Username: DemoAccount, Password: DemoPassword})
	s.createAccountLocked(proto.CreateAccount{ID: SecureAccount, DisplayName: "Secure User", Phone: SecurePhone, Username: SecureAccount, Password: SecurePassword, TwoFactorCode: Secure2FA})
}

func catalogID(name string) string { return "cat-" + name }

// now returns the time of a new message or event. With a seed it is a virtual
// clock that moves one second per call.
func (s *Server) now() time.Time {
	if s.cfg.Deterministic {
		s.clock = s.clock.Add(time.Second)
		return s.clock
	}
	return time.Now().UTC().Truncate(time.Millisecond)
}

func (s *Server) nextID(prefix string) string {
	s.counter++
	if s.epoch != "" {
		return fmt.Sprintf("%s-%s-%06d", prefix, s.epoch, s.counter)
	}
	return fmt.Sprintf("%s-%06d", prefix, s.counter)
}

func (s *Server) secret(prefix string) string {
	var b [12]byte
	if s.cfg.Deterministic {
		for i := range b {
			b[i] = byte(s.rng.UintN(256))
		}
	} else {
		_, _ = rand.Read(b[:])
	}
	return prefix + hex.EncodeToString(b[:])
}

func isContact(id string) bool {
	for _, c := range contacts {
		if c.id == id {
			return true
		}
	}
	return false
}

func (s *Server) user(id string) (proto.User, bool) {
	for _, c := range contacts {
		if c.id == id {
			return proto.User{ID: c.id, Name: c.name, Phone: c.phone, AvatarMediaID: "avatar-" + c.id}, true
		}
	}
	if a := s.accounts[id]; a != nil {
		return proto.User{ID: a.ID, Name: a.DisplayName, Phone: a.Phone}, true
	}
	return proto.User{}, false
}

func (s *Server) createAccountLocked(req proto.CreateAccount) (*account, error) {
	if req.ID == "" {
		req.ID = s.nextID("acct")
	}
	if isContact(req.ID) || s.accounts[req.ID] != nil {
		return nil, fmt.Errorf("account id %q is taken", req.ID)
	}
	if strings.ContainsAny(req.ID, "/:?#% ") {
		return nil, fmt.Errorf("account id %q has a reserved character", req.ID)
	}
	if req.DisplayName == "" {
		req.DisplayName = "User " + req.ID
	}
	for _, a := range s.accounts {
		if req.Phone != "" && a.Phone == req.Phone {
			return nil, fmt.Errorf("phone %s is taken", req.Phone)
		}
		if req.Username != "" && a.Username == req.Username {
			return nil, fmt.Errorf("username %s is taken", req.Username)
		}
	}
	a := &account{
		Account:   proto.Account{ID: req.ID, DisplayName: req.DisplayName, Phone: req.Phone, Username: req.Username},
		password:  req.Password,
		twoFactor: req.TwoFactorCode,
		chats:     map[string]*chat{},
		notify:    make(chan struct{}),
		clientIDs: map[string]string{},
		streams:   map[*stream]struct{}{},
	}
	s.accounts[a.ID] = a
	s.seedChats(a)
	return a, nil
}

// seedChats creates the four seed chats with past messages. Seed history is not
// in the event stream: a client gets it by backfill.
func (s *Server) seedChats(a *account) {
	base := s.now().Add(-24 * time.Hour)
	if !s.cfg.Deterministic {
		base = time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	}
	add := func(c *chat, sender, text string, at time.Time) {
		m := &proto.Message{ID: s.nextID("msg"), ChatID: c.ID, SenderID: sender, TS: at, Kind: proto.KindText, Text: text}
		c.append(m)
	}
	mk := func(id, kind, title string, members ...string) *chat {
		c := &chat{Chat: proto.Chat{ID: id, Kind: kind, Title: title, Members: append([]string{a.ID}, members...)}, byID: map[string]*proto.Message{}}
		if kind == "dm" {
			c.AvatarMediaID = "avatar-" + members[0]
		}
		a.chats[id] = c
		return c
	}
	alice := mk(ChatAlice, "dm", "Alice Andersen", "alice")
	for i, t := range []string{"Hi! Are you there?", "I sent the notes yesterday.", "Did you get them?", "Let me know.", "Talk soon"} {
		sender := "alice"
		if i == 2 {
			sender = a.ID
			t = "Yes, thanks!"
		}
		add(alice, sender, t, base.Add(time.Duration(i)*time.Minute))
	}
	bob := mk(ChatBob, "dm", "Bob Brown", "bob")
	for i, t := range []string{"Lunch tomorrow?", "Sure, 12:30.", "See you there."} {
		sender := []string{"bob", a.ID, "bob"}[i]
		add(bob, sender, t, base.Add(time.Hour+time.Duration(i)*time.Minute))
	}
	project := mk(ChatProject, "group", "Project X", "alice", "bob", "carol")
	for i := 1; i <= ProjectHistory; i++ {
		sender := []string{"alice", "bob", "carol"}[i%3]
		add(project, sender, fmt.Sprintf("Project update %d", i), base.Add(2*time.Hour+time.Duration(i)*time.Minute))
	}
	family := mk(ChatFamily, "group", "Family", "dave", "erin")
	add(family, "dave", "Dinner on Sunday?", base.Add(4*time.Hour))
	add(family, "erin", "Yes!", base.Add(4*time.Hour+time.Minute))
}

func (c *chat) append(m *proto.Message) {
	c.messages = append(c.messages, m)
	c.byID[m.ID] = m
	c.LastMessageID = m.ID
	c.LastMessageAt = m.TS
}

func (c *chat) view() proto.Chat {
	out := c.Chat
	out.Members = append([]string(nil), c.Members...)
	return out
}

func (c *chat) isMember(id string) bool {
	for _, m := range c.Members {
		if m == id {
			return true
		}
	}
	return false
}

// emit appends an event to the account's stream and wakes its subscribers.
func (s *Server) emit(a *account, evt proto.Event, origin string) int64 {
	evt.Seq = int64(len(a.events) + 1)
	if evt.TS.IsZero() {
		evt.TS = s.now()
	}
	a.events = append(a.events, eventRec{evt: evt, origin: origin})
	close(a.notify)
	a.notify = make(chan struct{})
	return evt.Seq
}

func (s *Server) record(a *account, r proto.SentRecord) {
	r.N = len(a.sent) + 1
	r.At = s.now()
	a.sent = append(a.sent, r)
}

func (s *Server) allStreamsLocked() []*stream {
	var out []*stream
	for _, a := range s.accounts {
		for st := range a.streams {
			out = append(out, st)
		}
	}
	return out
}

func kick(st *stream, code int) {
	select {
	case st.kick <- code:
	default:
	}
}

// revoke ends every session of the account, also web sessions, and closes its streams.
func (s *Server) revokeLocked(a *account) []*stream {
	for tok, sess := range s.sessions {
		if sess.accountID == a.ID {
			delete(s.sessions, tok)
		}
	}
	for tok, id := range s.webSessions {
		if id == a.ID {
			delete(s.webSessions, tok)
		}
	}
	var out []*stream
	for st := range a.streams {
		out = append(out, st)
	}
	return out
}

func (s *Server) newSession(a *account) proto.Session {
	tok := s.secret("fct_")
	s.sessions[tok] = &session{token: tok, accountID: a.ID}
	return proto.Session{AccessToken: tok, Account: a.Account}
}

// takeFault consumes a configured fault that matches the scope.
func (s *Server) takeFault(scope string) *proto.Fault {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, f := range s.faults {
		if f.Scope == proto.ScopeAny || f.Scope == scope {
			out := f.Fault
			f.Count--
			if f.Count <= 0 {
				s.faults = append(s.faults[:i], s.faults[i+1:]...)
			}
			return &out
		}
	}
	return nil
}

func sortedChats(a *account) []*chat {
	out := make([]*chat, 0, len(a.chats))
	for _, c := range a.chats {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastMessageAt.Equal(out[j].LastMessageAt) {
			return out[i].LastMessageAt.After(out[j].LastMessageAt)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

package main

import (
	"testing"

	"github.com/go-resty/resty/v2"
	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"
)

func TestConfigureQRClientTypeUsesChrome(t *testing.T) {
	client := &whatsmeow.Client{}

	configureQRClientType(client)

	if client.QRClientType != whatsmeow.PairClientChrome {
		t.Fatalf("QRClientType = %q; want %q", client.QRClientType, whatsmeow.PairClientChrome)
	}
	if string(whatsmeow.PairClientChrome) != "1" {
		t.Fatalf("PairClientChrome = %q; want WhatsApp web QR suffix %q", whatsmeow.PairClientChrome, "1")
	}
}

func TestIsBenignSessionStateError(t *testing.T) {
	tests := []struct {
		name string
		err  string
		want bool
	}{
		{name: "already connected", err: "already connected", want: true},
		{name: "already logged in", err: "already logged in", want: true},
		{name: "disconnect no session", err: "no session", want: true},
		{name: "disconnect not logged in", err: "cannot disconnect because it is not logged in", want: true},
		{name: "connect failure", err: "failed to connect", want: false},
		{name: "empty", err: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBenignSessionStateError(tt.err); got != tt.want {
				t.Fatalf("isBenignSessionStateError(%q) = %v; want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestMarkPairSuccessPersistsAuthorizedState(t *testing.T) {
	const (
		userID = "pair-success-user"
		token  = "pair-success-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "old-qr", 0,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "old-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	jid := types.NewJID("8613536835892", types.DefaultUserServer)

	paired, err := mycli.markPairSuccess(jid)
	if err != nil {
		t.Fatalf("markPairSuccess: %v", err)
	}
	if !paired {
		t.Fatal("markPairSuccess ignored an active pair success")
	}

	var row struct {
		JID       string `db:"jid"`
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != jid.String() {
		t.Fatalf("jid = %q; want %q", row.JID, jid.String())
	}
	if row.QRCode != "" {
		t.Fatalf("qrcode = %q; want cleared", row.QRCode)
	}
	if row.Connected != 1 {
		t.Fatalf("connected = %d; want 1", row.Connected)
	}
	if !mycli.hasPairSuccess() {
		t.Fatal("PairSuccess should mark QR loop as paired")
	}

	cached, found := userinfocache.Get(token)
	if !found {
		t.Fatal("cached user info missing")
	}
	values := cached.(Values)
	if values.Get("Jid") != jid.String() {
		t.Fatalf("cached Jid = %q; want %q", values.Get("Jid"), jid.String())
	}
	if values.Get("Qrcode") != "" {
		t.Fatalf("cached Qrcode = %q; want cleared", values.Get("Qrcode"))
	}
}

func TestStaleQRCodeAndTimeoutIgnoredAfterPairSuccess(t *testing.T) {
	const (
		userID = "stale-qr-user"
		token  = "stale-qr-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "old-qr", 0,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "old-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	jid := types.NewJID("8613536835892", types.DefaultUserServer)
	paired, err := mycli.markPairSuccess(jid)
	if err != nil {
		t.Fatalf("markPairSuccess: %v", err)
	}
	if !paired {
		t.Fatal("markPairSuccess ignored an active pair success")
	}

	qrWebhookSent := false
	stored, err := mycli.storeQRCode("new-qr")
	if err != nil {
		t.Fatalf("storeQRCode: %v", err)
	}
	if stored {
		qrWebhookSent = true
	}
	if stored {
		t.Fatal("storeQRCode stored a stale QR after pair success")
	}
	if qrWebhookSent {
		t.Fatal("storeQRCode sent a stale QR webhook after pair success")
	}

	timeoutWebhookSent := false
	kill := make(chan bool, 1)
	setKillChannel(userID, kill)
	t.Cleanup(func() { deleteKillChannel(userID, kill) })
	timeoutResult, err := mycli.markQRTimeout(kill)
	if err != nil {
		t.Fatalf("markQRTimeout: %v", err)
	}
	if timeoutResult == qrTimeoutApplied {
		timeoutWebhookSent = true
	}
	if timeoutResult != qrTimeoutIgnored {
		t.Fatalf("markQRTimeout result = %v; want ignored after pair success", timeoutResult)
	}
	if timeoutWebhookSent {
		t.Fatal("markQRTimeout sent a stale timeout webhook after pair success")
	}

	var row struct {
		JID       string `db:"jid"`
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != jid.String() || row.QRCode != "" || row.Connected != 1 {
		t.Fatalf("row = {jid:%q qrcode:%q connected:%d}; want paired jid, cleared QR, connected=1", row.JID, row.QRCode, row.Connected)
	}
}

func TestRepeatedPairSuccessIsIgnored(t *testing.T) {
	const (
		userID = "repeated-pair-user"
		token  = "repeated-pair-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "old-qr", 0,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "old-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	firstJID := types.NewJID("8613536835892", types.DefaultUserServer)
	paired, err := mycli.markPairSuccess(firstJID)
	if err != nil {
		t.Fatalf("first markPairSuccess: %v", err)
	}
	if !paired {
		t.Fatal("first markPairSuccess should authorize pending login")
	}

	secondJID := types.NewJID("8611111111111", types.DefaultUserServer)
	paired, err = mycli.markPairSuccess(secondJID)
	if err != nil {
		t.Fatalf("second markPairSuccess: %v", err)
	}
	if paired {
		t.Fatal("repeated markPairSuccess should be ignored")
	}

	var row struct {
		JID       string `db:"jid"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != firstJID.String() || row.Connected != 1 {
		t.Fatalf("row = {jid:%q connected:%d}; want first jid and connected=1", row.JID, row.Connected)
	}
}

func TestQRChannelSuccessDoesNotAuthorizeWithoutJID(t *testing.T) {
	const (
		userID = "qr-success-user"
		token  = "qr-success-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "old-qr", 0,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "old-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	if err := mycli.markQRChannelSuccess(); err != nil {
		t.Fatalf("markQRChannelSuccess: %v", err)
	}

	var row struct {
		JID       string `db:"jid"`
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != "" {
		t.Fatalf("jid = %q; want unchanged empty jid", row.JID)
	}
	if row.QRCode != "" {
		t.Fatalf("qrcode = %q; want cleared", row.QRCode)
	}
	if row.Connected != 0 {
		t.Fatalf("connected = %d; QR channel success must not authorize without jid", row.Connected)
	}
}

func TestQRTimeoutStillCleansActiveLogin(t *testing.T) {
	const (
		userID = "qr-timeout-user"
		token  = "qr-timeout-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "old-qr", 1,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "old-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	kill := make(chan bool, 1)
	setKillChannel(userID, kill)
	t.Cleanup(func() { deleteKillChannel(userID, kill) })

	timeoutWebhookSent := false
	timeoutResult, err := mycli.markQRTimeout(kill)
	if err != nil {
		t.Fatalf("markQRTimeout: %v", err)
	}
	if timeoutResult == qrTimeoutApplied {
		timeoutWebhookSent = true
	}
	if timeoutResult != qrTimeoutApplied {
		t.Fatalf("markQRTimeout result = %v; want applied for active QR timeout", timeoutResult)
	}
	if !timeoutWebhookSent {
		t.Fatal("markQRTimeout did not send timeout webhook for active login")
	}

	var row struct {
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.QRCode != "" {
		t.Fatalf("qrcode = %q; want cleared", row.QRCode)
	}
	if row.Connected != 0 {
		t.Fatalf("connected = %d; want 0 after active QR timeout", row.Connected)
	}

	cached, found := userinfocache.Get(token)
	if !found {
		t.Fatal("cached user info missing")
	}
	if cached.(Values).Get("Qrcode") != "" {
		t.Fatalf("cached Qrcode = %q; want cleared", cached.(Values).Get("Qrcode"))
	}
}

func TestPairSuccessIgnoredAfterQRTimeoutWins(t *testing.T) {
	const (
		userID = "timeout-wins-user"
		token  = "timeout-wins-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "old-qr", 1,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "old-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	kill := make(chan bool, 1)
	setKillChannel(userID, kill)
	t.Cleanup(func() { deleteKillChannel(userID, kill) })

	timeoutResult, err := mycli.markQRTimeout(kill)
	if err != nil {
		t.Fatalf("markQRTimeout: %v", err)
	}
	if timeoutResult != qrTimeoutApplied {
		t.Fatalf("markQRTimeout result = %v; want applied", timeoutResult)
	}

	jid := types.NewJID("8613536835892", types.DefaultUserServer)
	paired, err := mycli.markPairSuccess(jid)
	if err != nil {
		t.Fatalf("markPairSuccess after timeout: %v", err)
	}
	if paired {
		t.Fatal("markPairSuccess should be ignored after timeout wins the QR state")
	}

	var row struct {
		JID       string `db:"jid"`
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != "" || row.QRCode != "" || row.Connected != 0 {
		t.Fatalf("row = {jid:%q qrcode:%q connected:%d}; want timeout state to remain disconnected without jid", row.JID, row.QRCode, row.Connected)
	}
}

func TestStaleQRTimeoutDoesNotClearReplacementSessionState(t *testing.T) {
	const (
		userID = "replacement-session-user"
		token  = "replacement-session-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "new-session-qr", 1,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "new-session-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	oldKill := make(chan bool, 1)
	newKill := make(chan bool, 1)
	setKillChannel(userID, oldKill)
	setKillChannel(userID, newKill)
	t.Cleanup(func() { deleteKillChannel(userID, newKill) })

	oldClient := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	timeoutResult, err := oldClient.markQRTimeout(oldKill)
	if err != nil {
		t.Fatalf("markQRTimeout: %v", err)
	}
	if timeoutResult != qrTimeoutStaleSession {
		t.Fatalf("markQRTimeout result = %v; want stale session", timeoutResult)
	}

	var row struct {
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.QRCode != "new-session-qr" || row.Connected != 1 {
		t.Fatalf("row = {qrcode:%q connected:%d}; want replacement session state preserved", row.QRCode, row.Connected)
	}
	if cached, found := userinfocache.Get(token); !found || cached.(Values).Get("Qrcode") != "new-session-qr" {
		t.Fatalf("cache qrcode changed; found=%v cached=%v", found, cached)
	}

	jid := types.NewJID("8613536835892", types.DefaultUserServer)
	paired, err := oldClient.markPairSuccess(jid)
	if err != nil {
		t.Fatalf("markPairSuccess after stale timeout: %v", err)
	}
	if paired {
		t.Fatal("stale session PairSuccess should not authorize replacement session")
	}
}

func TestReplacementSessionRejectsOldQRCodeAndPairSuccessBeforeTimeout(t *testing.T) {
	const (
		userID = "replacement-before-timeout-user"
		token  = "replacement-before-timeout-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "new-session-qr", 1,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "new-session-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	oldKill := make(chan bool, 1)
	newKill := make(chan bool, 1)
	setKillChannel(userID, oldKill)
	setKillChannel(userID, newKill)
	t.Cleanup(func() { deleteKillChannel(userID, newKill) })

	oldClient := &MyClient{
		userID:    userID,
		token:     token,
		db:        s.db,
		loginKill: oldKill,
	}
	stored, err := oldClient.storeQRCode("old-session-qr")
	if err != nil {
		t.Fatalf("storeQRCode: %v", err)
	}
	if stored {
		t.Fatal("old session QR should not overwrite replacement session QR")
	}

	jid := types.NewJID("8613536835892", types.DefaultUserServer)
	paired, err := oldClient.markPairSuccess(jid)
	if err != nil {
		t.Fatalf("markPairSuccess: %v", err)
	}
	if paired {
		t.Fatal("old session PairSuccess should not authorize replacement session")
	}

	var row struct {
		JID       string `db:"jid"`
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != "" || row.QRCode != "new-session-qr" || row.Connected != 1 {
		t.Fatalf("row = {jid:%q qrcode:%q connected:%d}; want replacement session state preserved", row.JID, row.QRCode, row.Connected)
	}
	if cached, found := userinfocache.Get(token); !found || cached.(Values).Get("Qrcode") != "new-session-qr" {
		t.Fatalf("cache qrcode changed; found=%v cached=%v", found, cached)
	}
}

func TestPairSuccessStateSurvivesReconnectDisconnect(t *testing.T) {
	const (
		userID = "pair-success-reconnect-disconnect-user"
		token  = "pair-success-reconnect-disconnect-token"
	)

	s := makeTestServer(t)
	if _, err := s.db.Exec(
		`INSERT INTO users (id, name, token, jid, qrcode, connected) VALUES ($1,$2,$3,$4,$5,$6)`,
		userID, "tester", token, "", "pending-qr", 0,
	); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	userinfocache.Set(token, Values{map[string]string{
		"Id":     userID,
		"Token":  token,
		"Jid":    "",
		"Qrcode": "pending-qr",
	}}, 0)
	t.Cleanup(func() { userinfocache.Delete(token) })

	mycli := &MyClient{
		userID: userID,
		token:  token,
		db:     s.db,
	}
	jid := types.NewJID("8613536835892", types.DefaultUserServer)
	paired, err := mycli.markPairSuccess(jid)
	if err != nil {
		t.Fatalf("markPairSuccess: %v", err)
	}
	if !paired {
		t.Fatal("markPairSuccess ignored an active pair success")
	}

	if err := s.setDisconnectedState(userID, false); err != nil {
		t.Fatalf("setDisconnectedState: %v", err)
	}

	var row struct {
		JID       string `db:"jid"`
		QRCode    string `db:"qrcode"`
		Connected int    `db:"connected"`
	}
	if err := s.db.Get(&row, `SELECT jid, qrcode, connected FROM users WHERE id=$1`, userID); err != nil {
		t.Fatalf("read user: %v", err)
	}
	if row.JID != jid.String() || row.QRCode != "" || row.Connected != 1 {
		t.Fatalf("row = {jid:%q qrcode:%q connected:%d}; want pair success state preserved", row.JID, row.QRCode, row.Connected)
	}
}

func TestDeleteSessionIfCurrentPreservesReplacementClients(t *testing.T) {
	const userID = "replacement-client-user"

	oldWA := &whatsmeow.Client{}
	newWA := &whatsmeow.Client{}
	oldMy := &MyClient{userID: userID}
	newMy := &MyClient{userID: userID}
	oldHTTP := resty.New()
	newHTTP := resty.New()

	clientManager.SetWhatsmeowClient(userID, oldWA)
	clientManager.SetMyClient(userID, oldMy)
	clientManager.SetHTTPClient(userID, oldHTTP)
	clientManager.SetWhatsmeowClient(userID, newWA)
	clientManager.SetMyClient(userID, newMy)
	clientManager.SetHTTPClient(userID, newHTTP)
	t.Cleanup(func() {
		clientManager.DeleteSessionIfCurrent(userID, newWA, newMy, newHTTP)
	})

	clientManager.DeleteSessionIfCurrent(userID, oldWA, oldMy, oldHTTP)

	if got := clientManager.GetWhatsmeowClient(userID); got != newWA {
		t.Fatalf("WhatsmeowClient = %p; want replacement %p", got, newWA)
	}
	if got := clientManager.GetMyClient(userID); got != newMy {
		t.Fatalf("MyClient = %p; want replacement %p", got, newMy)
	}
	if got := clientManager.GetHTTPClient(userID); got != newHTTP {
		t.Fatalf("HTTPClient = %p; want replacement %p", got, newHTTP)
	}
}

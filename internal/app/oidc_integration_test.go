//go:build integration

package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jonboulle/clockwork"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/net/html"

	"github.com/gowthamgts/cronpad/internal/config"
)

// TestGenericOIDCDexLogin exercises discovery, browser state/PKCE/nonce, token
// exchange, verified account linking and an authenticated API session against Dex.
func TestGenericOIDCDexLogin(t *testing.T) {
	testcontainers.SkipIfProviderIsNotHealthy(t)
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(nil)
	_ = server.Listener.Close()
	server.Listener = listener
	defer server.Close()
	publicURL := "http://" + listener.Addr().String()
	dexListener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, dexPort, _ := net.SplitHostPort(dexListener.Addr().String())
	_ = dexListener.Close()
	issuer := "http://127.0.0.1:" + dexPort + "/dex"
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatal(err)
	}
	// Dex's local connector and static client configuration follow its upstream
	// config example: https://dexidp.io/docs/connectors/local/ .
	dexConfig := fmt.Sprintf(`issuer: %s
storage:
  type: memory
web:
  http: 0.0.0.0:5556
oauth2:
  skipApprovalScreen: true
enablePasswordDB: true
staticClients:
- id: cronpad-test
  secret: test-secret
  name: cronpad
  redirectURIs: ['%s/auth/oidc:default/callback']
staticPasswords:
- email: alice@example.com
  hash: '%s'
  username: Alice
  userID: dex-alice
`, issuer, publicURL, hash)
	dex, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{ContainerRequest: testcontainers.ContainerRequest{
		Image: "ghcr.io/dexidp/dex:v2.43.1", ExposedPorts: []string{"5556/tcp"}, Cmd: []string{"dex", "serve", "/etc/dex/test.yaml"},
		Files: []testcontainers.ContainerFile{{Reader: strings.NewReader(dexConfig), ContainerFilePath: "/etc/dex/test.yaml", FileMode: 0644}},
		HostConfigModifier: func(h *container.HostConfig) {
			h.PortBindings = network.PortMap{network.MustParsePort("5556/tcp"): []network.PortBinding{{HostIP: netip.MustParseAddr("127.0.0.1"), HostPort: dexPort}}}
		},
		WaitingFor: wait.ForHTTP("/dex/.well-known/openid-configuration").WithPort("5556/tcp").WithStartupTimeout(time.Minute),
	}, Started: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dex.Terminate(context.Background()); err != nil {
			t.Log(err)
		}
	})
	c, err := config.Load(ctx, "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Auth.DevLogin = false
	c.Auth.OIDC.Issuer = issuer
	c.Auth.OIDC.ClientID = "cronpad-test"
	c.Auth.OIDC.ClientSecret = "test-secret"
	c.Auth.OIDC.DisplayName = "Company Login"
	c.Server.PublicURL = publicURL
	dir := t.TempDir()
	c.Database.Path = filepath.Join(dir, "c.db")
	c.Database.HistoryPath = filepath.Join(dir, "h.db")
	a, err := New(ctx, c, "oss", nil, nil, clockwork.NewRealClock(), slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = a.Close() }()
	server.Config.Handler = a.Handler
	server.Start()
	client := server.Client()
	client.Timeout = 15 * time.Second
	jar, _ := cookiejar.New(nil)
	client.Jar = jar
	client.CheckRedirect = func(r *http.Request, _ []*http.Request) error {
		if r.URL.Host == listener.Addr().String() && r.URL.Path == "/" {
			return http.ErrUseLastResponse
		}
		return nil
	}
	login, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+"/auth/oidc:default/login", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(login)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("Dex login page: %d %s", resp.StatusCode, body)
	}
	action := formAction(t, string(body))
	target, err := resp.Request.URL.Parse(action)
	if err != nil {
		t.Fatal(err)
	}
	credentials := url.Values{"login": {"alice@example.com"}, "password": {"test-password"}}
	submit, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(credentials.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	submit.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err = client.Do(submit)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/" {
		t.Fatalf("OIDC callback: %d location=%s %s", resp.StatusCode, resp.Header.Get("Location"), body)
	}
	me, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+"/api/v1/me", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(me)
	if err != nil {
		t.Fatal(err)
	}
	body, err = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || !strings.Contains(string(body), "alice@example.com") {
		t.Fatalf("session: %d %s %v", resp.StatusCode, body, err)
	}
	var provider string
	if err = a.DBs.Control.Reader.QueryRowContext(ctx, "SELECT i.provider FROM identities i JOIN users u ON u.id=i.user_id WHERE u.email='alice@example.com' AND i.subject<>''").Scan(&provider); err != nil || provider != "oidc:default" {
		t.Fatalf("provider=%s err=%v", provider, err)
	}
	features, err := http.NewRequestWithContext(ctx, http.MethodGet, publicURL+"/api/v1/features", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err = client.Do(features)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.Contains(string(body), `"oidc:default"`) || !strings.Contains(string(body), `"oidc_display_name":"Company Login"`) {
		t.Fatalf("features: %s", body)
	}
}
func formAction(t *testing.T, body string) string {
	t.Helper()
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	var action string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "form" {
			for _, a := range n.Attr {
				if a.Key == "action" {
					action = a.Val
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	if action == "" {
		t.Fatalf("Dex form missing: %s", body)
	}
	return action
}

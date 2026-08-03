package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mau.fi/whatsmeow"
	"golang.org/x/net/proxy"
)

type recordingWhatsAppProxyClient struct {
	socksDialer  proxy.Dialer
	proxyAddress string
}

func (c *recordingWhatsAppProxyClient) SetSOCKSProxy(dialer proxy.Dialer, _ ...whatsmeow.SetProxyOptions) {
	c.socksDialer = dialer
}

func (c *recordingWhatsAppProxyClient) SetProxyAddress(address string, _ ...whatsmeow.SetProxyOptions) error {
	c.proxyAddress = address
	return nil
}

func TestNewWebhookHTTPClientDisablesAllProxies(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:1")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:1")

	client := newWebhookHTTPClient()
	transport, err := client.Transport()
	if err != nil {
		t.Fatalf("get webhook transport: %v", err)
	}
	if transport.Proxy != nil {
		t.Fatal("webhook transport must not inherit account or environment proxy")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	resp, err := client.R().Get(server.URL)
	if err != nil {
		t.Fatalf("direct webhook request failed: %v", err)
	}
	if resp.StatusCode() != http.StatusNoContent {
		t.Fatalf("status = %d; want %d", resp.StatusCode(), http.StatusNoContent)
	}
}

func TestNewWebhookHTTPClientPreservesTimeout(t *testing.T) {
	client := newWebhookHTTPClient()
	if got, want := client.GetClient().Timeout, 30*time.Second; got != want {
		t.Fatalf("timeout = %s; want %s", got, want)
	}
}

func TestConfigureWhatsAppProxyDoesNotChangeWebhookTransport(t *testing.T) {
	tests := []struct {
		name             string
		proxyURL         string
		wantSOCKSDialer  bool
		wantProxyAddress string
	}{
		{
			name:            "socks5 configures only WhatsApp SOCKS proxy",
			proxyURL:        "socks5://127.0.0.1:1080",
			wantSOCKSDialer: true,
		},
		{
			name:             "http configures only WhatsApp address proxy",
			proxyURL:         "http://127.0.0.1:8080",
			wantProxyAddress: "http://127.0.0.1:8080",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			webhookClient := newWebhookHTTPClient()
			whatsAppClient := &recordingWhatsAppProxyClient{}

			configureWhatsAppProxy(whatsAppClient, tt.proxyURL)

			transport, err := webhookClient.Transport()
			if err != nil {
				t.Fatalf("get webhook transport: %v", err)
			}
			if transport.Proxy != nil {
				t.Fatal("account proxy configuration must not change webhook transport")
			}
			if got := whatsAppClient.socksDialer != nil; got != tt.wantSOCKSDialer {
				t.Fatalf("SOCKS dialer configured = %t; want %t", got, tt.wantSOCKSDialer)
			}
			if whatsAppClient.proxyAddress != tt.wantProxyAddress {
				t.Fatalf("proxy address = %q; want %q", whatsAppClient.proxyAddress, tt.wantProxyAddress)
			}
		})
	}
}

package mcpgdrive

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

// Login runs a one-time OAuth consent flow and prints the resulting refresh
// token for the user to store in AGENTBOX_GDRIVE_REFRESH_TOKEN. It needs
// AGENTBOX_GDRIVE_CLIENT_ID/SECRET set. An HTTP server captures the OAuth
// redirect, and Google always redirects the browser to the loopback host
// (http://localhost:8765/callback), so a browser must be reachable.
//
// By default the server binds to that same loopback host, which is right when
// running the binary directly on the user's machine. To run login inside the
// headless container instead, set AGENTBOX_GDRIVE_LOGIN_ADDR=0.0.0.0:8765 and
// publish the port (docker compose run --rm -p 127.0.0.1:8765:8765 ...): the
// host browser's redirect to localhost:8765 is then forwarded to the container.
func Login(ctx context.Context) error {
	clientID := strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("set AGENTBOX_GDRIVE_CLIENT_ID and AGENTBOX_GDRIVE_CLIENT_SECRET first")
	}
	conf := Config{ClientID: clientID, ClientSecret: clientSecret}.oauthConfig()

	// The redirect always targets loopbackAddr; the listen address may differ so
	// the server can sit behind a published container port (see doc comment).
	listenAddr := strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_LOGIN_ADDR"))
	if listenAddr == "" {
		listenAddr = loopbackAddr
	}
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s (is another login running?): %w", listenAddr, err)
	}
	defer ln.Close()

	codeCh := make(chan string, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "no code in callback: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		fmt.Fprintln(w, "Authorized — you can close this tab and return to the terminal.")
		codeCh <- code
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(ln)
	defer srv.Close()

	// AccessTypeOffline + ApprovalForce guarantee a refresh token is returned,
	// even on a repeat consent.
	authURL := conf.AuthCodeURL("agentbox", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Println("Open this URL in your browser, authorize, then return here:")
	fmt.Println()
	fmt.Println("  " + authURL)
	fmt.Println()

	var code string
	select {
	case code = <-codeCh:
	case <-time.After(5 * time.Minute):
		return fmt.Errorf("timed out waiting for authorization")
	case <-ctx.Done():
		return ctx.Err()
	}

	tok, err := conf.Exchange(ctx, code)
	if err != nil {
		return fmt.Errorf("exchange code: %w", err)
	}
	if tok.RefreshToken == "" {
		return fmt.Errorf("no refresh token returned; revoke the app's access and try again (it's only issued on first consent)")
	}

	fmt.Println("Success. Add this line to your .env (keep it secret):")
	fmt.Println()
	fmt.Println("  AGENTBOX_GDRIVE_REFRESH_TOKEN=" + tok.RefreshToken)
	fmt.Println()
	fmt.Println("Note: this long-lived token now sits in your terminal scrollback/history.")
	fmt.Println("Clear it (and any session log) after copying it into your .env.")
	return nil
}

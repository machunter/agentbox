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

// Login runs a one-time OAuth consent flow on the local machine and prints the
// resulting refresh token for the user to store in AGENTBOX_GDRIVE_REFRESH_TOKEN.
// It needs AGENTBOX_GDRIVE_CLIENT_ID/SECRET set. A loopback HTTP server captures
// the redirect, so this must run somewhere with a browser (the user's machine),
// not inside the headless container.
func Login(ctx context.Context) error {
	clientID := strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_CLIENT_ID"))
	clientSecret := strings.TrimSpace(os.Getenv("AGENTBOX_GDRIVE_CLIENT_SECRET"))
	if clientID == "" || clientSecret == "" {
		return fmt.Errorf("set AGENTBOX_GDRIVE_CLIENT_ID and AGENTBOX_GDRIVE_CLIENT_SECRET first")
	}
	conf := Config{ClientID: clientID, ClientSecret: clientSecret}.oauthConfig()

	ln, err := net.Listen("tcp", loopbackAddr)
	if err != nil {
		return fmt.Errorf("listen on %s (is another login running?): %w", loopbackAddr, err)
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

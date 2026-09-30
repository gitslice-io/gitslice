package clerk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestUserClientVerifiedEmails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/users/user_123" || r.Header.Get("Authorization") != "Bearer sk_test" {
			http.Error(w, "bad request", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"id":"user_123","email_addresses":[
			{"email_address":"Me@Example.com","verification":{"status":"verified"}},
			{"email_address":"pending@example.com","verification":{"status":"unverified"}},
			{"email_address":"none@example.com","verification":null}
		]}`))
	}))
	defer srv.Close()

	client := NewUserClient("sk_test").WithBaseURL(srv.URL)
	got, err := client.VerifiedEmails(context.Background(), "user_123")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"Me@Example.com"}) {
		t.Fatalf("VerifiedEmails = %v", got)
	}
	if _, err := client.VerifiedEmails(context.Background(), "user_404"); err == nil {
		t.Fatal("expected error for non-200 response")
	}
	if NewUserClient("  ") != nil {
		t.Fatal("empty secret key must disable the client")
	}
}

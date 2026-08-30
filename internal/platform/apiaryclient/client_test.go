package apiaryclient_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	appmedia "github.com/sbezhuk/beebase-media-service/internal/application/media"
	"github.com/sbezhuk/beebase-media-service/internal/platform/apiaryclient"
)

func TestClient_Verify_Owned(t *testing.T) {
	apiaryID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" {
			t.Errorf("Authorization header = %q, want forwarded bearer token", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/v1/apiaries/"+apiaryID.String() {
			t.Errorf("path = %q, want to include the apiary id", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := apiaryclient.New(srv.URL)
	if err := client.Verify(context.Background(), "good-token", apiaryID); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestClient_Verify_NotOwned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	client := apiaryclient.New(srv.URL)
	err := client.Verify(context.Background(), "some-token", uuid.New())
	if !errors.Is(err, appmedia.ErrApiaryNotFound) {
		t.Fatalf("Verify against a 404: got %v, want ErrApiaryNotFound", err)
	}
}

func TestClient_Verify_UnexpectedStatusFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := apiaryclient.New(srv.URL)
	err := client.Verify(context.Background(), "some-token", uuid.New())
	if err == nil {
		t.Fatal("Verify against a 500: got nil error, want a failure")
	}
	if errors.Is(err, appmedia.ErrApiaryNotFound) {
		t.Fatal("Verify against a 500 should not be reported as ErrApiaryNotFound: that would mask apiary-service being broken as a plain 404")
	}
}

func TestClient_Verify_UnreachableServer(t *testing.T) {
	client := apiaryclient.New("http://127.0.0.1:1") // nothing listens here
	err := client.Verify(context.Background(), "some-token", uuid.New())
	if err == nil {
		t.Fatal("Verify against an unreachable server: got nil error, want a failure")
	}
}

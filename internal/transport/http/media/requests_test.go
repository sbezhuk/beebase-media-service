package media

import (
	"testing"

	"github.com/google/uuid"
)

func TestUploadRequest_Validate(t *testing.T) {
	validMediaID := uuid.New().String()

	tests := []struct {
		name string
		req  UploadRequest
		want map[string]string
	}{
		{
			name: "valid, no media_id",
			req:  UploadRequest{},
			want: map[string]string{},
		},
		{
			name: "valid, with media_id",
			req:  UploadRequest{MediaID: validMediaID},
			want: map[string]string{},
		},
		{
			name: "malformed media_id",
			req:  UploadRequest{MediaID: "not-a-uuid"},
			want: map[string]string{"media_id": CodeMediaIDInvalid},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.req.Validate()
			if len(got) != len(tt.want) {
				t.Fatalf("Validate() = %v, want %v", got, tt.want)
			}
			for field, wantCode := range tt.want {
				if gotCode, ok := got[field]; !ok || gotCode != wantCode {
					t.Errorf("field %q: got code %q, want %q", field, gotCode, wantCode)
				}
			}
		})
	}
}

func TestIDsQuery_Validate(t *testing.T) {
	validID := uuid.New().String()
	anotherValidID := uuid.New().String()

	tests := []struct {
		name string
		q    IDsQuery
		want map[string]string
	}{
		{
			name: "no ids",
			q:    IDsQuery{},
			want: map[string]string{},
		},
		{
			name: "valid ids",
			q:    IDsQuery{IDs: []string{validID, anotherValidID}},
			want: map[string]string{},
		},
		{
			name: "one malformed id among valid ones",
			q:    IDsQuery{IDs: []string{validID, "not-a-uuid"}},
			want: map[string]string{"ids": CodeIDsInvalid},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.q.Validate()
			if len(got) != len(tt.want) {
				t.Fatalf("Validate() = %v, want %v", got, tt.want)
			}
			for field, wantCode := range tt.want {
				if gotCode, ok := got[field]; !ok || gotCode != wantCode {
					t.Errorf("field %q: got code %q, want %q", field, gotCode, wantCode)
				}
			}
		})
	}
}

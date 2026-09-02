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

func TestAttachRequest_Validate(t *testing.T) {
	validOwnerID := uuid.New().String()

	tests := []struct {
		name string
		req  AttachRequest
		want map[string]string
	}{
		{
			name: "valid",
			req:  AttachRequest{OwnerType: "APIARY", OwnerID: validOwnerID},
			want: map[string]string{},
		},
		{
			name: "missing owner_type",
			req:  AttachRequest{OwnerType: "", OwnerID: validOwnerID},
			want: map[string]string{"owner_type": CodeOwnerTypeRequired},
		},
		{
			name: "invalid owner_type",
			req:  AttachRequest{OwnerType: "hive_box", OwnerID: validOwnerID},
			want: map[string]string{"owner_type": CodeOwnerTypeInvalid},
		},
		{
			name: "missing owner_id",
			req:  AttachRequest{OwnerType: "APIARY", OwnerID: ""},
			want: map[string]string{"owner_id": CodeOwnerIDRequired},
		},
		{
			name: "malformed owner_id",
			req:  AttachRequest{OwnerType: "APIARY", OwnerID: "not-a-uuid"},
			want: map[string]string{"owner_id": CodeOwnerIDInvalid},
		},
		{
			name: "everything wrong at once",
			req:  AttachRequest{OwnerType: "bad", OwnerID: ""},
			want: map[string]string{"owner_type": CodeOwnerTypeInvalid, "owner_id": CodeOwnerIDRequired},
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

func TestListQuery_Validate(t *testing.T) {
	validOwnerID := uuid.New().String()

	tests := []struct {
		name string
		q    ListQuery
		want map[string]string
	}{
		{
			name: "valid",
			q:    ListQuery{OwnerType: "APIARY", OwnerID: validOwnerID},
			want: map[string]string{},
		},
		{
			name: "missing owner_type and owner_id",
			q:    ListQuery{},
			want: map[string]string{"owner_type": CodeOwnerTypeRequired, "owner_id": CodeOwnerIDRequired},
		},
		{
			name: "invalid owner_type",
			q:    ListQuery{OwnerType: "box", OwnerID: validOwnerID},
			want: map[string]string{"owner_type": CodeOwnerTypeInvalid},
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

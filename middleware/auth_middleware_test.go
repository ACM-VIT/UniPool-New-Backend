package middleware

import (
	"testing"

	firebaseauth "firebase.google.com/go/v4/auth"
)

func TestFallbackAuthNameFromEmail(t *testing.T) {
	tests := []struct {
		name  string
		email string
		want  string
	}{
		{
			name:  "apple private relay",
			email: "abc123@privaterelay.appleid.com",
			want:  "Apple User",
		},
		{
			name:  "normal email local part",
			email: "name.dotted-student@example.com",
			want:  "name dotted student",
		},
		{
			name:  "empty fallback",
			email: "",
			want:  "UniPool User",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fallbackAuthNameFromEmail(tt.email); got != tt.want {
				t.Fatalf("fallbackAuthNameFromEmail(%q) = %q, want %q", tt.email, got, tt.want)
			}
		})
	}
}

func TestFirstStringValue(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "string", value: "  user@example.com  ", want: "user@example.com"},
		{name: "string slice", value: []string{"", " user@example.com "}, want: "user@example.com"},
		{name: "interface slice", value: []interface{}{"", " user@example.com "}, want: "user@example.com"},
		{name: "nested interface slice", value: []interface{}{[]interface{}{" user@example.com "}}, want: "user@example.com"},
		{name: "unsupported", value: 42, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := firstStringValue(tt.value); got != tt.want {
				t.Fatalf("firstStringValue(%#v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestAuthTokenEmail(t *testing.T) {
	tests := []struct {
		name    string
		decoded *firebaseauth.Token
		want    string
	}{
		{
			name: "email claim wins and is normalized",
			decoded: &firebaseauth.Token{
				Claims: map[string]interface{}{
					"email": " Student@Example.COM ",
				},
				Firebase: firebaseauth.FirebaseInfo{
					Identities: map[string]interface{}{
						"email": []interface{}{"other@example.com"},
					},
				},
			},
			want: "student@example.com",
		},
		{
			name: "firebase identity email fallback",
			decoded: &firebaseauth.Token{
				Claims: map[string]interface{}{},
				Firebase: firebaseauth.FirebaseInfo{
					Identities: map[string]interface{}{
						"email": []interface{}{" Student@Example.COM "},
					},
				},
			},
			want: "student@example.com",
		},
		{
			name:    "nil token",
			decoded: nil,
			want:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := authTokenEmail(tt.decoded); got != tt.want {
				t.Fatalf("authTokenEmail() = %q, want %q", got, tt.want)
			}
		})
	}
}

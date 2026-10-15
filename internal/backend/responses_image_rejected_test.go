package backend

import "testing"

func TestImageAccountRejected(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"disguised 404", 404, `{"detail":"Model not found"}`, true},
		{"case insensitive detail", 404, `{"detail":"model not found"}`, true},
		{"padded detail", 404, "  {\"detail\":\" Model not found \"}  ", true},
		{"different detail", 404, `{"detail":"Not found"}`, false},
		{"empty body", 404, ``, false},
		{"html body", 404, `<html><body>Not Found</body></html>`, false},
		{"200 is never a rejection", 200, `{"detail":"Model not found"}`, false},
		{"500 is never a rejection", 500, `{"detail":"Model not found"}`, false},
	}
	for _, tc := range cases {
		if got := imageAccountRejected(tc.status, []byte(tc.body)); got != tc.want {
			t.Fatalf("%s: imageAccountRejected(%d, %q) = %v, want %v", tc.name, tc.status, tc.body, got, tc.want)
		}
	}
}

// TestReportImageAccountRejected checks the disguised 404 cools the account
// down and that a lookup without the capability is simply ignored.
func TestReportImageAccountRejected(t *testing.T) {
	recorder := &rejectionRecorder{}
	client := &Client{AccessToken: "at-1", lookup: recorder}
	client.reportImageAccountRejected()
	if recorder.marked != "at-1" {
		t.Fatalf("marked = %q, want at-1", recorder.marked)
	}

	// No capability: must not panic, and must not record anything.
	plain := &Client{AccessToken: "at-2", lookup: testAccountLookup{}}
	plain.reportImageAccountRejected()

	// No access token: nothing to cool down.
	tokenless := &Client{lookup: recorder}
	tokenless.reportImageAccountRejected()
	if recorder.marked != "at-1" {
		t.Fatalf("tokenless call changed the mark to %q", recorder.marked)
	}
}

type rejectionRecorder struct {
	testAccountLookup
	marked string
}

func (r *rejectionRecorder) MarkImageAccountRejected(accessToken string) {
	r.marked = accessToken
}

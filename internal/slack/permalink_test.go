package slack

import "testing"

func TestParsePermalink(t *testing.T) {
	tests := []struct {
		name, in string
		want     Permalink
		wantErr  bool
	}{
		{
			name: "thread reply",
			in:   "https://example.slack.com/archives/C0123ABCD/p1700000123456789?thread_ts=1700000000.000100&cid=C0123ABCD",
			want: Permalink{ChannelID: "C0123ABCD", TS: "1700000123.456789", ThreadTS: "1700000000.000100"},
		},
		{
			name: "top-level message",
			in:   "https://example.slack.com/archives/C0123ABCD/p1700000123456789",
			want: Permalink{ChannelID: "C0123ABCD", TS: "1700000123.456789"},
		},
		{name: "not a permalink", in: "https://example.com/foo", wantErr: true},
		{name: "mismatched cid", in: "https://example.slack.com/archives/C0123ABCD/p1700000123456789?cid=C999", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParsePermalink(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseMinInterval(t *testing.T) {
	if d, err := ParseMinInterval("750ms"); err != nil || d.Milliseconds() != 750 {
		t.Fatalf("750ms: got %v, %v", d, err)
	}
	if d, err := ParseMinInterval("0s"); err != nil || d != 0 {
		t.Fatalf("0s: got %v, %v", d, err)
	}
	for _, bad := range []string{"", "fast", "-1s", "2m"} {
		if _, err := ParseMinInterval(bad); err == nil {
			t.Fatalf("%q: expected error", bad)
		}
	}
}

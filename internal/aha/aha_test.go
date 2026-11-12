package aha

import "testing"

// Vectors from AVM's "Session-IDs im FRITZ!Box Webinterface" technical note.
func TestChallengeResponse(t *testing.T) {
	cases := []struct{ challenge, password, want string }{
		{"2$10000$5A1711$2000$5A1722", "1example!", "5A1722$1798a1672bca7c6463d6b245f82b53703b0f50813401b03e4045a5861e689adb"},
		{"1234567z", "äbc", "1234567z-9e224a41eeefa284df7bb0f26c2913e2"},
	}
	for _, c := range cases {
		got, err := ChallengeResponse(c.challenge, c.password)
		if err != nil {
			t.Fatal(err)
		}
		if got != c.want {
			t.Errorf("ChallengeResponse(%q) = %q, want %q", c.challenge, got, c.want)
		}
	}
}

func TestTempEncoding(t *testing.T) {
	for in, want := range map[string]int{"8": 16, "21.5": 43, "21,5": 43, "28": 56, "off": TempOff, "on": TempOn} {
		got, err := EncodeTemp(in)
		if err != nil || got != want {
			t.Errorf("EncodeTemp(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"7.5", "28.5", "warm"} {
		if _, err := EncodeTemp(bad); err == nil {
			t.Errorf("EncodeTemp(%q) should fail", bad)
		}
	}
	for v, want := range map[int]string{43: "21.5°C", TempOff: "off", TempOn: "on", 0: "–"} {
		if got := DecodeTemp(v); got != want {
			t.Errorf("DecodeTemp(%d) = %q, want %q", v, got, want)
		}
	}
}

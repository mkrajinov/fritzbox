package fritz

import "testing"

func TestRecordingURL(t *testing.T) {
	cases := []struct{ list, path, want string }{
		{"https://fritz.box:49443/tamcalllist.lua?sid=abc&tamindex=0", "/data/tam/rec/rec.0.000",
			"https://fritz.box:49443/download.lua?path=%2Fdata%2Ftam%2Frec%2Frec.0.000&sid=abc"},
		{"https://fritz.box:49443/tamcalllist.lua?sid=abc", "/download.lua?path=/data/tam/rec/rec.0.001",
			"https://fritz.box:49443/download.lua?path=%2Fdata%2Ftam%2Frec%2Frec.0.001&sid=abc"},
		{"/tamcalls.xml?sid=x", "/data/r", "/download.lua?path=%2Fdata%2Fr&sid=x"},
	}
	for _, c := range cases {
		got, err := recordingURL(c.list, c.path)
		if err != nil || got != c.want {
			t.Errorf("recordingURL(%q, %q) = %q, %v; want %q", c.list, c.path, got, err, c.want)
		}
	}
	if _, err := recordingURL("/x?sid=1", ""); err == nil {
		t.Error("empty path should fail")
	}
}

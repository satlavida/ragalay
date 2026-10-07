package embed

import (
	"strings"
	"testing"
)

func TestProfiles(t *testing.T) {
	if p := Profiles()[0]; p.Name != DefaultProfile {
		t.Fatalf("default profile must be listed first, got %s", p.Name)
	}
	for _, p := range Profiles() {
		if p.Local() && (p.IndexModel == "" || len(p.IndexRevision) != 40 || !p.AllowsDim(p.DefaultDim) || p.Pooling == "") {
			t.Errorf("local profile %s is incomplete: %+v", p.Name, p)
		}
	}
	g, _ := Lookup(Gemma2)
	if g.AllowsDim(1024) || !g.AllowsDim(256) || g.NonCommercial {
		t.Errorf("gemma: %+v", g)
	}
	j, _ := Lookup(JinaV5)
	if !j.NonCommercial || !j.AllowsDim(32) {
		t.Errorf("jina: %+v", j)
	}
	o, _ := Lookup(OpenAI)
	if o.Local() || !o.AllowsDim(1536) || o.AllowsDim(0) {
		t.Errorf("openai: %+v", o)
	}
	if _, ok := Lookup("nope"); ok {
		t.Error("unknown profile found")
	}
}

func TestHTTPID(t *testing.T) {
	if got := HTTPID("m", "h:1", 8, ""); got != "openai/m@h:1:8" {
		t.Fatalf("HTTPID = %s", got)
	}
	a, b := HTTPID("m", "h", 8, "x"), HTTPID("m", "h", 8, "y")
	if a == b || !strings.HasPrefix(a, "openai/m@h:8#") {
		t.Fatalf("variants must differ: %s %s", a, b)
	}
}

package config

import "testing"

func TestStateHome(t *testing.T) {
	home := "/synthetic/user"
	cases := []struct {
		name string
		env  map[string]string
		want string
		err  bool
	}{
		{"default", nil, "/synthetic/user/.local/state/askother", false},
		{"override", map[string]string{"ASKOTHER_HOME": "/tmp/askother-x/"}, "/tmp/askother-x", false},
		{"empty override ignored", map[string]string{"ASKOTHER_HOME": ""}, "/synthetic/user/.local/state/askother", false},
		{"relative override", map[string]string{"ASKOTHER_HOME": "state"}, "", true},
	}
	for _, c := range cases {
		got, err := StateHome(c.env, home)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("%s: got %q, %v; want %q, err=%v", c.name, got, err, c.want, c.err)
		}
	}
	if _, err := StateHome(nil, ""); err == nil {
		t.Error("unknown home: want error")
	}
}

func TestConfigFile(t *testing.T) {
	got, err := ConfigFile(nil, "/synthetic/user")
	if err != nil || got != "/synthetic/user/.config/askother/config.json" {
		t.Errorf("default: got %q, %v", got, err)
	}
	got, err = ConfigFile(map[string]string{"ASKOTHER_CONFIG": "/tmp/c.json"}, "")
	if err != nil || got != "/tmp/c.json" {
		t.Errorf("override: got %q, %v", got, err)
	}
	if _, err := ConfigFile(map[string]string{"ASKOTHER_CONFIG": "c.json"}, "/h"); err == nil {
		t.Error("relative ASKOTHER_CONFIG: want error")
	}
	if _, err := ConfigFile(nil, "rel"); err == nil {
		t.Error("relative home: want error")
	}
}
